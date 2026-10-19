package atlas

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func rawMetadata(width, height int, glyphs string) []byte {
	return []byte(fmt.Sprintf(`{"atlas":{"type":"mtsdf","yOrigin":"top","size":32,"distanceRange":8,"width":%d,"height":%d},%s}`, width, height, glyphs))
}

func rgbaFixture(img *image.NRGBA) []byte {
	data := make([]byte, 12+len(img.Pix))
	copy(data, "RGBA")
	binary.BigEndian.PutUint32(data[4:8], uint32(img.Rect.Dx()))
	binary.BigEndian.PutUint32(data[8:12], uint32(img.Rect.Dy()))
	copy(data[12:], img.Pix)
	return data
}

func assertRawParity(t *testing.T, pngResult, rawResult *Result) {
	t.Helper()
	if len(pngResult.PNG) == 0 || rawResult.PNG != nil {
		t.Fatal("PNG output must be retained by default and absent for raw transport")
	}
	if pngResult.Image.Rect != rawResult.Image.Rect || pngResult.Image.Stride != rawResult.Image.Stride ||
		!bytes.Equal(pngResult.Pixels, rawResult.Pixels) || !bytes.Equal(rawResult.Image.Pix, rawResult.Pixels) {
		t.Fatal("PNG/raw base RGBA bytes, orientation, or dimensions differ")
	}
	if !bytes.Equal(pngResult.Metadata, rawResult.Metadata) || !reflect.DeepEqual(pngResult.Glyphs, rawResult.Glyphs) ||
		!reflect.DeepEqual(pngResult.Report, rawResult.Report) {
		t.Fatal("transport changed glyphs, metadata, or source report")
	}
	pngMips, err := TexturePixels(pngResult.Image)
	if err != nil {
		t.Fatal(err)
	}
	rawMips, err := TexturePixels(rawResult.Image)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pngMips, rawMips) {
		t.Fatal("PNG/raw mip bytes differ")
	}
	width, height := rawResult.Image.Rect.Dx(), rawResult.Image.Rect.Dy()
	expected := 0
	for {
		expected += width * height * 4
		if width == 1 && height == 1 {
			break
		}
		width, height = max(1, width/2), max(1, height/2)
	}
	if len(rawMips) != expected {
		t.Fatalf("mip chain has %d bytes, want %d through 1x1", len(rawMips), expected)
	}
}

func TestDecodeRawResultParity(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	copy(img.Pix, []byte{
		230, 40, 10, 0, 90, 80, 70, 127, 255, 4, 200, 1, 3, 2, 1, 255,
		7, 130, 240, 254, 250, 17, 99, 128, 31, 211, 67, 0, 9, 18, 27, 63,
	})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	for name, glyphs := range map[string]string{
		"single":   `"glyphs":[{"unicode":63,"advance":0.5},{"unicode":65,"advance":1}]`,
		"variants": `"variants":[{"glyphs":[{"unicode":63,"advance":0.5}]},{"glyphs":[{"unicode":65,"advance":1}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			metadata := rawMetadata(4, 2, glyphs)
			expected := map[rune]bool{'?': true, 'A': true}
			pngResult, err := decodeResult(encoded.Bytes(), metadata, expected)
			if err != nil {
				t.Fatal(err)
			}
			data := rgbaFixture(img)
			rawResult, err := decodeRawResult(data, metadata, expected)
			if err != nil {
				t.Fatal(err)
			}
			assertRawParity(t, pngResult, rawResult)
			if !bytes.Equal(rawResult.Pixels, img.Pix) {
				t.Fatal("lost straight channels, including RGB under zero alpha, or flipped rows")
			}
			if &rawResult.Pixels[0] != &data[12] || &rawResult.Image.Pix[0] != &data[12] {
				t.Fatal("raw decoding copied the pixel buffer")
			}
		})
	}
}

func TestDecodeRawResultErrors(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	data := rgbaFixture(img)
	glyphs := `"variants":[{"glyphs":[{"unicode":63,"advance":0.5}]},{"glyphs":[{"unicode":65,"advance":1}]}]`
	metadata := rawMetadata(4, 2, glyphs)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string][]byte{
		"wrong type":        bytes.Replace(metadata, []byte("mtsdf"), []byte("msdf"), 1),
		"wrong origin":      bytes.Replace(metadata, []byte("top"), []byte("bottom"), 1),
		"wrong size":        bytes.Replace(metadata, []byte(`"size":32`), []byte(`"size":16`), 1),
		"wrong range":       bytes.Replace(metadata, []byte(`"distanceRange":8`), []byte(`"distanceRange":4`), 1),
		"missing default":   bytes.Replace(metadata, []byte(`"unicode":63`), []byte(`"unicode":66`), 1),
		"invalid Unicode":   bytes.Replace(metadata, []byte(`"unicode":65`), []byte(`"unicode":55296`), 1),
		"invalid metrics":   bytes.Replace(metadata, []byte(`"advance":1`), []byte(`"advance":1e40`), 1),
		"no plane bounds":   bytes.Replace(metadata, []byte(`"advance":1`), []byte(`"advance":1,"atlasBounds":{}`), 1),
		"missing supported": bytes.Replace(metadata, []byte(`"unicode":65`), []byte(`"unicode":66`), 1),
		"bad JSON":          []byte("{"),
	} {
		t.Run(name, func(t *testing.T) {
			expected := map[rune]bool{'A': true}
			_, pngErr := decodeResult(encoded.Bytes(), bad, expected)
			result, rawErr := decodeRawResult(data, bad, expected)
			if pngErr == nil || rawErr == nil || pngErr.Error() != rawErr.Error() || result != nil {
				t.Fatalf("metadata validation differs: PNG=%v raw=%v", pngErr, rawErr)
			}
		})
	}
	for _, tc := range []struct {
		name string
		data []byte
		meta []byte
		want string
	}{
		{"empty", nil, metadata, "header"},
		{"short header", data[:11], metadata, "header"},
		{"bad magic", append([]byte("ARGB"), data[4:]...), metadata, "header"},
		{"header only", data[:12], metadata, "pixel byte count is 0, expected 32"},
		{"truncated pixels", data[:len(data)-1], metadata, "pixel byte count is 31, expected 32"},
		{"extra pixels", append(bytes.Clone(data), 0), metadata, "pixel byte count is 33, expected 32"},
		{"wrong width", data, rawMetadata(8, 2, glyphs), "dimensions do not match metadata"},
		{"wrong height", data, rawMetadata(4, 4, glyphs), "dimensions do not match metadata"},
		{"transposed dimensions", data, rawMetadata(2, 4, glyphs), "dimensions do not match metadata"},
		{"zero width", data, rawMetadata(0, 2, glyphs), "invalid texture dimensions"},
		{"negative height", data, rawMetadata(4, -1, glyphs), "invalid texture dimensions"},
		{"dimension overflow", data, rawMetadata(math.MaxInt, 2, glyphs), "invalid texture dimensions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := decodeRawResult(tc.data, tc.meta, nil)
			if result != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
	if strconv.IntSize == 64 {
		largeHeight := uint64(math.MaxUint32)
		_, err := decodeRawResult(data, rawMetadata(math.MaxUint32/4, int(largeHeight), glyphs), nil)
		if err == nil || !strings.Contains(err.Error(), "overflow mip storage") {
			t.Fatalf("storage overflow: %v", err)
		}
	}
	// Keep the same square-glyph default fallback accepted by the PNG path.
	square := bytes.Replace(metadata, []byte(`"unicode":63`), []byte(`"unicode":9633`), 1)
	if _, err := decodeRawResult(data, square, map[rune]bool{0x25a1: true, 'A': true}); err != nil {
		t.Fatal(err)
	}
}

func rawNativeHelper(t testing.TB) string {
	t.Helper()
	helper := os.Getenv("HD2_MSDF_ATLAS_GEN")
	if helper == "" {
		if runtime.GOOS != "windows" {
			t.Skip("bundled generator is Windows-only; set HD2_MSDF_ATLAS_GEN to a native build")
		}
		helper = filepath.Join("..", "..", "..", "vendor", "msdf-atlas-gen", "msdf-atlas-gen.exe")
	}
	helper, err := filepath.Abs(helper)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(helper); err != nil {
		t.Fatal(err)
	}
	return helper
}

func TestNativeRawGenerateParity(t *testing.T) {
	helper := rawNativeHelper(t)
	root := t.TempDir()
	customPath, fallbackPath := filepath.Join(root, "custom font.ttf"), filepath.Join(root, "fallback font.ttf")
	if err := os.WriteFile(customPath, syntheticTTF([]rune{'?', 'A', 0x4e2d}, 600), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fallbackPath, syntheticTTF([]rune{'A', 'B', 0x20000}, 800), 0600); err != nil {
		t.Fatal(err)
	}
	sources := []Source{
		{FontPath: customPath, Charset: []rune{0, '?', 'A', 0x4e2d}, Name: "Custom", Custom: true},
		{FontPath: fallbackPath, Charset: []rune{'A', 'B', 0x20000}, Name: "Fallback"},
	}
	for _, tc := range []struct {
		name    string
		sources []Source
		keys    []rune
		count   int
	}{
		{"multi-source full charset", sources, nil, 5},
		{"multi-source selected", sources, []rune{0, 'A', 'B', 0x4e2d, 0x20000, 0x9999}, 5},
		{"single source", sources[:1], nil, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{Executable: helper, WorkDir: filepath.Join(t.TempDir(), "work"), Threads: 1, Keys: tc.keys}
			pngResult, err := Generate(context.Background(), tc.sources, opts)
			if err != nil {
				t.Fatal(err)
			}
			opts.RawPixels = true
			rawResult, err := Generate(context.Background(), tc.sources, opts)
			if err != nil {
				t.Fatal(err)
			}
			assertRawParity(t, pngResult, rawResult)
			if len(rawResult.Glyphs) != tc.count || math.Abs(rawResult.Glyphs['A'].Advance-0.6) > 1e-9 {
				t.Fatal("lost charset coverage or custom-first source precedence")
			}
			if _, ok := rawResult.Glyphs[0]; ok {
				t.Fatal("sentinel sent to generator")
			}
			if len(tc.sources) > 1 && math.Abs(rawResult.Glyphs['B'].Advance-0.8) > 1e-9 {
				t.Fatal("lost fallback source")
			}
			if tc.keys != nil && !reflect.DeepEqual(rawResult.Report.DefaultGlyphCharacters, []string{"U+9999"}) {
				t.Fatal("lost missing-glyph report")
			}
			partialAlpha := false
			for i := 3; i < len(rawResult.Pixels); i += 4 {
				partialAlpha = partialAlpha || rawResult.Pixels[i] > 0 && rawResult.Pixels[i] < 255
			}
			if !partialAlpha {
				t.Fatal("fixture did not exercise MTSDF alpha distance values")
			}
			entries, err := os.ReadDir(opts.WorkDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary atlas files retained: %v %v", entries, err)
			}
		})
	}
	t.Run("omitted supported glyph", func(t *testing.T) {
		_, err := Generate(context.Background(), []Source{{FontPath: customPath, Charset: []rune{'?', 'X'}}}, Options{
			Executable: helper, WorkDir: t.TempDir(), Threads: 1, RawPixels: true,
		})
		if err == nil || !strings.Contains(err.Error(), "omitted supported glyph U+0058") {
			t.Fatalf("got %v, want missing supported glyph error", err)
		}
	})
}

func TestNativeRawRealFontParity(t *testing.T) {
	font := os.Getenv("HD2_ATLAS_PARITY_FONT")
	if font == "" {
		t.Skip("set HD2_ATLAS_PARITY_FONT to a prepared font for optional real-font parity")
	}
	chars := []rune("?AagjpQW09@&")
	if sample := os.Getenv("HD2_ATLAS_PARITY_CHARS"); sample != "" {
		chars = []rune("?" + sample)
	}
	sources := []Source{{FontPath: font, Charset: chars}}
	opts := Options{Executable: rawNativeHelper(t), WorkDir: t.TempDir(), Threads: 1}
	pngResult, err := Generate(context.Background(), sources, opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.RawPixels = true
	rawResult, err := Generate(context.Background(), sources, opts)
	if err != nil {
		t.Fatal(err)
	}
	assertRawParity(t, pngResult, rawResult)
}
