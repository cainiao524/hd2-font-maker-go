// Package pipeline builds complete, offline-verified HD2 font replacement ZIPs.
package pipeline

import (
	"context"
	"runtime"

	"hd2font/native/internal/atlas"
	"hd2font/native/internal/inventory"
)

type Options struct {
	FontPath        string
	FaceIndex       int
	Axes            map[string]float32
	Languages       []string
	MatchWeights    bool
	ProcessOutlines bool
	IncludeSymbols  bool
	OutputZIP       string
	BinDir          string
	WorkDir         string
	Name            string
	// Workers is the number of independent build jobs, 1 through MaxWorkers.
	Workers int
	// ZIPLevel accepts 0..9; DefaultOptions selects fast, lossless level 1.
	ZIPLevel int
	// AtlasExecutable overrides the bundled native msdf-atlas-gen path.
	AtlasExecutable string
	// Family lists other static faces of the source's family. With
	// MatchWeights each game weight uses the closest face (inventory.PickWeight)
	// and missing glyphs come from the other faces before the game's. Empty
	// keeps the single-source path; variable fonts ignore it.
	Family []FontFace
	// WeightFaces pins the face of a weight row (inventory.WeightRow): 0 is the
	// source, 1.. index Family, and -1 asks for synthetic emphasis of the source.
	WeightFaces map[int]int
}

// FontFace names one face of a font file or collection.
type FontFace struct {
	Path      string
	FaceIndex int
}

// MaxWorkers bounds independent build jobs; each job also runs threads of its own.
const MaxWorkers = 8

// DefaultWorkers gives one build job per four logical CPUs, so an 8-thread
// machine keeps the original two jobs. Outputs do not depend on the count.
func DefaultWorkers() int { return max(1, min(MaxWorkers, runtime.NumCPU()/4)) }

func DefaultOptions() Options {
	return Options{Languages: []string{"sc"}, MatchWeights: true, ProcessOutlines: false, Workers: DefaultWorkers(), ZIPLevel: 1, Name: "HD2 Custom Fonts"}
}

type SelectionReport struct {
	Group                    string `json:"group"`
	Label                    string `json:"label"`
	Font                     string `json:"font"`
	FontSHA256               string `json:"font_sha256"`
	FaceIndex                int    `json:"face_index"`
	ProcessOutlines          bool   `json:"process_outlines"`
	OriginalCharacters       int    `json:"original_characters"`
	CustomCharacters         int    `json:"custom_characters"`
	EmbeddedCustomCharacters int    `json:"embedded_custom_characters"`
	FallbackCharacters       int    `json:"fallback_characters"`
}

type StyleReport struct {
	TargetWeight    int                `json:"target_weight"`
	Method          string             `json:"method"`
	Axes            map[string]float32 `json:"axes"`
	Mode            string             `json:"mode"`
	ProcessOutlines bool               `json:"process_outlines"`
	PixelGrid       bool               `json:"pixel_grid"`
	SourceSHA256    string             `json:"source_sha256"`
	SourceName      string             `json:"source_name"`
	OriginalName    string             `json:"original_name"`
	OriginalSHA256  string             `json:"original_sha256"`
	Group           string             `json:"group"`
}

type ResourceReport struct {
	Archive            string `json:"archive"`
	ID                 string `json:"id"`
	Kind               string `json:"kind"`
	OriginalMainSHA256 string `json:"original_main_sha256"`
	MainSHA256         string `json:"main_sha256"`
	StreamSHA256       string `json:"stream_sha256"`
	GPUSHA256          string `json:"gpu_sha256"`
}

type RuntimeReport struct {
	Archive                       string `json:"archive"`
	ID                            string `json:"id"`
	OriginalSlots                 int    `json:"original_slots"`
	OutputSlots                   int    `json:"output_slots"`
	ReplacedSlots                 []int  `json:"replaced_slots"`
	RetainedSelectedOriginalSlots int    `json:"retained_selected_original_slots"`
}

type AtlasReport struct {
	atlas.Report
	Archive      string `json:"archive"`
	Group        string `json:"group"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Glyphs       int    `json:"glyphs"`
	FontID       string `json:"font_id"`
	TextureID    string `json:"texture_id"`
	OriginalFace string `json:"original_face"`
	TargetWeight int    `json:"target_weight"`
	CustomSHA256 string `json:"custom_sha256"`
}

type FontVerification struct {
	OriginalSHA256          string `json:"original_sha256"`
	OutputSHA256            string `json:"output_sha256"`
	CustomCharacters        int    `json:"custom_characters"`
	RequiredUnionCharacters int    `json:"required_union_characters"`
	OutputCharacters        int    `json:"output_characters"`
	FullSourceCmapPreserved bool   `json:"full_source_cmap_preserved"`
	FallbackUnionPreserved  bool   `json:"fallback_union_preserved"`
	Format                  string `json:"format"`
	UPM                     uint16 `json:"upm"`
}

type FileReport struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type Report struct {
	Tool                       string                `json:"tool"`
	Version                    string                `json:"version"`
	EngineProfile              string                `json:"engine_profile"`
	CreatedAt                  string                `json:"created_at"`
	Name                       string                `json:"name"`
	Selections                 []SelectionReport     `json:"selections"`
	Coverage                   []inventory.Coverage  `json:"coverage"`
	Resources                  []ResourceReport      `json:"resources"`
	Atlases                    []AtlasReport         `json:"atlases"`
	FullReplacement            bool                  `json:"full_replacement"`
	KeepRuntimeFonts           bool                  `json:"keep_runtime_fonts"`
	RuntimeSlots               []RuntimeReport       `json:"runtime_slots"`
	NoesisFamilyStylePreserved bool                  `json:"noesis_family_style_preserved"`
	StyleAdaptation            []StyleReport         `json:"style_adaptation"`
	FontReadback               []FontVerification    `json:"font_readback"`
	LosslessCompression        map[string]any        `json:"lossless_compression"`
	MixedChineseUICharacters   int                   `json:"mixed_chinese_ui_characters"`
	Performance                map[string]any        `json:"performance"`
	Files                      map[string]FileReport `json:"files"`
	RuntimeVerified            bool                  `json:"runtime_verified"`
	Installed                  bool                  `json:"installed"`
	OfflineReadbackVerified    bool                  `json:"offline_readback_verified"`
	Note                       string                `json:"note"`
	Output                     string                `json:"output,omitempty"`
	ZIPSHA256                  string                `json:"zip_sha256,omitempty"`
	ZIPBytes                   int64                 `json:"zip_bytes,omitempty"`
	// FamilyFaces names the source and its family faces when Family was used.
	FamilyFaces []string `json:"family_faces,omitempty"`
}

// The only product implementation is nativeOps. This private boundary lets
// synthetic tests exercise orchestration and readback without native DLLs.
type fontOps interface {
	Prepare(context.Context, []byte, uint32, map[string]float32, bool) ([]byte, error)
	PrepareAtlasFallback(context.Context, []byte, bool) ([]byte, error)
	Style(context.Context, []byte, int, bool, bool) ([]byte, error)
	Merge(context.Context, []byte, [][]byte) ([]byte, error)
	Conform(context.Context, []byte, bool, uint16, []byte, bool) ([]byte, [3]float32, error)
	Register([]byte, []byte) ([]byte, error)
	Axes([]byte, int) ([]axis, error)
	Subset(context.Context, []byte, []rune) ([]byte, error)
}

type axis struct {
	Tag               string
	Min, Default, Max float32
}
type atlasGenerator func(context.Context, []atlas.Source, atlas.Options) (*atlas.Result, error)
