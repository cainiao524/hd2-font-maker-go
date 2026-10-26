package mygoapp

import (
	"context"
	"image"
)

// Backend runs entirely in native code. Methods run on worker goroutines.
// Implementations must honor cancellation and must not write to GameFolder.
type Backend interface {
	InspectFont(context.Context, string) (FontInfo, error)
	Preview(context.Context, PreviewRequest) (PreviewResult, error)
	Generate(context.Context, BuildRequest, func(Progress)) (BuildResult, error)
}

type FontFace struct {
	Index int        `json:"index"`
	Name  string     `json:"name"`
	Axes  []AxisInfo `json:"axes,omitempty"`
	// Style is the face's family, weight, width and slope for weight matching.
	Style FaceStyle `json:"style"`
}

type FaceStyle struct {
	Family    string `json:"family"`
	Subfamily string `json:"subfamily"`
	Weight    int    `json:"weight"`
	Width     int    `json:"width"`
	Italic    bool   `json:"italic"`
}

// FamilyFace is a face of another file that may join the selected face's family.
type FamilyFace struct {
	Path      string    `json:"path"`
	FaceIndex int       `json:"face_index"`
	Name      string    `json:"name"`
	Style     FaceStyle `json:"style"`
}

type AxisInfo struct {
	Tag     string  `json:"tag"`
	Name    string  `json:"name"`
	Min     float32 `json:"min"`
	Default float32 `json:"default"`
	Max     float32 `json:"max"`
	// Step is optional; zero lets the UI select a suitable numeric step.
	Step float32 `json:"step,omitempty"`
}

type FontInfo struct {
	Path  string
	Faces []FontFace
	// Siblings are faces of other font files beside Path, already stored like
	// Path, whose family matches one of Faces.
	Siblings []FamilyFace
}

type FontSelection struct {
	Path      string `json:"path"`
	FaceIndex int    `json:"face_index"`
	// Axes are OpenType design coordinates, shared by preview and generation.
	// Missing values mean the font's own defaults.
	Axes map[string]float32 `json:"axes,omitempty"`
	// Family lists the other static faces of the selected face's family, and
	// WeightFaces pins weight rows (see pipeline.Options).
	Family      []FamilyFace `json:"family,omitempty"`
	WeightFaces map[int]int  `json:"weight_faces,omitempty"`
}

type BuildRequest struct {
	GameFolder string        `json:"game_folder"`
	Font       FontSelection `json:"font"`
	// Languages contains sc, tc, latin, jp, ko. SC+TC is ["sc", "tc"].
	Languages       []string `json:"languages"`
	MatchWeights    bool     `json:"match_weights"`
	ProcessOutlines bool     `json:"process_outlines"`
	IncludeSymbols  bool     `json:"include_symbols"`
	OutputZIP       string   `json:"output_zip"`
	// ModName is the package name mod managers show; it also derives the
	// package GUID, so differently named packages can be kept side by side.
	ModName string `json:"mod_name"`
}

type PreviewRequest struct {
	Font            FontSelection
	Text            string
	MatchWeights    bool
	ProcessOutlines bool
	// Weight is the game weight row to preview (400, 500 or 700); zero keeps
	// the bold preview MatchWeights has always shown.
	Weight int
	Width  int
	Height int
	// GameFontID selects an original game face for preview only.
	GameFolder string
	GameFontID string
}

type GameFont struct {
	ID   string
	Name string
}

type GameFontBackend interface {
	GameFonts(context.Context, string) ([]GameFont, error)
}

type PreviewResult struct {
	Image             image.Image
	MissingCharacters int
}

type Progress struct {
	// Stage is a catalog suffix: source, scan, scope, weights, merge,
	// outlines, convert, register, atlas, package, cache, done, working.
	Stage     string
	Completed int64
	Total     int64
	// Fraction is overall progress in [0,1]; negative means indeterminate.
	Fraction float64
	Detail   string
}

type BuildResult struct {
	OutputZIP      string
	Bytes          int64
	ResourceGroups []string
	Cached         bool
}

// Options configures Run. Backend is required for generation; nil explicitly
// disables it. SettingsPath optionally selects a portable settings file.
type Options struct {
	Backend      Backend
	SettingsPath string
	Locale       string
	InitialFont  string
	Width        int
	Height       int
	// Demo visibly marks an explicit demonstration session. It never enables
	// a backend by itself.
	Demo bool
}
