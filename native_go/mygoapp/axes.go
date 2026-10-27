package mygoapp

import (
	"fmt"
	"math"
	"os"
	"time"

	"github.com/egoist/mygo/ui"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"
)

const Version = "2.1.2"

func readFontAxes(path string) ([][]AxisInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	loaders, err := ot.NewLoaders(f)
	if err != nil {
		return nil, err
	}
	faces := make([][]AxisInfo, len(loaders))
	for i, loader := range loaders {
		if !loader.HasTable(ot.MustNewTag("fvar")) {
			continue
		}
		raw, err := loader.RawTable(ot.MustNewTag("fvar"))
		if err != nil {
			return nil, err
		}
		fvar, _, err := tables.ParseFvar(raw)
		if err != nil {
			return nil, err
		}
		for _, axis := range fvar.Axis {
			faces[i] = append(faces[i], AxisInfo{Tag: axis.Tag.String(), Name: axis.Tag.String(), Min: axis.Minimum, Default: axis.Default, Max: axis.Maximum})
		}
	}
	return faces, nil
}

func axisValues(face FontFace, saved map[string]float32) map[string]float32 {
	if len(face.Axes) == 0 {
		return nil
	}
	values := make(map[string]float32, len(face.Axes))
	for _, axis := range face.Axes {
		value, ok := saved[axis.Tag]
		if !ok || math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || value < axis.Min || value > axis.Max {
			value = axis.Default
		}
		values[axis.Tag] = value
	}
	return values
}

// selectFace starts the selection from another face of the imported file,
// with that face's family.
func (a *application) selectFace(face FontFace) {
	path := a.facesPath
	if path == "" {
		path = a.settings.Font.Path
	}
	a.applyFamily(familyOf(path, a.faces, a.siblings, face))
	a.settings.Font.Axes = nil
	if a.settings.Font.Path == path && a.settings.Font.FaceIndex == face.Index {
		a.settings.Font.Axes = axisValues(face, nil)
	}
	a.faceName = faceLabel(face)
	a.fontDraft = a.settings.Font.Path
	a.updateModName()
	a.changed(true)
}

func (a *application) axesView(c *ui.Context) {
	for _, face := range a.faces {
		if !a.isSource(face) || len(face.Axes) == 0 {
			continue
		}
		ui.Column(c.Key("axes")).Gap(8).Children(func() {
			ui.Text(c, a.t("dialog.axes")).Bold()
			for _, axis := range face.Axes {
				label := a.t("axis." + axis.Tag)
				if label == "axis."+axis.Tag {
					label = axis.Name
					if label == "" {
						label = axis.Tag
					}
				}
				value := float64(a.settings.Font.Axes[axis.Tag])
				step := float64(axis.Step)
				if step <= 0 {
					step = 1
					if axis.Max-axis.Min < 20 || math.Trunc(float64(axis.Default)) != float64(axis.Default) {
						step = 0.01
					}
				}
				ui.Row(c.Key(axis.Tag)).Gap(12).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
						ui.Text(c, label)
						ui.Text(c, a.t("axes.range", "min", fmt.Sprint(axis.Min), "max", fmt.Sprint(axis.Max))).FontSize(12).TextColor(c.Theme().TextMuted)
					})
					if ui.NumberInput(c.Key("value"), &value, float64(axis.Min), float64(axis.Max), step).Width(150).Label(label).Disabled(a.busy).Changed() {
						a.settings.Font.Axes[axis.Tag] = float32(value)
						a.previewDirtyAt = time.Now()
						a.changed(false)
					}
				})
			}
			ui.Button(c.Key("axis-defaults"), a.t("axes.default")).Disabled(a.busy).OnClick(func() { a.settings.Font.Axes = axisValues(face, nil); a.changed(true) })
		})
	}
}
