package atlas

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNativeGenerateUnicodePathsAndMultiSourceParity(t *testing.T) {
	helperSource := rawNativeHelper(t)
	root := t.TempDir()
	unicodeRoot := filepath.Join(root, "下载 目录 中文")
	if err := os.MkdirAll(unicodeRoot, 0755); err != nil {
		t.Fatal(err)
	}
	helperPath := filepath.Join(unicodeRoot, "原生 工具", "msdf atlas 生成器.exe")
	copyAtlasTestFile(t, helperSource, helperPath)
	workUnicode := filepath.Join(unicodeRoot, "工作 目录")

	unicodeFontDir := filepath.Join(unicodeRoot, "准备 字体")
	unicodeCustomPath := filepath.Join(unicodeFontDir, "自定义 字体.ttf")
	unicodeFallbackPath := filepath.Join(unicodeFontDir, "后备 字体.ttf")
	asciiRoot := filepath.Join(root, "ascii baseline")
	asciiFontDir := filepath.Join(asciiRoot, "prepared fonts")
	asciiCustomPath := filepath.Join(asciiFontDir, "custom.ttf")
	asciiFallbackPath := filepath.Join(asciiFontDir, "fallback.ttf")
	workASCII := filepath.Join(asciiRoot, "work dir")

	customChars := []rune{'?', 'A', 0x4e2d}
	fallbackChars := []rune{'A', 'B', 0x570b}
	writeAtlasSyntheticFont(t, unicodeCustomPath, customChars, 600)
	writeAtlasSyntheticFont(t, unicodeFallbackPath, fallbackChars, 800)
	writeAtlasSyntheticFont(t, asciiCustomPath, customChars, 600)
	writeAtlasSyntheticFont(t, asciiFallbackPath, fallbackChars, 800)

	const customName = "自定义 优先源"
	unicodeSources := []Source{
		{FontPath: unicodeCustomPath, Charset: customChars, Name: customName, Custom: true},
		{FontPath: unicodeFallbackPath, Charset: fallbackChars},
	}
	asciiSources := []Source{
		{FontPath: asciiCustomPath, Charset: customChars, Name: customName, Custom: true},
		{FontPath: asciiFallbackPath, Charset: fallbackChars},
	}
	wantAdvances := map[rune]float64{'?': 0.6, 'A': 0.6, 0x4e2d: 0.6, 'B': 0.8, 0x570b: 0.8}
	wantUnicodeReport := Report{
		Sources: []SourceReport{
			{Font: customName, Characters: len(customChars), Custom: true},
			// A is claimed by the custom source, leaving B and 国 for the fallback.
			{Font: filepath.Base(unicodeFallbackPath), Characters: 2},
		},
		DefaultGlyphCharacters: []string{},
		Threads:                1,
	}

	for _, tc := range []struct {
		name string
		raw  bool
	}{
		{name: "png", raw: false},
		{name: "raw pixels", raw: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unicodeResult, err := Generate(context.Background(), unicodeSources, Options{
				Executable: helperPath,
				WorkDir:    workUnicode,
				Threads:    1,
				RawPixels:  tc.raw,
			})
			if err != nil {
				t.Fatalf("Unicode Paths generation failed: %v", err)
			}
			assertAtlasTestWorkClean(t, workUnicode)
			if tc.raw && unicodeResult.PNG != nil {
				t.Fatal("raw transport retained PNG bytes")
			}
			if !tc.raw && len(unicodeResult.PNG) == 0 {
				t.Fatal("PNG transport omitted PNG bytes")
			}

			baseline, err := Generate(context.Background(), asciiSources, Options{
				Executable: helperSource,
				WorkDir:    workASCII,
				Threads:    1,
				RawPixels:  tc.raw,
			})
			if err != nil {
				t.Fatalf("ASCII baseline generation failed: %v", err)
			}
			assertAtlasTestWorkClean(t, workASCII)

			if unicodeResult.Image.Rect != baseline.Image.Rect || unicodeResult.Image.Stride != baseline.Image.Stride {
				t.Fatalf("image geometry differs: Unicode=%v/%d ASCII=%v/%d",
					unicodeResult.Image.Rect, unicodeResult.Image.Stride, baseline.Image.Rect, baseline.Image.Stride)
			}
			if len(unicodeResult.Pixels) == 0 || !bytes.Equal(unicodeResult.Pixels, baseline.Pixels) {
				t.Fatal("Unicode paths changed base RGBA pixels relative to the ASCII baseline")
			}
			if !bytes.Equal(unicodeResult.Metadata, baseline.Metadata) {
				t.Fatal("Unicode paths changed generated metadata relative to the ASCII baseline")
			}
			if len(unicodeResult.Glyphs) != len(wantAdvances) {
				t.Fatalf("Unicode generation has %d glyphs, want %d", len(unicodeResult.Glyphs), len(wantAdvances))
			}
			for cp, want := range wantAdvances {
				unicodeGlyph, ok := unicodeResult.Glyphs[cp]
				if !ok {
					t.Fatalf("Unicode generation omitted U+%04X", cp)
				}
				baselineGlyph, ok := baseline.Glyphs[cp]
				if !ok {
					t.Fatalf("ASCII baseline omitted U+%04X", cp)
				}
				if math.Abs(unicodeGlyph.Advance-baselineGlyph.Advance) > 1e-9 {
					t.Fatalf("U+%04X advance differs: Unicode=%g ASCII=%g", cp, unicodeGlyph.Advance, baselineGlyph.Advance)
				}
				if math.Abs(unicodeGlyph.Advance-want) > 1e-9 {
					t.Fatalf("U+%04X advance=%g, want %g from source priority", cp, unicodeGlyph.Advance, want)
				}
			}
			if !reflect.DeepEqual(unicodeResult.Glyphs, baseline.Glyphs) {
				t.Fatal("Unicode paths changed glyph bounds or advances relative to the ASCII baseline")
			}
			if !reflect.DeepEqual(unicodeResult.Report, wantUnicodeReport) {
				t.Fatalf("report does not preserve explicit source names and original fallback basename: %+v", unicodeResult.Report)
			}
			if unicodeResult.Report.Sources[0].Font == filepath.Base(unicodeCustomPath) {
				t.Fatal("report used the font basename instead of the higher-priority explicit source name")
			}
		})
	}
}

func copyAtlasTestFile(t testing.TB, source, destination string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0755); err != nil {
		t.Fatal(err)
	}
}

func writeAtlasSyntheticFont(t testing.TB, path string, chars []rune, advance uint16) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, syntheticTTF(chars, advance), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertAtlasTestWorkClean(t testing.TB, work string) {
	t.Helper()
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, entry := range entries {
			names[i] = entry.Name()
		}
		t.Fatalf("temporary atlas files retained in %q: %v", work, names)
	}
}
