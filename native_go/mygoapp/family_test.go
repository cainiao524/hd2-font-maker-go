package mygoapp

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
	"hd2font/native/internal/inventory"
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

func TestCollectionKeepsEqualWeightFaces(t *testing.T) {
	regular := FontFace{Index: 0, Name: "Pixel Regular", Style: FaceStyle{Family: "Pixel", Weight: 400, Width: 5}}
	supplement := regular
	supplement.Index, supplement.Name = 1, "Pixel CJK"
	bold := regular
	bold.Index, bold.Style.Weight = 2, 700
	italic := regular
	italic.Index, italic.Style.Italic = 3, true
	other := regular
	other.Index, other.Style.Family = 4, "Other"
	members := familyOf("font.ttc", []FontFace{regular, supplement, bold, italic, other}, regular)
	if len(members) != 3 || members[1].FaceIndex != 1 || members[2].FaceIndex != 2 {
		t.Fatalf("collection members: %+v", members)
	}
}

// familyBackend imports a collection whose stored folder holds no siblings,
// as after a restart.
type familyBackend struct {
	recordingBackend
	faces []FontFace
}

func TestSingleFontWeightChoicePersistsAndResetsForNewFont(t *testing.T) {
	face := FontFace{Index: 0, Name: "Pixel Light", Style: FaceStyle{Family: "Pixel", Weight: 300, Width: 5}}
	b := &familyBackend{recordingBackend: recordingBackend{requests: make(chan BuildRequest, 1)}, faces: []FontFace{face}}
	a := readyApp(t, b)
	a.inspectFont("stored/single.ttf")
	awaitState(t, a, func() bool { return !a.busy })
	if inventory.SingleFontAnchor(a.settings.Font.SingleFontWeight) != 500 {
		t.Fatal("new single font did not default to medium")
	}
	tt := ui.NewTester(a.view, 1140, 1100)
	if output := os.Getenv("HD2_MYGO_SCREENSHOTS"); output != "" {
		savePNG(t, filepath.Join(output, "single-default-medium.png"), tt.Image())
	}
	a.settings.Font.WeightFaces = map[int]int{700: 0}
	if err := tt.Click(a.t("weight.medium") + " (500)"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(a.t("weight.single_regular") + " (400)"); err != nil {
		t.Fatal(err)
	}
	if a.settings.Font.SingleFontWeight != 400 || len(a.settings.Font.WeightFaces) != 0 {
		t.Fatalf("source selection did not restore automatic derivation: %+v", a.settings.Font)
	}
	rows := a.weightRows()
	if rows[0].Kind != "native" || rows[1].Kind != "synthetic" || rows[2].Kind != "synthetic" {
		t.Fatalf("regular source rows: %+v", rows)
	}
	if output := os.Getenv("HD2_MYGO_SCREENSHOTS"); output != "" {
		savePNG(t, filepath.Join(output, "single-selected-regular.png"), tt.Image())
	}
	a.save()
	loaded, err := LoadSettings(a.settingsPath)
	if err != nil || loaded.Font.SingleFontWeight != 400 {
		t.Fatalf("source assignment not saved: %+v %v", loaded.Font, err)
	}
	a.settings = loaded
	a.inspectFont(loaded.Font.Path)
	awaitState(t, a, func() bool { return !a.busy })
	if a.settings.Font.SingleFontWeight != 400 {
		t.Fatal("reloading reset the selected source weight")
	}
	a.inspectFont("stored/different.ttf")
	awaitState(t, a, func() bool { return !a.busy })
	if inventory.SingleFontAnchor(a.settings.Font.SingleFontWeight) != 500 {
		t.Fatal("a new font inherited the previous font's source assignment")
	}
}

func TestCollectionAutomaticallyMatchesThreeNativeWeights(t *testing.T) {
	faces := []FontFace{
		{Index: 0, Name: "Pixel Bold", Style: FaceStyle{Family: "Pixel", Subfamily: "Bold", Weight: 700, Width: 5}},
		{Index: 1, Name: "Pixel Regular", Style: FaceStyle{Family: "Pixel", Subfamily: "Regular", Weight: 400, Width: 5}},
		{Index: 2, Name: "Pixel Medium", Style: FaceStyle{Family: "Pixel", Subfamily: "Medium", Weight: 500, Width: 5}},
	}
	b := &familyBackend{recordingBackend: recordingBackend{requests: make(chan BuildRequest, 1)}, faces: faces}
	a := readyApp(t, b)
	a.settings.Font.SingleFontWeight = 700
	a.inspectFont("stored/three-weights.ttc")
	awaitState(t, a, func() bool { return !a.busy })
	if len(a.settings.Font.Family) != 2 || a.settings.Font.FaceIndex != 1 {
		t.Fatalf("collection family was not discovered: %+v", a.settings.Font)
	}
	for i, row := range a.weightRows() {
		want := []string{"Pixel Regular", "Pixel Medium", "Pixel Bold"}[i]
		if row.Kind != "native" || row.Source != want {
			t.Fatalf("collection weight %d: %+v, want %s as drawn", row.Target, row, want)
		}
	}
	tt := ui.NewTester(a.view, 1140, 1100)
	if tt.HasText(a.t("weight.single_source")) {
		t.Fatal("single-font source selector shown for a complete family")
	}
}

func (b *familyBackend) InspectFont(_ context.Context, path string) (FontInfo, error) {
	return FontInfo{Path: path, Faces: b.faces}, nil
}

func TestReloadDropsExternalFilesButKeepsCollectionFaces(t *testing.T) {
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
	if found || len(a.members()) != 2 || a.settings.Font.FaceIndex != 0 {
		t.Fatalf("reload retained an external file or lost the collection: %+v", a.settings.Font)
	}
}

func TestFamilyRowsLabelsAndRebuilds(t *testing.T) {
	a := readyApp(t, &recordingBackend{requests: make(chan BuildRequest, 1), release: make(chan struct{})})
	bold := FaceStyle{Family: "Pixel", Subfamily: "Bold", Weight: 700, Width: 5}
	a.sourceFace = FamilyFace{Path: a.settings.Font.Path, FaceIndex: 2, Name: "Pixel Bold", Style: bold}
	kinds := map[int]string{}
	for _, row := range a.weightRows() {
		kinds[row.Target] = row.Kind
	}
	if kinds[400] != "reduction" || kinds[500] != "native" || kinds[700] != "synthetic" {
		t.Fatalf("lone bold rows: %+v", a.weightRows())
	}
	a.sourceFace.Style.Weight = 300
	kinds = map[int]string{}
	for _, row := range a.weightRows() {
		kinds[row.Target] = row.Kind
	}
	if kinds[400] != "reduction" || kinds[500] != "native" || kinds[700] != "synthetic" {
		t.Fatalf("lone light rows: %+v", a.weightRows())
	}
	a.sourceFace.Style.Weight = 400
	kinds = map[int]string{}
	for _, row := range a.weightRows() {
		kinds[row.Target] = row.Kind
	}
	if kinds[400] != "reduction" || kinds[500] != "native" || kinds[700] != "synthetic" {
		t.Fatalf("lone regular rows: %+v", a.weightRows())
	}
	a.settings.Font.WeightFaces = map[int]int{700: 0}
	for _, row := range a.weightRows() {
		if row.Target == 700 && row.Kind != "native" {
			t.Fatalf("pinning the only face still synthesized 700: %+v", row)
		}
	}
	a.settings.Font.WeightFaces = nil
	a.sourceFace.Style = bold
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

func TestRestoreDuplicateCachedFontUsesSingleMediumAnchor(t *testing.T) {
	dir := filepath.Join("UserData", "Fonts", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	path, copyPath := filepath.Join(dir, "regular.ttf"), filepath.Join(dir, "regular (1).ttf")
	regular := FontFace{Name: "Regular", Style: FaceStyle{Family: "Sample", Weight: 400, Width: 5}}
	b := &refreshFamilyBackend{info: FontInfo{Path: path, Faces: []FontFace{regular}}}
	a := testApp(t, b)
	a.settings.MatchWeights = true
	a.settings.Font = FontSelection{Path: path, Family: []FamilyFace{candidate(copyPath, regular)}, WeightFaces: map[int]int{500: 1}}
	a.sourceFace = candidate(path, regular)
	a.inspectFont(path)
	awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
	if a.errorKey != "" || len(a.settings.Font.Family) != 0 || a.settings.Font.WeightFaces[500] != 0 {
		t.Fatalf("saved duplicate was not migrated: %+v %s", a.settings.Font, a.errorDetail)
	}
	rows := a.weightRows()
	if len(rows) != 3 || rows[0].Kind != "reduction" || rows[1].Kind != "native" || rows[2].Kind != "synthetic" {
		t.Fatalf("restored copy bypassed the single font rules: %+v", rows)
	}
}

func (b *refreshFamilyBackend) InspectFont(context.Context, string) (FontInfo, error) {
	return b.info, nil
}

func (b *refreshFamilyBackend) Preview(context.Context, PreviewRequest) (PreviewResult, error) {
	return PreviewResult{Image: image.NewRGBA(image.Rect(0, 0, 2, 2))}, nil
}

func TestImportAndReloadIgnoreExternalFiles(t *testing.T) {
	regular := FontFace{Index: 0, Name: "Regular", Style: FaceStyle{Family: "Pixel", Weight: 400, Width: 5}}
	bold := FamilyFace{Path: "stored/bold.ttf", Name: "Bold", Style: FaceStyle{Family: "Pixel", Weight: 700, Width: 5}}
	b := &refreshFamilyBackend{info: FontInfo{Path: "stored/regular.ttf", Faces: []FontFace{regular}, Siblings: []FamilyFace{bold}}}
	a := testApp(t, b)
	a.settings.MatchWeights = true
	a.settings.Font = FontSelection{Path: b.info.Path, Family: []FamilyFace{bold}, WeightFaces: map[int]int{500: 0, 700: 1}}
	for _, path := range []string{b.info.Path, "downloads/regular.ttf"} {
		a.inspectFont(path)
		awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
		if a.errorKey != "" || len(a.settings.Font.Family) != 0 {
			t.Fatalf("external file survived import: %+v %s", a.settings.Font, a.errorDetail)
		}
		if _, ok := a.settings.Font.WeightFaces[700]; ok {
			t.Fatal("pin to removed external font survived")
		}
		if a.weightRows()[2].Kind != "synthetic" {
			t.Fatal("external Bold still selected")
		}
	}
	a.save()
	saved, err := LoadSettings(a.settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	restarted := testApp(t, b)
	restarted.settings = saved
	restarted.inspectFont(saved.Font.Path)
	awaitState(t, restarted, func() bool { return !restarted.busy && !restarted.previewBusy })
	if len(restarted.settings.Font.Family) != 0 {
		t.Fatal("restart restored removed external fonts")
	}
	// Explicitly importing the old sibling must replace the selected file.
	b.info = FontInfo{Path: bold.Path, Faces: []FontFace{{Name: bold.Name, Style: bold.Style}}}
	a.inspectFont(bold.Path)
	awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
	if a.settings.Font.Path != bold.Path || len(a.settings.Font.Family) != 0 || len(a.settings.Font.WeightFaces) != 0 {
		t.Fatalf("new import retained old sources: %+v", a.settings.Font)
	}
}

func TestCollectionReloadKeepsManualSources(t *testing.T) {
	regular := FontFace{Index: 0, Name: "Regular", Style: FaceStyle{Family: "Pixel", Weight: 400, Width: 5}}
	medium := regular
	medium.Index, medium.Name, medium.Style.Weight = 1, "Medium", 500
	bold := regular
	bold.Index, bold.Name, bold.Style.Weight = 2, "Bold", 700
	b := &refreshFamilyBackend{info: FontInfo{Path: "stored/family.ttc", Faces: []FontFace{regular, medium, bold}}}
	a := testApp(t, b)
	a.settings.MatchWeights = true
	a.inspectFont("downloads/family.ttc")
	awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
	a.settings.Font.WeightFaces = map[int]int{500: 2, 700: -1}
	saved := a.settings.Font
	for _, path := range []string{"downloads/family.ttc", saved.Path} {
		a.inspectFont(path)
		awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
		if !reflect.DeepEqual(a.settings.Font, saved) {
			t.Fatalf("collection choices changed: %+v want %+v", a.settings.Font, saved)
		}
	}
	a.save()
	loaded, err := LoadSettings(a.settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	restarted := testApp(t, b)
	restarted.settings = loaded
	restarted.inspectFont(saved.Path)
	awaitState(t, restarted, func() bool { return !restarted.busy && !restarted.previewBusy })
	if !reflect.DeepEqual(restarted.settings.Font, saved) {
		t.Fatalf("restart changed collection choices: %+v", restarted.settings.Font)
	}
}
