package mygoapp

import (
	"context"
	"errors"
	"strings"
	"time"
)

func (a *application) loadGameFonts() {
	backend, ok := a.backend.(GameFontBackend)
	if a.busy || !ok {
		return
	}
	folder := a.settings.GameFolder
	if strings.TrimSpace(folder) == "" {
		a.setError("preview.game_missing", nil)
		return
	}
	a.busy, a.cancelling = true, false
	a.errorKey, a.errorDetail = "", ""
	a.progress, a.started = Progress{Stage: "scan", Fraction: -1}, time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	go func() {
		defer cancel()
		fonts, err := backend.GameFonts(ctx, folder)
		if err == nil {
			err = ctx.Err()
		}
		a.post(func() {
			a.busy, a.cancelling, a.cancel = false, false, nil
			if errors.Is(err, context.Canceled) {
				a.statusKey = "native.cancelled"
				return
			}
			if err != nil {
				a.setError("error.font_read", err)
				return
			}
			if len(fonts) == 0 {
				a.setError("error.font_read", errors.New("no game font presets found"))
				return
			}
			a.gameFonts, a.gameFontFolder = fonts, folder
			a.selectGamePreview(fonts[0].ID)
			a.statusKey = "preview.ready"
		})
	}()
}

// selectGamePreview chooses the original game font the preview compares with.
func (a *application) selectGamePreview(id string) {
	a.gameFontID = id
	a.compare = id != ""
	a.changed(true)
}

// The sample is edited on one line; " / " stands for its line breaks.
func flatSample(text string) string { return strings.ReplaceAll(text, "\n", " / ") }

func sampleText(flat string) string { return strings.ReplaceAll(flat, " / ", "\n") }

// previousPreviewSamples are the preview.sample values shipped through 2.2.5.
// A saved copy of any of them is still the untouched default.
var previousPreviewSamples = func() map[string]struct{} {
	slogans := []string{
		"Für Über-Erde!",
		"For Super Earth!",
		"¡Por la Supertierra!",
		"Pour la Super-Terre !",
		"Per la Super Terra!",
		"スーパーアースのために！",
		"슈퍼지구를 위하여!",
		"Za Super Ziemię!",
		"Pela Superterra!",
		"За Супер-Землю!",
		"为了超级地球！",
		"為了超級地球！",
	}
	samples := make(map[string]struct{}, len(slogans))
	for _, slogan := range slogans {
		samples[slogan+"\nPlayer G4 - 0123456789"] = struct{}{}
	}
	return samples
}()

// normalizePreviewText fills an empty sample and replaces a saved previous
// default with the current locale's sample. Any other text is a user edit.
func normalizePreviewText(locale, text string) string {
	if text == "" {
		return Translate(locale, "preview.sample")
	}
	if _, ok := previousPreviewSamples[text]; ok {
		return Translate(locale, "preview.sample")
	}
	return text
}
