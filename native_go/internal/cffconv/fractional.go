package cffconv

import (
	"encoding/binary"
	"fmt"
	"math"

	"hd2font/native/internal/geometry"
)

// EncodeFractional matches the non-rounding pen used by winding normalization.
// Final destination conversion still uses Encode's absolute integer grid.
func EncodeFractional(path geometry.Path, advance uint16) ([]byte, error) {
	if advance > 32767 {
		return nil, fmt.Errorf("CFF advance exceeds literal range")
	}
	data := appendInt(nil, int32(advance))
	x, y := 0., 0.
	var err error
	number := func(v float64) {
		if err != nil {
			return
		}
		fixed := math.Floor(v*65536 + .5)
		if math.IsNaN(v) || math.IsInf(v, 0) || fixed < math.MinInt32 || fixed > math.MaxInt32 {
			err = fmt.Errorf("CFF fixed operand exceeds range")
			return
		}
		if v == math.Trunc(v) {
			data = appendInt(data, int32(v))
		} else {
			data = append(data, 255)
			data = binary.BigEndian.AppendUint32(data, uint32(int32(fixed)))
		}
	}
	curve := func(x1, y1, x2, y2, x3, y3 float64) {
		number(x1 - x)
		number(y1 - y)
		number(x2 - x1)
		number(y2 - y1)
		number(x3 - x2)
		number(y3 - y2)
		data = append(data, 8)
		x, y = x3, y3
	}
	for _, c := range path {
		switch c.Verb {
		case geometry.Move, geometry.Line:
			nx, ny := float64(c.Points[0]), float64(c.Points[1])
			number(nx - x)
			number(ny - y)
			op := byte(5)
			if c.Verb == geometry.Move {
				op = 21
			}
			data = append(data, op)
			x, y = nx, ny
		case geometry.Quad:
			q1, q2, ex, ey := float64(c.Points[0]), float64(c.Points[1]), float64(c.Points[2]), float64(c.Points[3])
			curve(x+(q1-x)*2/3, y+(q2-y)*2/3, ex+(q1-ex)*2/3, ey+(q2-ey)*2/3, ex, ey)
		case geometry.Cubic:
			curve(float64(c.Points[0]), float64(c.Points[1]), float64(c.Points[2]), float64(c.Points[3]), float64(c.Points[4]), float64(c.Points[5]))
		case geometry.Close:
		default:
			return nil, fmt.Errorf("invalid CFF path verb")
		}
	}
	data = append(data, 14)
	if len(data) > maxCharString {
		return nil, fmt.Errorf("CFF charstring is too long")
	}
	return data, err
}
