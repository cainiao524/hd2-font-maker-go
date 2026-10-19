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
}

type FontSelection struct {
	Path      string `json:"path"`
	FaceIndex int    `json:"face_index"`
	// Axes are OpenType design coordinates, shared by preview and generation.
	// Missing values mean the font's own defaults.
	Axes map[string]float32 `json:"axes,omitempty"`
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
}

type PreviewRequest struct {
	Font            FontSelection
	Text            string
	MatchWeights    bool
	ProcessOutlines bool
	Width           int
	Height          int
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
