package mygoapp

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
)

func TestPreviewSamplesCoverScripts(t *testing.T) {
	five := []string{
		"为了超级地球！",
		"為了超級地球！",
		"スーパーアースのために！",
		"슈퍼지구를 위하여!",
		"For Super Earth!",
	}
	own := map[string]string{
		"de": "Für Über-Erde!", "en": "For Super Earth!",
		"es-419": "¡Por la Supertierra!", "es-ES": "¡Por la Supertierra!",
		"fr": "Pour la Super-Terre !", "it": "Per la Super Terra!",
		"ja": "スーパーアースのために！", "ko": "슈퍼지구를 위하여!",
		"pl": "Za Super Ziemię!", "pt-BR": "Pela Superterra!", "pt-PT": "Pela Superterra!",
		"ru": "За Супер-Землю!", "zh-CN": "为了超级地球！", "zh-TW": "為了超級地球！",
	}
	if len(own) != len(Locales) {
		t.Fatalf("slogan list covers %d locales, catalog has %d", len(own), len(Locales))
	}
	for _, loc := range Locales {
		got := Translate(loc.Code, "preview.sample")
		lines := []string{own[loc.Code]}
		for _, line := range five {
			if line != own[loc.Code] {
				lines = append(lines, line)
			}
		}
		lines = append(lines, "Player G4 - 0123456789")
		want := strings.Join(lines, "\n")
		if got != want {
			t.Fatalf("%s sample\n got %q\nwant %q", loc.Code, got, want)
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(got, "\n") {
			if seen[line] {
				t.Fatalf("%s repeats %q", loc.Code, line)
			}
			seen[line] = true
		}
		flat := flatSample(got)
		if strings.Contains(flat, "\n") || !strings.Contains(flat, " / ") || sampleText(flat) != got {
			t.Fatalf("%s one-line sample = %q", loc.Code, flat)
		}
	}
}

func TestPreviewSampleMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mygo-settings.json")
	for old := range previousPreviewSamples {
		savePreview(t, path, "en", old)
		a := openSettings(t, path, "")
		if a.settings.Locale != "en" || a.settings.PreviewText != Translate("en", "preview.sample") {
			t.Fatalf("old default %q stayed %q", old, a.settings.PreviewText)
		}
		if a.sampleDraft != flatSample(a.settings.PreviewText) {
			t.Fatal("editor did not follow the migrated sample")
		}
	}
	savePreview(t, path, "de", "Für Über-Erde!\nPlayer G4 - 0123456789")
	german := openSettings(t, path, "")
	if german.settings.PreviewText != Translate("de", "preview.sample") {
		t.Fatalf("german default = %q", german.settings.PreviewText)
	}
	savePreview(t, path, "zh-CN", "我的呼号")
	custom := openSettings(t, path, "")
	if custom.settings.PreviewText != "我的呼号" || custom.sampleDraft != "我的呼号" {
		t.Fatalf("custom sample changed: %q / %q", custom.settings.PreviewText, custom.sampleDraft)
	}
	savePreview(t, path, "ja", "")
	empty := openSettings(t, path, "")
	if empty.settings.PreviewText != Translate("ja", "preview.sample") {
		t.Fatal("empty sample was not filled")
	}
	fresh := Translate("en", "preview.sample")
	if got := normalizePreviewText("en", fresh); got != fresh {
		t.Fatal("the new default was treated as a legacy sample")
	}
}

func TestPreviewSampleFollowsLocale(t *testing.T) {
	a := testApp(t, nil)
	if a.settings.PreviewText != Translate("en", "preview.sample") {
		t.Fatalf("initial sample = %q", a.settings.PreviewText)
	}
	tt := ui.NewTester(a.view, 1140, 1000)
	if err := tt.Click(a.t("ui.language")); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Deutsch"); err != nil {
		t.Fatal(err)
	}
	if a.settings.Locale != "de" || a.settings.PreviewText != Translate("de", "preview.sample") || a.sampleDraft != flatSample(a.settings.PreviewText) {
		t.Fatalf("locale switch kept %q", a.settings.PreviewText)
	}
	a.settings.Locale = "en"
	a.localeName = "English"
	a.settings.PreviewText = "Custom squad"
	a.sampleDraft = "Custom squad"
	tt.Frame()
	if err := tt.Click(a.t("ui.language")); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Deutsch"); err != nil {
		t.Fatal(err)
	}
	if a.settings.PreviewText != "Custom squad" || a.sampleDraft != "Custom squad" {
		t.Fatalf("custom sample was replaced: %q", a.settings.PreviewText)
	}
}

func TestDefaultSampleFitsPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "regular.ttf")
	if err := os.WriteFile(path, goregular.TTF, 0600); err != nil {
		t.Fatal(err)
	}
	collection, err := sfnt.ParseCollection(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	f, err := collection.Font(0)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: 30, DPI: 96, Hinting: font.HintingFull})
	if err != nil {
		t.Fatal(err)
	}
	defer face.Close()
	ascent := face.Metrics().Ascent.Ceil()
	descent := face.Metrics().Descent.Ceil()
	lineHeight := face.Metrics().Height.Ceil() + 14
	const width, height = 1000, 480
	backend := FontPreviewBackend{}
	for _, loc := range Locales {
		text := Translate(loc.Code, "preview.sample")
		lines := strings.Split(text, "\n")
		baseline := 24 + ascent + (len(lines)-1)*lineHeight
		if baseline+descent > height-16 {
			t.Fatalf("%s last line does not fit in %dpx: baseline %d descent %d", loc.Code, height, baseline, descent)
		}
		result, err := backend.Preview(context.Background(), PreviewRequest{Font: FontSelection{Path: path}, Text: text, Width: width, Height: height})
		if err != nil {
			t.Fatal(err)
		}
		lowest := lowestPreviewInk(result.Image)
		if lowest < baseline-ascent || lowest > baseline+descent+1 {
			t.Fatalf("%s last line ink at y=%d, baseline %d ascent %d descent %d", loc.Code, lowest, baseline, ascent, descent)
		}
	}
}

func savePreview(t *testing.T, path, locale, text string) {
	t.Helper()
	s := DefaultSettings()
	s.Locale = locale
	s.PreviewText = text
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
}

func openSettings(t *testing.T, path, locale string) *application {
	t.Helper()
	a, err := newApplication(Options{SettingsPath: path, Locale: locale})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if a.previewCancel != nil {
			a.previewCancel()
		}
		close(a.closed)
	})
	return a
}

func lowestPreviewInk(img image.Image) int {
	bounds := img.Bounds()
	lowest := -1
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, _, _ := img.At(x, y).RGBA()
			if r > 40000 && g > 40000 {
				lowest = y
			}
		}
	}
	return lowest
}
