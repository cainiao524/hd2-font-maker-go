package mygoapp

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

type axesBackend struct {
	recordingBackend
	previews chan PreviewRequest
}

func (b *axesBackend) Preview(_ context.Context, req PreviewRequest) (PreviewResult, error) {
	b.previews <- req
	return PreviewResult{Image: image.NewRGBA(image.Rect(0, 0, 400, 200))}, nil
}

func TestAxesDefaultsControlsPersistenceAndRequests(t *testing.T) {
	b := &axesBackend{recordingBackend: recordingBackend{requests: make(chan BuildRequest, 1)}, previews: make(chan PreviewRequest, 16)}
	a := readyApp(t, b)
	face := FontFace{Index: 2, Name: "Variable", Axes: []AxisInfo{{Tag: "wght", Name: "Weight", Min: 100, Default: 400, Max: 900}, {Tag: "wdth", Name: "Width", Min: 75, Default: 100, Max: 125}}}
	a.faces = []FontFace{face}
	a.selectFace(face)
	previewReq := <-b.previews
	awaitState(t, a, func() bool { return !a.previewBusy })
	if previewReq.Font.Axes["wght"] != 400 || a.settings.Font.Axes["wdth"] != 100 {
		t.Fatal("font defaults not preserved")
	}
	tt := ui.NewTester(a.view, 1140, 1100)
	if err := tt.Click("Increase"); err != nil {
		t.Fatal(err)
	}
	if a.settings.Font.Axes["wght"] != 401 {
		t.Fatalf("stepper did not update weight: %+v", a.settings.Font.Axes)
	}
	if previewReq.Font.Axes["wght"] != 400 {
		t.Fatal("editing mutated an already dispatched preview")
	}
	a.settings.Font.Axes["wght"] = 650
	a.requestPreview()
	previewReq = <-b.previews
	awaitState(t, a, func() bool { return !a.previewBusy })
	if previewReq.Font.Axes["wght"] != 650 {
		t.Fatal("axis not forwarded to preview")
	}
	a.generate()
	buildReq := <-b.requests
	awaitState(t, a, func() bool { return !a.busy })
	if buildReq.Font.Axes["wght"] != 650 {
		t.Fatal("axis not forwarded to generation")
	}
	a.settings.Font.Axes["wght"] = 700
	if buildReq.Font.Axes["wght"] != 650 {
		t.Fatal("editing mutated dispatched build")
	}
	a.save()
	loaded, err := LoadSettings(a.settingsPath)
	if err != nil || loaded.Font.Axes["wght"] != 700 {
		t.Fatalf("axes settings: %+v, %v", loaded.Font, err)
	}
	tt.Frame()
	if err := tt.Click(a.t("axes.default")); err != nil {
		t.Fatal(err)
	}
	if a.settings.Font.Axes["wght"] != 400 || a.settings.Font.Axes["wdth"] != 100 {
		t.Fatal("reset defaults failed")
	}
	if got := axisValues(face, map[string]float32{"wght": 950, "wdth": 90}); got["wght"] != 400 || got["wdth"] != 90 {
		t.Fatal("saved bounds/default handling failed")
	}
}

func TestRealVariableFontPreview(t *testing.T) {
	path := "C:/Windows/Fonts/SegUIVar.ttf"
	if _, err := os.Stat(path); err != nil {
		t.Skip("Windows variable font not present")
	}
	b := FontPreviewBackend{}
	info, err := b.InspectFont(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Faces) == 0 || len(info.Faces[0].Axes) == 0 {
		t.Fatal("variable axes not discovered")
	}
	req := PreviewRequest{Font: FontSelection{Path: path, FaceIndex: 0, Axes: axisValues(info.Faces[0], nil)}, Text: "For Super Earth! 0123456789", Width: 900, Height: 240}
	req.Font.Axes["wght"] = 300
	light, err := b.Preview(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Font.Axes["wght"] = 700
	bold, err := b.Preview(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var p1, p2 bytes.Buffer
	if err := png.Encode(&p1, light.Image); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&p2, bold.Image); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(p1.Bytes(), p2.Bytes()) {
		t.Fatal("variable axis changed no preview pixels")
	}
	if output := os.Getenv("HD2_MYGO_SCREENSHOTS"); output != "" {
		if err := os.MkdirAll(output, 0755); err != nil {
			t.Fatal(err)
		}
		savePNG(t, filepath.Join(output, "variable-weight-300.png"), light.Image)
		savePNG(t, filepath.Join(output, "variable-weight-700.png"), bold.Image)
		a := testApp(t, nil)
		a.settings.Font = req.Font
		a.fontDraft = path
		a.faces = info.Faces
		a.faceName = faceLabel(info.Faces[0])
		a.preview = ui.NewBitmap(bold.Image)
		a.settings.PreviewText = req.Text
		a.statusKey = "status.imported"
		tt := ui.NewTester(a.view, 1140, 1000)
		savePNG(t, filepath.Join(output, "variable-controls.png"), tt.Image())
	}
}
