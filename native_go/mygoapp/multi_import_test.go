package mygoapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type multiImportBackend struct {
	refreshFamilyBackend
	infos     map[string]FontInfo
	inspected []string
	blocked   string
	entered   chan struct{}
}

func (b *multiImportBackend) InspectFont(ctx context.Context, path string) (FontInfo, error) {
	b.inspected = append(b.inspected, path)
	if path == b.blocked {
		close(b.entered)
		<-ctx.Done()
		return FontInfo{}, ctx.Err()
	}
	if info, ok := b.infos[path]; ok {
		return info, nil
	}
	return FontInfo{}, errors.New("unselected or unreadable font: " + path)
}

func batchBackend() *multiImportBackend {
	b := &multiImportBackend{infos: map[string]FontInfo{}}
	b.requests = make(chan BuildRequest, 1)
	for _, weight := range []int{400, 500, 700} {
		name := fmt.Sprintf("weight-%d.ttf", weight)
		face := FontFace{Name: name, Style: FaceStyle{Family: "Batch", Subfamily: fmt.Sprint(weight), Weight: weight, Width: 5}}
		info := FontInfo{Path: filepath.Join("stored", name), Faces: []FontFace{face}, Siblings: []FamilyFace{{Path: "unselected.ttf"}}}
		b.infos[name], b.infos[info.Path] = info, info
	}
	return b
}

func assertNativeBatch(t *testing.T, a *application) {
	t.Helper()
	if a.errorKey != "" || len(a.settings.Font.Family) != 2 || len(a.settings.Font.SelectedFiles) != 3 {
		t.Fatalf("batch import failed: %+v %s", a.settings.Font, a.errorDetail)
	}
	if a.settings.Font.Path != filepath.Join("stored", "weight-400.ttf") || a.facesPath != a.settings.Font.Path {
		t.Fatalf("wrong active file: %+v", a.settings.Font)
	}
	for _, row := range a.weightRows() {
		if row.Kind != "native" || row.Source != fmt.Sprintf("Batch %d", row.Target) {
			t.Fatalf("wrong native weight source: %+v", row)
		}
	}
}

func TestMultiImportMatchesExplicitFilesInAnyOrder(t *testing.T) {
	for _, order := range [][]int{{400, 500, 700}, {700, 500, 400}, {500, 400, 700}, {400, 700, 500}, {500, 700, 400}, {700, 400, 500}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			b := batchBackend()
			a := readyApp(t, b)
			a.settings.MatchWeights = true
			var paths []string
			for _, w := range order {
				paths = append(paths, fmt.Sprintf("weight-%d.ttf", w))
			}
			a.inspectFonts(paths)
			awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
			assertNativeBatch(t, a)
			if !slices.Equal(paths, b.inspected) {
				t.Fatalf("import accessed files outside selection: %v", b.inspected)
			}
			a.generate()
			req := <-b.requests
			awaitState(t, a, func() bool { return !a.busy })
			if !reflect.DeepEqual(req.Font, a.settings.Font) {
				t.Fatalf("build lost selected weights: %+v", req.Font)
			}
		})
	}
}

func TestMultiImportRestoresPinsAndSingleSelectionClearsBatch(t *testing.T) {
	b := batchBackend()
	a := testApp(t, b)
	a.inspectFonts([]string{"weight-700.ttf", "weight-400.ttf", "weight-500.ttf"})
	awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
	assertNativeBatch(t, a)
	a.settings.Font.WeightFaces = map[int]int{500: 1, 700: -1}
	a.save()
	saved, err := LoadSettings(a.settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	restarted := testApp(t, b)
	restarted.settings = saved
	restarted.inspectFont(saved.Font.Path)
	awaitState(t, restarted, func() bool { return !restarted.busy && !restarted.previewBusy })
	if !reflect.DeepEqual(restarted.settings.Font, saved.Font) {
		t.Fatalf("restart lost selected files/pins: got %+v want %+v", restarted.settings.Font, saved.Font)
	}
	restarted.inspectFonts([]string{saved.Font.Path})
	awaitState(t, restarted, func() bool { return !restarted.busy && !restarted.previewBusy })
	if len(restarted.settings.Font.Family) != 0 || len(restarted.settings.Font.SelectedFiles) != 0 {
		t.Fatalf("single import retained batch: %+v", restarted.settings.Font)
	}
	if _, ok := restarted.settings.Font.WeightFaces[500]; ok {
		t.Fatal("pin to removed file survived")
	}
	rows := restarted.weightRows()
	if rows[0].Kind != "reduction" || rows[1].Kind != "native" || rows[2].Kind != "synthetic" {
		t.Fatalf("single font lost Medium default: %+v", rows)
	}
}

func TestMultiFileDropImportsAllSupportedFiles(t *testing.T) {
	b := batchBackend()
	a := testApp(t, b)
	tt := ui.NewTester(a.view, 1140, 1000)
	if !tt.DropFiles(100, 200, "notes.txt", "weight-700.ttf", "weight-400.ttf", "weight-500.ttf") {
		t.Fatal("file drop was not accepted")
	}
	awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
	assertNativeBatch(t, a)
	if output := os.Getenv("HD2_MYGO_SCREENSHOTS"); output != "" {
		a.settings.Locale = "zh-CN"
		savePNG(t, filepath.Join(output, "multi-selected-native-weights.png"), ui.NewTester(a.view, 1140, 1000).Image())
	}
	if slices.Contains(b.inspected, "notes.txt") {
		t.Fatal("unsupported drop was inspected")
	}
}

func TestMultiImportDoesNotPartiallyReplaceOnFailureOrCancel(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			b := batchBackend()
			a := testApp(t, b)
			a.inspectFont("weight-500.ttf")
			awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
			before := a.settings.Font
			source := a.sourceFace
			if cancelled {
				b.blocked = "missing.ttf"
				b.entered = make(chan struct{})
			}
			a.inspectFonts([]string{"weight-700.ttf", "missing.ttf", "weight-400.ttf"})
			if cancelled {
				select {
				case <-b.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("import did not start")
				}
				a.cancelWork()
			}
			awaitState(t, a, func() bool { return !a.busy })
			if !reflect.DeepEqual(a.settings.Font, before) || a.sourceFace != source {
				t.Fatal("partial batch replaced active font")
			}
			if slices.Contains(b.inspected, "weight-400.ttf") {
				t.Fatal("import continued after failure")
			}
			if cancelled && a.statusKey != "native.cancelled" {
				t.Fatal("cancellation was not reported")
			}
			if !cancelled && a.errorKey == "" {
				t.Fatal("failure was not reported")
			}
		})
	}
}

func TestMultiImportDeduplicatesCopiesAndIgnoresUnrelatedFaces(t *testing.T) {
	b := batchBackend()
	dir := filepath.Join("UserData", "Fonts", strings.Repeat("a", 64))
	for _, name := range []string{"first.ttf", "copy.ttf"} {
		info := b.infos["weight-400.ttf"]
		info.Path = filepath.Join(dir, name)
		b.infos[name] = info
	}
	a := testApp(t, b)
	a.inspectFonts([]string{"first.ttf", "copy.ttf"})
	awaitState(t, a, func() bool { return !a.busy && !a.previewBusy })
	if len(a.settings.Font.Family) != 0 || len(a.settings.Font.SelectedFiles) != 0 || a.weightRows()[1].Kind != "native" {
		t.Fatalf("copies triggered family emphasis: %+v", a.settings.Font)
	}
	base := b.infos["weight-400.ttf"].Faces[0]
	italic := base
	italic.Style.Italic = true
	other := base
	other.Style.Family = "Other"
	variable := base
	variable.Axes = []AxisInfo{{Tag: "wght", Min: 100, Default: 400, Max: 900}}
	collection := FontInfo{Path: "weights.ttc", Faces: []FontFace{b.infos["weight-500.ttf"].Faces[0], b.infos["weight-700.ttf"].Faces[0]}}
	collection.Faces[1].Index = 1
	members := familyOf("regular.ttf", []FontFace{base}, base, collection, FontInfo{Path: "other.ttf", Faces: []FontFace{italic, other, variable}})
	if len(members) != 3 {
		t.Fatalf("wrong compatible faces: %+v", members)
	}
	if members[1].Style.Weight != 500 || members[2].Style.Weight != 700 || members[2].FaceIndex != 1 {
		t.Fatalf("collection weights missing: %+v", members)
	}
}
