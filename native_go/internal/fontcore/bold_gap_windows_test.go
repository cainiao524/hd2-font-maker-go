//go:build windows && amd64

package fontcore

import (
	"testing"

	"hd2font/native/internal/geometry"
	"hd2font/native/internal/inventory"
)

func TestSyntheticBoldRetainsNarrowStrokeGap(t *testing.T) {
	p := processor(t)
	var strokes geometry.Path
	for _, x := range []float64{0, 60} {
		strokes.MoveTo(x, 0)
		strokes.LineTo(x, 500)
		strokes.LineTo(x+30, 500)
		strokes.LineTo(x+30, 0)
		strokes.Close()
	}
	previousArea := geometry.AbsArea(strokes)
	for _, weight := range []int{500, 600, 700} {
		radius := inventory.EmphasisRadius(1024, weight, false)
		out, err := p.Geometry.Process(strokes, geometry.Options{Radius: float32(radius), Clockwise: true})
		if err != nil {
			t.Fatal(err)
		}
		area := geometry.AbsArea(out)
		if geometry.ContourCount(out) != 2 || area <= previousArea {
			t.Fatalf("weight %d must add ink without joining the strokes: contours=%d area=%v", weight, geometry.ContourCount(out), area)
		}
		previousArea = area
	}
	old, err := p.Geometry.Process(strokes, geometry.Options{Radius: 23, Clockwise: true})
	if err != nil || geometry.ContourCount(old) != 1 {
		t.Fatalf("fixture must reproduce the former closed gap: %v", err)
	}
}
