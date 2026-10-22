package mygoapp

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/font/gofont/goregular"
)

func testApp(t *testing.T, backend Backend) *application {
	t.Helper()
	a, err := newApplication(Options{Backend: backend, SettingsPath: filepath.Join(t.TempDir(), "settings.json"), Locale: "en"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if a.cancel != nil {
			a.cancel()
		}
		if a.previewCancel != nil {
			a.previewCancel()
		}
		close(a.closed)
	})
	return a
}

func awaitState(t *testing.T, a *application, done func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !done() {
		select {
		case fn := <-a.events:
			fn()
		case <-deadline:
			t.Fatal("backend did not complete")
		}
	}
}

func TestCatalogsAndSettings(t *testing.T) {
	if len(catalogs) != 14 {
		t.Fatalf("catalog count: %d", len(catalogs))
	}
	for _, locale := range Locales {
		for key := range catalogs["en"] {
			if Translate(locale.Code, key) == "" || catalogs[locale.Code][key] == "" {
				t.Errorf("%s lacks %s", locale.Code, key)
			}
		}
		path := filepath.Join(t.TempDir(), "settings.json")
		s := DefaultSettings()
		s.Locale = locale.Code
		s.Languages = []string{"sc", "tc"}
		s.MatchWeights = false
		s.ProcessOutlines = true
		s.Font = FontSelection{Path: "example.ttc", FaceIndex: 2}
		if err := SaveSettings(path, s); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadSettings(path)
		if err != nil || !reflect.DeepEqual(s, loaded) {
			t.Fatalf("settings not preserved: %+v %v", loaded, err)
		}
	}
	for input, want := range map[string]string{"zh_Hant_HK": "zh-TW", "es_MX": "es-419", "pt_BR": "pt-BR", "de_DE": "de", "unknown": "en"} {
		if got := ResolveLocale(input); got != want {
			t.Errorf("%s => %s, want %s", input, got, want)
		}
	}
}

func TestFontInspectionAndPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readable.ttf")
	if err := os.WriteFile(path, goregular.TTF, 0600); err != nil {
		t.Fatal(err)
	}
	a := testApp(t, nil)
	a.settings.PreviewText = "For Super Earth!\nPlayer G4 - 0123456789"
	a.inspectFont(path)
	awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
	if a.errorKey != "" || len(a.faces) != 1 || a.preview == nil {
		t.Fatalf("font/preview: %s %s, %d faces", a.errorKey, a.errorDetail, len(a.faces))
	}
	b := FontPreviewBackend{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.InspectFont(ctx, path); !errors.Is(err, context.Canceled) {
		t.Errorf("font cancellation: %v", err)
	}
	if _, err := b.Generate(context.Background(), BuildRequest{}, nil); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("standalone generation must be unavailable: %v", err)
	}
	result, err := b.Preview(context.Background(), PreviewRequest{Font: a.settings.Font, Text: "Earth 0123456789", Width: 700, Height: 240})
	if err != nil {
		t.Fatal(err)
	}
	ink := 0
	for y := 0; y < 240; y++ {
		for x := 0; x < 700; x++ {
			r, g, bb, _ := result.Image.At(x, y).RGBA()
			if r < 20000 && g < 20000 && bb < 20000 {
				ink++
			}
		}
	}
	if ink < 500 {
		t.Fatalf("font preview is blank: %d ink pixels", ink)
	}
}

type relocatedFontBackend struct {
	FontPreviewBackend
	path string
}

func (b relocatedFontBackend) InspectFont(ctx context.Context, _ string) (FontInfo, error) {
	return b.FontPreviewBackend.InspectFont(ctx, b.path)
}

func TestModelUsesImportedFontPath(t *testing.T) {
	stored := filepath.Join(t.TempDir(), "imported.ttf")
	if err := os.WriteFile(stored, goregular.TTF, 0600); err != nil {
		t.Fatal(err)
	}
	a := testApp(t, relocatedFontBackend{path: stored})
	a.inspectFont("original.ttf")
	awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
	if a.settings.Font.Path != stored || a.fontDraft != stored || a.preview == nil {
		t.Fatalf("imported path was ignored: %+v", a.settings.Font)
	}
	a.save()
	loaded, err := LoadSettings(a.settingsPath)
	if err != nil || loaded.Font.Path != stored {
		t.Fatalf("imported path not persisted: %v", err)
	}
}

type recordingBackend struct {
	FontPreviewBackend
	requests chan BuildRequest
	release  chan struct{}
	failure  error
}

func (b *recordingBackend) Generate(ctx context.Context, req BuildRequest, progress func(Progress)) (BuildResult, error) {
	b.requests <- req
	progress(Progress{Stage: "weights", Completed: 2, Total: 3, Fraction: 0.4})
	if b.release != nil {
		select {
		case <-ctx.Done():
			return BuildResult{}, ctx.Err()
		case <-b.release:
		}
	}
	if b.failure != nil {
		return BuildResult{}, b.failure
	}
	if err := os.WriteFile(req.OutputZIP, []byte("test output"), 0600); err != nil {
		return BuildResult{}, err
	}
	return BuildResult{OutputZIP: req.OutputZIP}, nil
}

func readyApp(t *testing.T, b Backend) *application {
	a := testApp(t, b)
	a.settings.Font = FontSelection{Path: "selected.ttc", FaceIndex: 2}
	a.fontDraft = a.settings.Font.Path
	a.faces = []FontFace{{Index: 2, Name: "Collection Face"}}
	a.settings.GameFolder = filepath.Join(t.TempDir(), "game")
	a.settings.OutputZIP = filepath.Join(t.TempDir(), "result.zip")
	a.settings.Languages = []string{"sc", "tc"}
	return a
}

func TestGenerationContractCancelAndFailure(t *testing.T) {
	b := &recordingBackend{requests: make(chan BuildRequest, 1), release: make(chan struct{})}
	a := readyApp(t, b)
	a.settings.MatchWeights, a.settings.ProcessOutlines = false, true
	a.settings.LastOutput = "previous.zip"
	a.generate()
	req := <-b.requests
	if !slices.Equal(req.Languages, []string{"sc", "tc"}) || req.MatchWeights || !req.ProcessOutlines || req.Font.FaceIndex != 2 {
		t.Fatalf("wrong request: %+v", req)
	}
	a.drain()
	if a.progress.Stage != "weights" {
		t.Fatalf("progress not forwarded: %+v", a.progress)
	}
	a.cancelWork()
	awaitState(t, a, func() bool { return !a.busy })
	if a.statusKey != "native.cancelled" || a.settings.LastOutput != "previous.zip" {
		t.Fatalf("cancellation: %s %s", a.statusKey, a.settings.LastOutput)
	}
	b.release = nil
	b.failure = errors.New("test backend failure")
	a.generate()
	<-b.requests
	awaitState(t, a, func() bool { return !a.busy })
	if a.errorDetail != "test backend failure" || a.settings.LastOutput != "previous.zip" {
		t.Fatal("failure discarded prior output or detail")
	}
	b.failure = nil
	a.generate()
	<-b.requests
	awaitState(t, a, func() bool { return !a.busy })
	if a.statusKey != "status.complete" || a.settings.LastOutput != a.settings.OutputZIP {
		t.Fatal("success not recorded")
	}
}

func TestRequestValidation(t *testing.T) {
	a := readyApp(t, &recordingBackend{requests: make(chan BuildRequest, 1)})
	good := a.settings.BuildRequest
	if key := ValidateRequest(good); key != "" {
		t.Fatalf("valid mixed request rejected: %s", key)
	}
	bad := good
	bad.OutputZIP = filepath.Join(bad.GameFolder, "forbidden.zip")
	if key := ValidateRequest(bad); key != "error.output_location" {
		t.Errorf("game directory output: %s", key)
	}
	bad = good
	bad.Languages = nil
	if key := ValidateRequest(bad); key != "error.no_language" {
		t.Errorf("empty languages: %s", key)
	}
	bad = good
	bad.OutputZIP = "output.ttf"
	if key := ValidateRequest(bad); key != "error.output_zip" {
		t.Errorf("bad extension: %s", key)
	}
}

func TestNativeViewInteractions(t *testing.T) {
	a := readyApp(t, &recordingBackend{requests: make(chan BuildRequest, 1)})
	tt := ui.NewTester(a.view, 1140, 1000)
	if err := tt.Click(a.t("preset.chinese")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.settings.Languages, []string{"sc", "tc"}) {
		t.Fatal("mixed preset failed")
	}
	if err := tt.Click(a.t("weight.toggle")); err != nil {
		t.Fatal(err)
	}
	if a.settings.MatchWeights || !a.settings.ProcessOutlines {
		t.Fatal("weight toggle changed outline setting")
	}
	if err := tt.Click(a.t("outline.toggle")); err != nil {
		t.Fatal(err)
	}
	if a.settings.MatchWeights || a.settings.ProcessOutlines {
		t.Fatal("outline toggle changed weight setting")
	}
	if err := tt.Click(a.t("ui.language")); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Deutsch"); err != nil {
		t.Fatal(err)
	}
	if a.settings.Locale != "de" || !tt.HasText(a.t("build")) {
		t.Fatal("locale selector failed")
	}
}

func TestAllLocalesResponsive(t *testing.T) {
	output := os.Getenv("HD2_MYGO_SCREENSHOTS")
	if output != "" {
		if err := os.MkdirAll(output, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, locale := range Locales {
		for _, size := range []struct {
			w, h  int
			scale float32
		}{{1140, 860, 1}, {760, 620, 1.5}, {540, 480, 1}} {
			a := testApp(t, nil)
			a.settings.Locale = locale.Code
			a.localeName = locale.Name
			a.settings.PreviewText = a.t("preview.sample")
			tt := ui.NewTester(a.view, size.w, size.h)
			tt.SetScale(size.scale)
			if size.w == 1140 {
				for _, key := range []string{"preset.chinese", "weight.toggle", "outline.toggle"} {
					r, ok := tt.Find(a.t(key))
					if !ok {
						t.Fatalf("%s missing %s", locale.Code, key)
					}
					if r.Y+r.H > float32(size.h-140) {
						a.scroll.Y += r.Y + r.H - float32(size.h-150)
						tt.Frame()
					}
					if err := tt.Click(a.t(key)); err != nil {
						t.Fatal(err)
					}
				}
				if !slices.Equal(a.settings.Languages, []string{"sc", "tc"}) || a.settings.MatchWeights || a.settings.ProcessOutlines {
					t.Fatalf("%s controls not independent", locale.Code)
				}
				a.scroll.Y = 0
				tt.Frame()
			}
			for _, key := range []string{"build", "cancel", "output.open", "ui.language"} {
				r, ok := tt.Find(a.t(key))
				if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size.w)+1 || r.Y+r.H > float32(size.h)+1 {
					t.Errorf("%s %dx%d %s clipped: %+v", locale.Code, size.w, size.h, key, r)
				}
			}
			if output != "" {
				savePNG(t, filepath.Join(output, fmtName(locale.Code, size.w)), tt.Image())
			}
		}
	}
}

func fmtName(locale string, width int) string {
	if width == 1140 {
		return locale + "-desktop.png"
	}
	if width == 760 {
		return locale + "-150dpi.png"
	}
	return locale + "-compact.png"
}
func savePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, img)
	closeErr := f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestReadinessThemeAndLists(t *testing.T) {
	b := &recordingBackend{requests: make(chan BuildRequest, 1)}
	a := testApp(t, b)
	a.settings.OutputZIP = filepath.Join(t.TempDir(), "result.zip")
	want := []string{a.t("game.path"), a.t("section.font")}
	if got := a.missing(); !slices.Equal(got, want) {
		t.Fatalf("missing = %v, want %v", got, want)
	}
	tt := ui.NewTester(a.view, 1140, 860)
	if !tt.HasText(a.t("ready.missing", "items", a.list(want))) {
		t.Fatal("missing steps are not listed in the footer")
	}
	_ = tt.Click(a.t("build"))
	select {
	case req := <-b.requests:
		t.Fatalf("generate ran while steps were missing: %+v", req)
	default:
	}
	if missing := readyApp(t, b).missing(); len(missing) != 0 {
		t.Fatalf("ready app still misses %v", missing)
	}

	tt.SetDark(true)
	tt.Frame()
	if a.theme == nil || !a.theme.Dark {
		t.Fatal("theme does not follow the dark system appearance")
	}
	if output := os.Getenv("HD2_MYGO_SCREENSHOTS"); output != "" {
		savePNG(t, filepath.Join(output, "en-dark.png"), tt.Image())
	}
	tt.SetDark(false)
	tt.Frame()
	if a.theme.Dark {
		t.Fatal("theme does not return to the light appearance")
	}

	a.settings.Locale = "zh-TW"
	if got := a.list([]string{"甲", "乙"}); got != "甲、乙" {
		t.Fatalf("zh-TW list = %q", got)
	}
	a.settings.Locale = "de"
	if got := a.list([]string{"A", "B"}); got != "A, B" {
		t.Fatalf("de list = %q", got)
	}
}
