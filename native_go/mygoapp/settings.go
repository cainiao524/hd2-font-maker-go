package mygoapp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type Settings struct {
	Version int    `json:"version"`
	Locale  string `json:"ui_locale"`
	BuildRequest
	PreviewText string `json:"preview_text"`
	LastOutput  string `json:"last_output"`
}

func DefaultSettings() Settings {
	return Settings{Version: 1, Locale: systemLocale(), BuildRequest: BuildRequest{
		Languages: []string{"sc"}, MatchWeights: true, ProcessOutlines: true,
	}}
}

func LoadSettings(path string) (Settings, error) {
	s := DefaultSettings()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return DefaultSettings(), err
	}
	s.Locale = ResolveLocale(s.Locale)
	return s, nil
}

func SaveSettings(path string, s Settings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "settings-*.pending")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

// ValidateRequest rejects accidental output into the game installation before
// dispatch. The backend remains responsible for canonical path validation.
func ValidateRequest(r BuildRequest) string {
	if strings.TrimSpace(r.Font.Path) == "" {
		return "error.no_font"
	}
	if r.Font.FaceIndex < 0 {
		return "error.font_read"
	}
	if strings.TrimSpace(r.GameFolder) == "" {
		return "error.game"
	}
	if len(r.Languages) == 0 {
		return "error.no_language"
	}
	for _, lang := range r.Languages {
		switch lang {
		case "sc", "tc", "latin", "jp", "ko":
		default:
			return "error.no_language"
		}
	}
	if !strings.EqualFold(filepath.Ext(r.OutputZIP), ".zip") {
		return "error.output_zip"
	}
	game, err := filepath.Abs(r.GameFolder)
	if err != nil {
		return "error.game"
	}
	out, err := filepath.Abs(r.OutputZIP)
	if err != nil {
		return "error.output_location"
	}
	rel, err := filepath.Rel(game, out)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "error.output_location"
	}
	if strings.EqualFold(out, r.Font.Path) {
		return "error.output_location"
	}
	return ""
}
