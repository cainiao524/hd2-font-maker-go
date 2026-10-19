package quadratic

import (
	"fmt"
	"hd2font/native/internal/geometry"
	"hd2font/native/internal/sfntio"
	"slices"
)

// Glyph implements Cu2QuPen -> TTGlyphPen without an intervening float32 path.
// Every spline control is rounded exactly once, when glyf is serialized.
func Glyph(path geometry.Path, tolerance float64, reverse bool) (sfntio.Glyph, error) {
	var contours [][]sfntio.OutlinePoint
	var contour []sfntio.OutlinePoint
	var current [2]float64
	flush := func() {
		if len(contour) == 0 {
			return
		}
		if len(contour) > 1 && contour[0] == contour[len(contour)-1] {
			contour = contour[:len(contour)-1]
		}
		if reverse && len(contour) > 1 {
			slices.Reverse(contour[1:])
		}
		contours = append(contours, contour)
		contour = nil
	}
	add := func(x, y float64, on bool) { contour = append(contour, sfntio.OutlinePoint{X: x, Y: y, OnCurve: on}) }
	for _, c := range path {
		switch c.Verb {
		case geometry.Move:
			flush()
			current = [2]float64{float64(c.Points[0]), float64(c.Points[1])}
			add(current[0], current[1], true)
		case geometry.Line:
			current = [2]float64{float64(c.Points[0]), float64(c.Points[1])}
			add(current[0], current[1], true)
		case geometry.Quad:
			add(float64(c.Points[0]), float64(c.Points[1]), false)
			current = [2]float64{float64(c.Points[2]), float64(c.Points[3])}
			add(current[0], current[1], true)
		case geometry.Cubic:
			pts := [4][2]float64{current, {float64(c.Points[0]), float64(c.Points[1])}, {float64(c.Points[2]), float64(c.Points[3])}, {float64(c.Points[4]), float64(c.Points[5])}}
			spline, err := Curve(pts, tolerance)
			if err != nil {
				return sfntio.Glyph{}, err
			}
			for i := 1; i < len(spline); i++ {
				add(spline[i][0], spline[i][1], i == len(spline)-1)
			}
			current = pts[3]
		case geometry.Close:
			flush()
		default:
			return sfntio.Glyph{}, fmt.Errorf("invalid quadratic path verb")
		}
	}
	flush()
	return sfntio.EncodeContours(contours)
}
