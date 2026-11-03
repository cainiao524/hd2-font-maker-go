package geometry

import (
	"encoding/binary"
	"math"
	"slices"
)

// ContourCount is the number of contours, counted by move commands.
func ContourCount(p Path) int {
	n := 0
	for _, c := range p {
		if c.Verb == Move {
			n++
		}
	}
	return n
}

// AbsArea is the absolute signed area of p, with the same curve contribution
// the native bridge uses so a before/after ratio is stable.
func AbsArea(p Path) float64 { return math.Abs(signedArea(p)) }

func signedArea(p Path) float64 {
	var sum float64
	var start, last [2]float64
	open := false
	line := func(x, y float64) {
		sum -= (x - last[0]) * (y + last[1]) * 0.5
		last = [2]float64{x, y}
	}
	for _, c := range p {
		switch c.Verb {
		case Move:
			if open {
				line(start[0], start[1])
			}
			start = [2]float64{float64(c.Points[0]), float64(c.Points[1])}
			last = start
			open = true
		case Line:
			line(float64(c.Points[0]), float64(c.Points[1]))
		case Quad:
			x1 := float64(c.Points[0]) - last[0]
			y1 := float64(c.Points[1]) - last[1]
			x2 := float64(c.Points[2]) - last[0]
			y2 := float64(c.Points[3]) - last[1]
			sum -= (x2*y1 - x1*y2) / 3
			line(float64(c.Points[2]), float64(c.Points[3]))
		case Cubic:
			x1 := float64(c.Points[0]) - last[0]
			y1 := float64(c.Points[1]) - last[1]
			x2 := float64(c.Points[2]) - last[0]
			y2 := float64(c.Points[3]) - last[1]
			x3 := float64(c.Points[4]) - last[0]
			y3 := float64(c.Points[5]) - last[1]
			sum -= (x1*(-y2-y3) + x2*(y1-2*y3) + x3*(y1+2*y2)) * 0.15
			line(float64(c.Points[4]), float64(c.Points[5]))
		case Close:
			if open {
				line(start[0], start[1])
				open = false
			}
		}
	}
	if open {
		line(start[0], start[1])
	}
	return sum
}

// ErosionKept reports whether an eroded outline is still a usable drawing of
// before. A lost contour or an area below 40% of the original is rejected so
// the caller can keep the source glyph instead of shipping a collapsed one.
func ErosionKept(before, after Path) bool {
	if ContourCount(before) == 0 {
		return true
	}
	if ContourCount(after) < ContourCount(before) {
		return false
	}
	area := AbsArea(before)
	return area <= 0 || AbsArea(after) >= area*0.4
}

// EqualContours matches pathops' set-of-contours comparison, retaining order
// inside contours while ignoring contour order and duplicate identical contours.
func EqualContours(a, b Path) bool {
	if slices.Equal(a, b) {
		return true
	}
	keys := func(path Path) map[string]bool {
		out := map[string]bool{}
		var key []byte
		flush := func() {
			if len(key) > 0 {
				out[string(key)] = true
				key = key[:0]
			}
		}
		for _, c := range path {
			if c.Verb == Move {
				flush()
			}
			key = binary.LittleEndian.AppendUint32(key, c.Verb)
			n := 0
			switch c.Verb {
			case Move, Line:
				n = 2
			case Quad:
				n = 4
			case Cubic:
				n = 6
			}
			for _, v := range c.Points[:n] {
				if v == 0 {
					v = 0
				}
				key = binary.LittleEndian.AppendUint32(key, math.Float32bits(v))
			}
			if c.Verb == Close {
				flush()
			}
		}
		flush()
		return out
	}
	x, y := keys(a), keys(b)
	if len(x) != len(y) {
		return false
	}
	for k := range x {
		if !y[k] {
			return false
		}
	}
	return true
}
