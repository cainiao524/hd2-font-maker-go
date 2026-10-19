package geometry

import (
	"encoding/binary"
	"math"
	"slices"
)

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
