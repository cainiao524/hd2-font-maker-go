package mygoapp

import (
	"context"
	"image"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestCollectionSelectionWithoutWeightMatching(t *testing.T) {
	regular := FontFace{Index: 0, Name: "Pixel Regular", Style: FaceStyle{Family: "Pixel", Subfamily: "Regular", Weight: 400, Width: 5}}
	bold := FontFace{Index: 1, Name: "Pixel Bold", Style: FaceStyle{Family: "Pixel", Subfamily: "Bold", Weight: 700, Width: 5}}
	for _, viaMenu := range []bool{true, false} {
		name := "source"
		if viaMenu {
			name = "selector"
		}
		t.Run(name, func(t *testing.T) {
			b := &familyBackend{recordingBackend: recordingBackend{requests: make(chan BuildRequest, 1)}, faces: []FontFace{regular, bold}}
			a := readyApp(t, b)
			a.settings.MatchWeights = false
			a.inspectFont("stored/pixel.ttc")
			awaitState(t, a, func() bool { return !a.busy })
			if viaMenu {
				tt := ui.NewTester(a.view, 1140, 1000)
				if err := tt.Click(a.t("dialog.face")); err != nil {
					t.Fatal(err)
				}
				if err := tt.Click(faceLabel(bold)); err != nil {
					t.Fatal(err)
				}
			} else {
				a.selectFace(bold)
			}
			if a.settings.Font.Path != a.facesPath || a.settings.Font.FaceIndex != bold.Index {
				t.Fatalf("selected Bold was replaced by another source: %+v", a.settings.Font)
			}
			a.inspectFont(a.settings.Font.Path)
			awaitState(t, a, func() bool { return !a.busy })
			if a.settings.Font.FaceIndex != bold.Index {
				t.Fatalf("reloading lost the selected Bold: %+v", a.settings.Font)
			}
		})
	}
}

func TestFamilyKeepsEqualWeightFaces(t *testing.T) {
	regular := FontFace{Index: 0, Name: "Pixel Regular", Style: FaceStyle{Family: "Pixel", Subfamily: "Regular", Weight: 400, Width: 5}}
	siblings := []FamilyFace{
		{Path: "cjk.ttf", Name: "Pixel CJK", Style: FaceStyle{Family: "Pixel", Subfamily: "Regular", Weight: 400, Width: 5}},
		{Path: "bold.ttf", Name: "Pixel Bold", Style: FaceStyle{Family: "Pixel", Subfamily: "Bold", Weight: 700, Width: 5}},
		{Path: "italic.ttf", Name: "Pixel Italic", Style: FaceStyle{Family: "Pixel", Weight: 400, Width: 5, Italic: true}},
		{Path: "other.ttf", Name: "Other", Style: FaceStyle{Family: "Other", Weight: 700, Width: 5}},
	}
	members := familyOf("latin.ttf", []FontFace{regular}, siblings, regular)
	if len(members) != 3 || members[1].Path != "cjk.ttf" || members[2].Path != "bold.ttf" {
		t.Fatalf("family members: %+v", members)
	}
}

// familyBackend imports a collection whose stored folder holds no siblings,
// as after a restart.
type familyBackend struct {
	recordingBackend
	faces []FontFace
}

func (b *familyBackend) InspectFont(_ context.Context, path string) (FontInfo, error) {
	return FontInfo{Path: path, Faces: b.faces}, nil
}

func TestReloadKeepsExternalFamilyFaces(t *testing.T) {
	regular := FontFace{Index: 0, Name: "Pixel Regular", Style: FaceStyle{Family: "Pixel", Subfamily: "Regular", Weight: 400, Width: 5}}
	medium := FontFace{Index: 1, Name: "Pixel Medium", Style: FaceStyle{Family: "Pixel", Subfamily: "Medium", Weight: 500, Width: 5}}
	b := &familyBackend{recordingBackend: recordingBackend{requests: make(chan BuildRequest, 1)}, faces: []FontFace{regular, medium}}
	a := testApp(t, b)
	bold := FamilyFace{Path: "stored/bold.ttf", Name: "Pixel Bold", Style: FaceStyle{Family: "Pixel", Subfamily: "Bold", Weight: 700, Width: 5}}
	a.settings.Font = FontSelection{Path: "stored/pixel.ttc", Family: []FamilyFace{{Path: "stored/pixel.ttc", FaceIndex: 1, Name: "Pixel Medium", Style: medium.Style}, bold}}
	a.inspectFont("stored/pixel.ttc")
	awaitState(t, a, func() bool { return !a.busy })
	a.selectFace(medium)
	found := false
	for _, m := range a.members() {
		found = found || m.Path == bold.Path
	}
	if !found || a.settings.Font.FaceIndex != 0 {
		t.Fatalf("reload lost the external Bold or the Regular source: %+v", a.settings.Font)
	}
}

func TestFamilyRowsLabelsAndRebuilds(t *testing.T) {
	a := readyApp(t, &recordingBackend{requests: make(chan BuildRequest, 1), release: make(chan struct{})})
	bold := FaceStyle{Family: "Pixel", Subfamily: "Bold", Weight: 700, Width: 5}
	a.sourceFace = FamilyFace{Path: a.settings.Font.Path, FaceIndex: 2, Name: "Pixel Bold", Style: bold}
	// A lone font keeps the 2.0 rule: every row above 400 is emboldened.
	for _, row := range a.weightRows() {
		if (row.Target > 400) != (row.Kind == "synthetic") {
			t.Errorf("single-font row %d is %s", row.Target, row.Kind)
		}
	}
	a.settings.Font.Family = []FamilyFace{{Path: "cjk/pixel-bold.ttf", Name: "Pixel Bold", Style: bold}}
	labels := a.memberLabels()
	if labels[0] == labels[1] {
		t.Fatalf("same-named members are indistinguishable: %v", labels)
	}
	a.done = true
	a.generate()
	if a.done {
		t.Fatal("a new build still shows the previous completion")
	}
}

type refreshFamilyBackend struct {
	recordingBackend
	info FontInfo
}

func (b *refreshFamilyBackend) InspectFont(context.Context, string) (FontInfo, error) {
	return b.info, nil
}

func (b *refreshFamilyBackend) Preview(context.Context, PreviewRequest) (PreviewResult, error) {
	return PreviewResult{Image: image.NewRGBA(image.Rect(0, 0, 2, 2))}, nil
}

func TestReimportRefreshesFamilyAndRemapsWeightPins(t *testing.T) {
	regular := FontFace{Index: 0, Name: "Pixel Regular", Style: FaceStyle{Family: "Pixel", Subfamily: "Regular", Weight: 400, Width: 5}}
	medium := FamilyFace{Path: "stored/medium.ttf", Name: "Pixel Medium", Style: FaceStyle{Family: "Pixel", Subfamily: "Medium", Weight: 500, Width: 5}}
	bold := FamilyFace{Path: "stored/bold.ttf", Name: "Pixel Bold", Style: FaceStyle{Family: "Pixel", Subfamily: "Bold", Weight: 700, Width: 5}}
	b := &refreshFamilyBackend{info: FontInfo{Path: "stored/regular.ttf", Faces: []FontFace{regular}, Siblings: []FamilyFace{medium}}}
	a := testApp(t, b)
	a.settings.MatchWeights = true
	importOriginal := func() {
		a.inspectFont("downloads/regular.ttf")
		awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
		if a.errorKey != "" {
			t.Fatal(a.errorKey, a.errorDetail)
		}
	}
	importOriginal()
	a.settings.Font.WeightFaces = map[int]int{400: -1, 500: 1}
	// Discovery order can change when a newly added file sorts before Medium.
	b.info.Siblings = []FamilyFace{bold, medium}
	importOriginal()
	if len(a.members()) != 3 || a.members()[1].Path != bold.Path {
		t.Fatalf("reimport discarded the new Bold: %+v", a.members())
	}
	if want := (map[int]int{400: -1, 500: 2}); !reflect.DeepEqual(a.settings.Font.WeightFaces, want) {
		t.Fatalf("manual pins changed their source: got %v want %v", a.settings.Font.WeightFaces, want)
	}
	rows := a.weightRows()
	if rows[2].Source != bold.Name || rows[2].Kind != "native" {
		t.Fatalf("new Bold is not used as drawn: %+v", rows)
	}
	// A removed member's pin must not silently select whoever takes its index.
	b.info.Siblings = []FamilyFace{bold}
	importOriginal()
	if want := (map[int]int{400: -1}); !reflect.DeepEqual(a.settings.Font.WeightFaces, want) {
		t.Fatalf("removed Medium left a stale pin: %v", a.settings.Font.WeightFaces)
	}
	// Restoring the cached source still keeps its externally stored family.
	saved := a.settings.Font
	b.info.Siblings = nil
	a.inspectFont(saved.Path)
	awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
	if !reflect.DeepEqual(a.settings.Font, saved) {
		t.Fatalf("cache reload changed the family: got %+v want %+v", a.settings.Font, saved)
	}
}

func TestReimportNonBaseFamilyMemberKeepsManualWeights(t *testing.T) {
	for _, name := range []string{"single-face", "collection"} {
		t.Run(name, func(t *testing.T) {
			bold := FontFace{Index: 0, Name: "Pixel Bold", Style: FaceStyle{Family: "Pixel", Subfamily: "Bold", Weight: 700, Width: 5}}
			other := FontFace{Index: 0, Name: "Other Regular", Style: FaceStyle{Family: "Other", Subfamily: "Regular", Weight: 400, Width: 5}}
			regular := FamilyFace{Path: "stored/regular.ttf", Name: "Pixel Regular", Style: FaceStyle{Family: "Pixel", Subfamily: "Regular", Weight: 400, Width: 5}}
			faces := []FontFace{bold}
			if name == "collection" {
				bold.Index = 1
				faces = []FontFace{other, bold}
			}
			b := &refreshFamilyBackend{info: FontInfo{Path: "stored/bold.font", Faces: faces, Siblings: []FamilyFace{regular}}}
			a := testApp(t, b)
			a.settings.MatchWeights = true
			awaitImport := func() {
				awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
				if a.errorKey != "" {
					t.Fatal(a.errorKey, a.errorDetail)
				}
			}
			a.inspectFont("downloads/bold.font")
			awaitImport()
			if name == "collection" {
				a.selectFace(bold)
				awaitImport()
			}
			if a.settings.Font.Path != regular.Path {
				t.Fatalf("family base was not Regular: %+v", a.settings.Font)
			}
			a.settings.Font.WeightFaces = map[int]int{500: 1, 700: -1}
			a.inspectFont("downloads/bold.font")
			awaitImport()
			if want := (map[int]int{500: 1, 700: -1}); !reflect.DeepEqual(a.settings.Font.WeightFaces, want) {
				t.Fatalf("reimporting the original Bold reset manual sources: got %v want %v", a.settings.Font.WeightFaces, want)
			}
			rows := a.weightRows()
			if rows[1].Source != "Pixel Bold" || rows[2].Source != regular.Name || rows[2].Kind != "synthetic" {
				t.Fatalf("manual weights changed rendering after reimport: %+v", rows)
			}
			// A different file and family must still reset the old manual choices.
			b.info = FontInfo{Path: "stored/other.font", Faces: []FontFace{other}}
			a.inspectFont("downloads/other.font")
			awaitImport()
			if len(a.settings.Font.WeightFaces) != 0 || len(a.settings.Font.Family) != 0 {
				t.Fatalf("unrelated import retained the previous family: %+v", a.settings.Font)
			}
		})
	}
}
