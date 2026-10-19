package atlas

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Source is a prepared font and its supported/requestable Unicode characters.
// Sources are ordered by priority; the first source containing a rune wins.
// Include '?' in a source's Charset if the prepared font supports it.
type Source struct {
	FontPath string
	Charset  []rune
	Name     string
	Custom   bool
}

type Options struct {
	// Executable is the native helper path. Empty uses the vendor location
	// beside the running application (or ../vendor in the development module).
	Executable string
	// WorkDir holds a unique temporary child directory, removed after the call.
	// Empty uses the operating system's temporary directory.
	WorkDir string
	// Keys restricts generation to these keys plus '?'. Nil uses all sources.
	// Sentinel zero is never sent to the generator.
	Keys []rune
	// RawPixels uses lossless RGBA transport instead of PNG. Result.PNG is nil.
	// Leave false for diagnostic consumers that need the encoded PNG.
	RawPixels bool
	// Threads zero uses max(1, min(16, NumCPU()-2)).
	Threads  int
	Progress func(string)
}

type SourceReport struct {
	Font       string `json:"font"`
	Characters int    `json:"characters"`
	Custom     bool   `json:"custom"`
}

type Report struct {
	Sources                []SourceReport `json:"sources"`
	DefaultGlyphCharacters []string       `json:"default_glyph_characters"`
	Threads                int            `json:"threads"`
	CacheHit               bool           `json:"cache_hit"`
}

type Result struct {
	// PNG contains the encoded atlas unless Options.RawPixels was enabled.
	PNG []byte
	// Image and Pixels contain the base level in straight (non-premultiplied)
	// RGBA8. Pixels aliases Image.Pix; TexturePixels builds the full mip chain.
	Image    *image.NRGBA
	Pixels   []byte
	Glyphs   map[rune]Glyph
	Metadata []byte
	Report   Report
}

// ExecutablePath resolves only the bundled native helper; it never searches PATH.
func ExecutablePath() (string, error) {
	var bases []string
	if executable, err := os.Executable(); err == nil {
		bases = append(bases, filepath.Dir(executable))
	}
	if cwd, err := os.Getwd(); err == nil {
		bases = append(bases, cwd, filepath.Dir(cwd))
	}
	for _, base := range bases {
		candidate := filepath.Join(base, "vendor", "msdf-atlas-gen", "msdf-atlas-gen.exe")
		if st, err := os.Stat(candidate); err == nil && st.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("bundled msdf-atlas-gen.exe not found; set atlas.Options.Executable")
}

// Generate invokes msdf-atlas-gen directly, without a shell or Python runtime.
// It always generates 32px MTSDF, range 8, top origin, power-of-two rectangles,
// aligned pixels, and no kerning. Font preparation and caching belong to callers.
func Generate(ctx context.Context, sources []Source, opts Options) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	selected, missing, err := selectSources(sources, opts.Keys)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no prepared font can draw the requested glyphs")
	}
	threads := opts.Threads
	if threads < 0 {
		return nil, fmt.Errorf("atlas threads must not be negative")
	}
	if threads == 0 {
		threads = max(1, min(16, runtime.NumCPU()-2))
	}
	executable := opts.Executable
	if executable == "" {
		executable, err = ExecutablePath()
		if err != nil {
			return nil, err
		}
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	if opts.WorkDir != "" {
		if err := os.MkdirAll(opts.WorkDir, 0755); err != nil {
			return nil, fmt.Errorf("create atlas workspace: %w", err)
		}
	}
	work, err := os.MkdirTemp(opts.WorkDir, "atlas-")
	if err != nil {
		return nil, fmt.Errorf("create atlas workspace: %w", err)
	}
	defer os.RemoveAll(work)
	work, err = filepath.Abs(work)
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, len(selected)*5+24)
	report := Report{Sources: []SourceReport{}, DefaultGlyphCharacters: []string{}, Threads: threads}
	expected := map[rune]bool{}
	for i, source := range selected {
		fontPath, err := filepath.Abs(source.FontPath)
		if err != nil {
			return nil, err
		}
		charset := filepath.Join(work, fmt.Sprintf("chars-%d.txt", i))
		chars := make([]string, len(source.Charset))
		for j, cp := range source.Charset {
			chars[j] = fmt.Sprintf("0x%X", cp)
			expected[cp] = true
		}
		if err := os.WriteFile(charset, []byte(strings.Join(chars, ",")), 0600); err != nil {
			return nil, err
		}
		if i > 0 {
			args = append(args, "-and")
		}
		args = append(args, "-font", fontPath, "-charset", charset)
		name := source.Name
		if name == "" {
			name = filepath.Base(source.FontPath)
		}
		report.Sources = append(report.Sources, SourceReport{Font: name, Characters: len(chars), Custom: source.Custom})
	}
	for _, cp := range missing {
		report.DefaultGlyphCharacters = append(report.DefaultGlyphCharacters, fmt.Sprintf("U+%04X", cp))
	}
	format := "png"
	if opts.RawPixels {
		format = "rgba"
		args = append(args, "-format", format)
	}
	imagePath, metaPath := filepath.Join(work, "atlas."+format), filepath.Join(work, "atlas.json")
	args = append(args, "-type", "mtsdf", "-size", "32", "-pxrange", "8", "-potr", "-yorigin", "top", "-nokerning", "-pxalign", "on",
		"-imageout", imagePath, "-json", metaPath, "-threads", strconv.Itoa(threads))
	if opts.Progress != nil {
		opts.Progress(fmt.Sprintf("Generating atlas: %d glyphs / %d threads", len(expected), threads))
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = work
	configureProcess(cmd)
	var stdout, stderr tailBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("atlas generation: %w", ctx.Err())
		}
		detail := stderr.String()
		if detail == "" {
			detail = stdout.String()
		}
		return nil, fmt.Errorf("atlas generation failed: %w: %s", err, detail)
	}
	meta, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("read generated glyph metadata: %w", err)
	}
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return nil, fmt.Errorf("read generated atlas %s: %w", strings.ToUpper(format), err)
	}
	decode := decodeResult
	if opts.RawPixels {
		decode = decodeRawResult
	}
	result, err := decode(data, meta, expected)
	if err != nil {
		return nil, err
	}
	result.Report = report
	return result, nil
}

func selectSources(sources []Source, keys []rune) ([]Source, []rune, error) {
	required := map[rune]bool{'?': true}
	add := func(cp rune) error {
		if !utf8.ValidRune(cp) {
			return fmt.Errorf("invalid Unicode character U+%X", cp)
		}
		if cp != 0 {
			required[cp] = true
		}
		return nil
	}
	if keys == nil {
		for _, s := range sources {
			for _, cp := range s.Charset {
				if err := add(cp); err != nil {
					return nil, nil, err
				}
			}
		}
	} else {
		for _, cp := range keys {
			if err := add(cp); err != nil {
				return nil, nil, err
			}
		}
	}
	var selected []Source
	for _, source := range sources {
		chars := make([]rune, 0, len(source.Charset))
		for _, cp := range source.Charset {
			if !utf8.ValidRune(cp) {
				return nil, nil, fmt.Errorf("invalid source character U+%X", cp)
			}
			if required[cp] {
				chars = append(chars, cp)
				delete(required, cp)
			}
		}
		if len(chars) == 0 {
			continue
		}
		if source.FontPath == "" {
			return nil, nil, fmt.Errorf("prepared source font path is empty")
		}
		slices.Sort(chars)
		source.Charset = chars
		selected = append(selected, source)
	}
	missing := make([]rune, 0, len(required))
	for cp := range required {
		missing = append(missing, cp)
	}
	slices.Sort(missing)
	return selected, missing, nil
}

func decodeResult(data, metadata []byte, expected map[rune]bool) (*Result, error) {
	return decodeAtlasResult(data, metadata, expected, false)
}

func decodeRawResult(data, metadata []byte, expected map[rune]bool) (*Result, error) {
	return decodeAtlasResult(data, metadata, expected, true)
}

func decodeAtlasResult(data, metadata []byte, expected map[rune]bool, rawPixels bool) (*Result, error) {
	var doc struct {
		Atlas struct {
			Type          string  `json:"type"`
			YOrigin       string  `json:"yOrigin"`
			Size          float64 `json:"size"`
			DistanceRange float64 `json:"distanceRange"`
			Width         int     `json:"width"`
			Height        int     `json:"height"`
		} `json:"atlas"`
		Glyphs   []Glyph `json:"glyphs"`
		Variants []struct {
			Glyphs []Glyph `json:"glyphs"`
		} `json:"variants"`
	}
	if err := json.Unmarshal(metadata, &doc); err != nil {
		return nil, fmt.Errorf("decode atlas metadata: %w", err)
	}
	if doc.Atlas.Type != "mtsdf" || doc.Atlas.YOrigin != "top" || doc.Atlas.Size != EmSize || doc.Atlas.DistanceRange != PixelRange {
		return nil, fmt.Errorf("unexpected atlas format: need top-origin 32px MTSDF with range 8")
	}
	glyphs := map[rune]Glyph{}
	add := func(list []Glyph) error {
		for _, g := range list {
			if !utf8.ValidRune(g.Unicode) {
				return fmt.Errorf("invalid generated Unicode character")
			}
			if _, err := Metrics(g); err != nil {
				return err
			}
			glyphs[g.Unicode] = g
		}
		return nil
	}
	if doc.Variants == nil {
		if err := add(doc.Glyphs); err != nil {
			return nil, err
		}
	} else {
		for _, variant := range doc.Variants {
			if err := add(variant.Glyphs); err != nil {
				return nil, err
			}
		}
	}
	for cp := range expected {
		if _, ok := glyphs[cp]; !ok {
			return nil, fmt.Errorf("generator omitted supported glyph U+%04X", cp)
		}
	}
	if _, ok := glyphs['?']; !ok {
		if _, square := glyphs[0x25a1]; !square {
			return nil, fmt.Errorf("missing default '?' glyph")
		}
	}
	if rawPixels {
		img, err := decodeRGBA(data, doc.Atlas.Width, doc.Atlas.Height)
		if err != nil {
			return nil, err
		}
		return &Result{Image: img, Pixels: img.Pix, Glyphs: glyphs, Metadata: metadata}, nil
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode atlas PNG: %w", err)
	}
	w, h := decoded.Bounds().Dx(), decoded.Bounds().Dy()
	if w != doc.Atlas.Width || h != doc.Atlas.Height {
		return nil, fmt.Errorf("atlas PNG dimensions do not match metadata")
	}
	img, ok := decoded.(*image.NRGBA)
	if !ok {
		img = image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.Draw(img, img.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
	}
	return &Result{PNG: data, Image: img, Pixels: img.Pix, Glyphs: glyphs, Metadata: metadata}, nil
}

func decodeRGBA(data []byte, width, height int) (*image.NRGBA, error) {
	const headerSize = 12
	if len(data) < headerSize || string(data[:4]) != "RGBA" {
		return nil, fmt.Errorf("decode atlas RGBA: invalid or truncated header")
	}
	if _, err := mipByteCount(width, height); err != nil {
		return nil, fmt.Errorf("decode atlas RGBA: %w", err)
	}
	if uint64(binary.BigEndian.Uint32(data[4:8])) != uint64(width) || uint64(binary.BigEndian.Uint32(data[8:12])) != uint64(height) {
		return nil, fmt.Errorf("atlas RGBA dimensions do not match metadata")
	}
	pixels := data[headerSize:]
	expected := uint64(width) * uint64(height) * 4
	if uint64(len(pixels)) != expected {
		return nil, fmt.Errorf("atlas RGBA pixel byte count is %d, expected %d", len(pixels), expected)
	}
	// MSDFgen's RGBA writer emits top-down, straight RGBA8, just like PNG.
	// Retain the file buffer without a second atlas-sized allocation or conversion.
	return &image.NRGBA{Pix: pixels, Stride: 4 * width, Rect: image.Rect(0, 0, width, height)}, nil
}

type tailBuffer struct{ data []byte }

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	const limit = 1600
	if len(p) >= limit {
		b.data = append(b.data[:0], p[len(p)-limit:]...)
	} else {
		if len(b.data)+len(p) > limit {
			b.data = b.data[len(b.data)+len(p)-limit:]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func (b *tailBuffer) String() string { return strings.ToValidUTF8(string(b.data), "\uFFFD") }
