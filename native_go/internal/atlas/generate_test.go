package atlas

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestSourcePriorityAndRequiredKeys(t *testing.T) {
	sources := []Source{
		{FontPath: "prepared-custom.ttf", Charset: []rune{0, 'A', '?', 'A'}, Custom: true},
		{FontPath: "prepared-fallback.otf", Charset: []rune{'A', 'B', 0x570b}},
	}
	selected, missing, err := selectSources(sources, []rune{0, 'A', 'B', 0x9999})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || !reflect.DeepEqual(selected[0].Charset, []rune{'?', 'A'}) || !reflect.DeepEqual(selected[1].Charset, []rune{'B'}) || !reflect.DeepEqual(missing, []rune{0x9999}) {
		t.Fatalf("selected=%v missing=%v", selected, missing)
	}
	if len(sources[0].Charset) != 4 {
		t.Fatal("mutated sources")
	}
	all, missing, err := selectSources(sources, nil)
	if err != nil || len(missing) != 0 || !reflect.DeepEqual(all[1].Charset, []rune{'B', 0x570b}) {
		t.Fatal("nil keys did not use source union")
	}
	if _, _, err := selectSources(sources, []rune{-1}); err == nil {
		t.Fatal("accepted invalid rune")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Generate(ctx, sources, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestDecodeResult(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	copy(img.Pix, []byte{230, 40, 10, 0, 90, 80, 70, 127})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"atlas":{"type":"mtsdf","yOrigin":"top","size":32,"distanceRange":8,"width":2,"height":1},"variants":[{"glyphs":[{"unicode":63,"advance":0.5}]},{"glyphs":[{"unicode":65,"advance":1}]}]}`)
	result, err := decodeResult(encoded.Bytes(), metadata, map[rune]bool{'?': true, 'A': true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.Pixels, img.Pix) || len(result.Glyphs) != 2 {
		t.Fatal("lost straight PNG channels or variant glyphs")
	}
	for name, bad := range map[string][]byte{
		"wrong type":       bytes.Replace(metadata, []byte("mtsdf"), []byte("msdf"), 1),
		"wrong origin":     bytes.Replace(metadata, []byte("top"), []byte("bottom"), 1),
		"wrong size":       bytes.Replace(metadata, []byte(`"size":32`), []byte(`"size":16`), 1),
		"wrong range":      bytes.Replace(metadata, []byte(`"distanceRange":8`), []byte(`"distanceRange":4`), 1),
		"wrong dimensions": bytes.Replace(metadata, []byte(`"width":2`), []byte(`"width":3`), 1),
		"no default":       bytes.Replace(metadata, []byte(`"unicode":63`), []byte(`"unicode":66`), 1),
		"bad JSON":         []byte("{"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeResult(encoded.Bytes(), bad, nil); err == nil {
				t.Fatal("accepted invalid output")
			}
		})
	}
	if _, err := decodeResult(encoded.Bytes(), metadata, map[rune]bool{'B': true}); err == nil {
		t.Fatal("accepted missing supported glyph")
	}
	if _, err := decodeResult([]byte("not PNG"), metadata, nil); err == nil {
		t.Fatal("accepted corrupt PNG")
	}
}

func TestNativeGenerate(t *testing.T) {
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
	root := t.TempDir()
	customPath := filepath.Join(root, "prepared custom.ttf")
	fallbackPath := filepath.Join(root, "prepared fallback.ttf")
	if err := os.WriteFile(customPath, syntheticTTF([]rune{'?', 'A'}, 600), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fallbackPath, syntheticTTF([]rune{'A', 'B'}, 800), 0600); err != nil {
		t.Fatal(err)
	}
	sources := []Source{{FontPath: customPath, Charset: []rune{'?', 'A'}, Name: "Synthetic Custom", Custom: true}, {FontPath: fallbackPath, Charset: []rune{'A', 'B'}, Name: "Synthetic Fallback"}}
	work := filepath.Join(root, "work")
	opts := Options{Executable: helper, WorkDir: work, Threads: 1, Keys: []rune{0, 'A', 'B', 0x9999}}
	progressCalls := 0
	opts.Progress = func(s string) {
		if s == "" {
			t.Error("empty progress")
		}
		progressCalls++
	}
	a, err := Generate(context.Background(), sources, opts)
	if err != nil {
		t.Fatal(err)
	}
	if progressCalls != 1 {
		t.Errorf("progress callbacks=%d", progressCalls)
	}
	if len(a.Glyphs) != 3 || len(a.PNG) == 0 || len(a.Pixels) == 0 {
		t.Fatal("missing generation output")
	}
	if _, ok := a.Glyphs[0]; ok {
		t.Fatal("sentinel sent to generator")
	}
	if math.Abs(a.Glyphs['A'].Advance-0.6) > 1e-9 || math.Abs(a.Glyphs['B'].Advance-0.8) > 1e-9 {
		t.Fatalf("wrong font selection: %v", a.Glyphs)
	}
	if !reflect.DeepEqual(a.Report.DefaultGlyphCharacters, []string{"U+9999"}) || len(a.Report.Sources) != 2 || a.Report.Sources[1].Characters != 1 {
		t.Fatalf("report=%+v", a.Report)
	}
	for _, size := range []int{a.Image.Rect.Dx(), a.Image.Rect.Dy()} {
		if size < 1 || size&(size-1) != 0 {
			t.Fatal("atlas dimensions are not powers of two")
		}
	}
	var metadata map[string]any
	if err := json.Unmarshal(a.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	checkNoKerning(t, metadata)
	entries, err := os.ReadDir(work)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary atlas files retained: %v %v", entries, err)
	}
	opts.Threads = 2
	b, err := Generate(context.Background(), sources, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Pixels, b.Pixels) || !reflect.DeepEqual(a.Glyphs, b.Glyphs) {
		t.Fatal("thread count changed output")
	}
	// A single source uses the generator's non-variant JSON shape.
	opts.Keys = []rune{'A'}
	if single, err := Generate(context.Background(), sources[:1], opts); err != nil || len(single.Glyphs) != 2 {
		t.Fatalf("single source: %v", err)
	}
	// Claiming a glyph that the prepared font lacks must not silently substitute.
	badSources := []Source{{FontPath: customPath, Charset: []rune{'?', 'X'}}}
	opts.Keys = []rune{'X'}
	if _, err := Generate(context.Background(), badSources, opts); err == nil {
		t.Fatal("accepted unsupported declared glyph")
	}
	badSources[0].FontPath = filepath.Join(root, "missing.ttf")
	if _, err := Generate(context.Background(), badSources, opts); err == nil {
		t.Fatal("accepted failed native process")
	}
}

func checkNoKerning(t *testing.T, v any) {
	t.Helper()
	switch node := v.(type) {
	case map[string]any:
		for key, child := range node {
			if key == "kerning" {
				if entries, ok := child.([]any); ok && len(entries) > 0 {
					t.Fatal("generated kerning pairs")
				}
			}
			checkNoKerning(t, child)
		}
	case []any:
		for _, child := range node {
			checkNoKerning(t, child)
		}
	}
}

func TestTailBuffer(t *testing.T) {
	var b tailBuffer
	if n, err := b.Write(bytes.Repeat([]byte{'a'}, 2000)); n != 2000 || err != nil {
		t.Fatal("incorrect write contract")
	}
	b.Write([]byte("last"))
	if len(b.data) != 1600 || !strings.HasSuffix(b.String(), "last") {
		t.Fatal("incorrect diagnostic tail")
	}
}

// syntheticTTF constructs rectangular glyphs entirely in Go. This fixture never
// reads an installed, personal, or shipped font and needs no Python tooling.
func syntheticTTF(chars []rune, advance uint16) []byte {
	chars = slices.Clone(chars)
	slices.Sort(chars)
	chars = slices.Compact(chars)
	n := len(chars) + 1
	u16 := func(b []byte, off int, v int) { binary.BigEndian.PutUint16(b[off:], uint16(v)) }
	u32 := func(b []byte, off int, v int) { binary.BigEndian.PutUint32(b[off:], uint32(v)) }
	head := make([]byte, 54)
	u32(head, 0, 0x00010000)
	u32(head, 4, 0x00010000)
	u32(head, 12, 0x5f0f3cf5)
	u16(head, 18, 1000)
	u16(head, 40, int(advance)-100)
	u16(head, 42, 700)
	u16(head, 46, 8)
	u16(head, 48, 2)
	u16(head, 50, 1)
	hhea := make([]byte, 36)
	u32(hhea, 0, 0x00010000)
	u16(hhea, 4, 800)
	u16(hhea, 6, -200)
	u16(hhea, 10, int(advance))
	u16(hhea, 16, int(advance)-100)
	u16(hhea, 18, 1)
	u16(hhea, 34, n)
	maxp := make([]byte, 32)
	u32(maxp, 0, 0x00010000)
	u16(maxp, 4, n)
	u16(maxp, 6, 4)
	u16(maxp, 8, 1)
	u16(maxp, 14, 1)
	hmtx, loca, glyf := make([]byte, 4*n), make([]byte, 4*(n+1)), make([]byte, 36*n)
	for i := 0; i < n; i++ {
		u16(hmtx, i*4, int(advance))
		u32(loca, i*4, 36*i)
		g := glyf[i*36:]
		u16(g, 0, 1)
		u16(g, 6, int(advance)-100)
		u16(g, 8, 700)
		u16(g, 10, 3)
		copy(g[14:18], []byte{1, 1, 1, 1})
		for j, d := range []int{0, 0, int(advance) - 100, 0, 0, 700, 0, -700} {
			u16(g, 18+2*j, d)
		}
	}
	u32(loca, n*4, 36*n)
	cmap := make([]byte, 12+16+12*len(chars))
	u16(cmap, 2, 1)
	u16(cmap, 4, 3)
	u16(cmap, 6, 10)
	u32(cmap, 8, 12)
	u16(cmap, 12, 12)
	u32(cmap, 16, len(cmap)-12)
	u32(cmap, 24, len(chars))
	for i, cp := range chars {
		u32(cmap, 28+12*i, int(cp))
		u32(cmap, 32+12*i, int(cp))
		u32(cmap, 36+12*i, i+1)
	}
	name := make([]byte, 6)
	u16(name, 4, 6)
	post := make([]byte, 32)
	u32(post, 0, 0x00030000)
	// A real kerning pair makes the no-kerning assertion meaningful.
	kern := make([]byte, 24)
	for off, value := range map[int]int{2: 1, 6: 20, 8: 1, 10: 1, 12: 6, 18: 1, 20: 2, 22: -50} {
		u16(kern, off, value)
	}
	tables := map[string][]byte{"head": head, "hhea": hhea, "maxp": maxp, "hmtx": hmtx, "loca": loca, "glyf": glyf, "cmap": cmap, "name": name, "post": post, "kern": kern}
	tags := make([]string, 0, len(tables))
	for tag := range tables {
		tags = append(tags, tag)
	}
	slices.Sort(tags)
	out := make([]byte, 12+16*len(tags))
	u32(out, 0, 0x00010000)
	u16(out, 4, len(tags))
	u16(out, 6, 128)
	u16(out, 8, 3)
	u16(out, 10, 16*len(tags)-128)
	headOffset := 0
	for i, tag := range tags {
		data := tables[tag]
		padding := (4 - len(data)%4) % 4
		padded := append(bytes.Clone(data), make([]byte, padding)...)
		offset := len(out)
		copy(out[12+16*i:], tag)
		u32(out, 16+16*i, int(fontChecksum(padded)))
		u32(out, 20+16*i, offset)
		u32(out, 24+16*i, len(data))
		out = append(out, padded...)
		if tag == "head" {
			headOffset = offset
		}
	}
	binary.BigEndian.PutUint32(out[headOffset+8:], 0xb1b0afba-fontChecksum(out))
	return out
}

func fontChecksum(b []byte) uint32 {
	var sum uint32
	for i := 0; i+4 <= len(b); i += 4 {
		sum += binary.BigEndian.Uint32(b[i:])
	}
	return sum
}
