package mygoapp

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
)

type gamePreviewBackend struct {
	recordingBackend
	previewRequests []PreviewRequest
	failure         error
}

func (b *gamePreviewBackend) GameFonts(ctx context.Context, folder string) ([]GameFont, error) {
	if b.failure != nil {
		return nil, b.failure
	}
	return []GameFont{{ID: "steelworks", Name: "Steelworks for HELLDIVERS 2"}, {ID: "sinclair", Name: "FSSinclair"}}, ctx.Err()
}

func (b *gamePreviewBackend) Preview(ctx context.Context, req PreviewRequest) (PreviewResult, error) {
	b.previewRequests = append(b.previewRequests, req)
	return PreviewResult{Image: image.NewRGBA(image.Rect(0, 0, 1000, 480))}, ctx.Err()
}

func TestSingleGamePreviewDoesNotChangeBuild(t *testing.T) {
	b := &gamePreviewBackend{}
	a := readyApp(t, b)
	before := a.settings.BuildRequest
	a.loadGameFonts()
	awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
	if len(a.gameFonts) != 2 || a.gameFontID != "steelworks" || a.preview == nil {
		t.Fatal("original preset did not load")
	}
	req := b.previewRequests[len(b.previewRequests)-1]
	if req.GameFolder != before.GameFolder || req.GameFontID != "steelworks" {
		t.Fatalf("preview request: %+v", req)
	}
	if !reflect.DeepEqual(before, a.settings.BuildRequest) {
		t.Fatal("preview changed generation settings")
	}
	tt := ui.NewTester(a.view, 1140, 860)
	if tt.HasText("Standard") || catalogs["en"]["preview.helldivers"] != "" {
		t.Fatal("unnecessary preview tab remains")
	}
	if err := tt.Click(a.t("preview.font")); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("FSSinclair"); err != nil {
		t.Fatal(err)
	}
	awaitState(t, a, func() bool { return !a.previewBusy })
	if a.gameFontID != "sinclair" {
		t.Fatal("preset selection failed")
	}
	tt.Frame()
	if err := tt.Click(a.t("preview.font")); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(a.t("preview.custom")); err != nil {
		t.Fatal(err)
	}
	awaitState(t, a, func() bool { return !a.previewBusy })
	if a.gameFontID != "" || !reflect.DeepEqual(before, a.settings.BuildRequest) {
		t.Fatal("custom preview changed generation settings")
	}
	a.selectGamePreview("steelworks")
	awaitState(t, a, func() bool { return !a.previewBusy })
	a.settings.GameFolder = filepath.Join(t.TempDir(), "different-game")
	tt.Frame()
	awaitState(t, a, func() bool { return !a.previewBusy })
	if a.gameFontID != "" || len(a.gameFonts) != 0 {
		t.Fatal("stale original font retained after changing game")
	}
}

func TestGamePreviewFailuresAndOldSettings(t *testing.T) {
	b := &gamePreviewBackend{failure: errors.New("unreadable game")}
	a := testApp(t, b)
	a.loadGameFonts()
	if a.errorKey != "preview.game_missing" {
		t.Fatal("missing folder not reported")
	}
	a.settings.GameFolder = "unreadable"
	a.loadGameFonts()
	awaitState(t, a, func() bool { return !a.busy })
	if a.errorDetail != "unreadable game" || a.gameFontID != "" {
		t.Fatal("failed read selected a font")
	}
	var s Settings
	if err := json.Unmarshal([]byte(`{"preview_style":"helldivers","preview_text":"sample"}`), &s); err != nil || s.PreviewText != "sample" {
		t.Fatal("old settings incompatible")
	}
}

func TestSinglePreviewLocales(t *testing.T) {
	file := os.Getenv("HD2_GAME_PREVIEW_PNG")
	if file == "" {
		t.Skip("set HD2_GAME_PREVIEW_PNG for original-font layout screenshots")
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
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
			a := testApp(t, &gamePreviewBackend{})
			a.settings.Locale, a.localeName = locale.Code, locale.Name
			a.settings.PreviewText = "HELLDIVERS 2\nFOR SUPER EARTH! 0123456789"
			a.gameFontID = "steelworks"
			a.gameFonts = []GameFont{{ID: "steelworks", Name: "Steelworks for HELLDIVERS 2"}}
			a.preview = ui.NewBitmap(img)
			tt := ui.NewTester(a.view, size.w, size.h)
			tt.SetScale(size.scale)
			if r, ok := tt.Find(a.t("preview.font")); ok && size.w < 960 {
				a.scroll.Y += r.Y - 130
				tt.Frame()
			}
			for _, key := range []string{"preview.font", "preview.load_game", "preview.update"} {
				r, ok := tt.Find(a.t(key))
				if !ok || r.X < 0 || r.X+r.W > float32(size.w)+1 || r.Y < 0 || r.Y+r.H > float32(size.h)+1 {
					t.Errorf("%s %d %s clipped: %+v", locale.Code, size.w, key, r)
				}
			}
			if output != "" {
				savePNG(t, filepath.Join(output, fmtName(locale.Code, size.w)), tt.Image())
			}
		}
	}
}
