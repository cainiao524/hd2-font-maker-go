package sfntio

import (
	"encoding/binary"
	"fmt"
	"math"
)

// ScaleGlyphData keeps point order, composite references and hint bytecode.
// Only stored coordinates and component translations change font units.
func ScaleGlyphData(data []byte, ratio float64) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return nil, fmt.Errorf("invalid glyph scale")
	}
	r := glyphReader{data: data}
	header, err := r.take(10)
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), header...)
	be := binary.BigEndian
	scale := func(v int16) (int16, error) {
		n := math.Floor(float64(v)*ratio + .5)
		if n < math.MinInt16 || n > math.MaxInt16 {
			return 0, fmt.Errorf("scaled glyph exceeds int16")
		}
		return int16(n), nil
	}
	for p := 2; p < 10; p += 2 {
		v, err := scale(int16(be.Uint16(out[p:])))
		if err != nil {
			return nil, err
		}
		be.PutUint16(out[p:], uint16(v))
	}
	contours := int(int16(be.Uint16(header)))
	if contours < 0 {
		for {
			row, err := r.take(4)
			if err != nil {
				return nil, err
			}
			flags, gid := be.Uint16(row), be.Uint16(row[2:])
			words, xy := flags&1 != 0, flags&2 != 0
			size := 2
			if words {
				size = 4
			}
			args, err := r.take(size)
			if err != nil {
				return nil, err
			}
			a, b := 0, 0
			if words {
				a, b = int(be.Uint16(args)), int(be.Uint16(args[2:]))
			} else {
				a, b = int(args[0]), int(args[1])
			}
			if xy {
				if words {
					a, b = int(int16(a)), int(int16(b))
				} else {
					a, b = int(int8(a)), int(int8(b))
				}
				x, err := scale(int16(a))
				if err != nil {
					return nil, err
				}
				y, err := scale(int16(b))
				if err != nil {
					return nil, err
				}
				a, b = int(x), int(y)
				if a < -128 || a > 127 || b < -128 || b > 127 {
					words = true
					flags |= 1
				}
			}
			out = be.AppendUint16(out, flags)
			out = be.AppendUint16(out, gid)
			if words {
				out = be.AppendUint16(out, uint16(a))
				out = be.AppendUint16(out, uint16(b))
			} else {
				out = append(out, byte(a), byte(b))
			}
			transforms := 0
			if flags&8 != 0 {
				transforms = 2
			} else if flags&64 != 0 {
				transforms = 4
			} else if flags&128 != 0 {
				transforms = 8
			}
			matrix, err := r.take(transforms)
			if err != nil {
				return nil, err
			}
			out = append(out, matrix...)
			if flags&32 == 0 {
				break
			}
		}
		return append(out, data[r.at:]...), nil
	}
	ends, err := r.take(2 * contours)
	if err != nil {
		return nil, err
	}
	out = append(out, ends...)
	if contours == 0 && r.at == len(data) {
		return out, nil
	}
	length, err := r.take(2)
	if err != nil {
		return nil, err
	}
	instructions, err := r.take(int(be.Uint16(length)))
	if err != nil {
		return nil, err
	}
	out = append(out, length...)
	out = append(out, instructions...)
	if contours == 0 {
		return out, nil
	}
	n := int(be.Uint16(ends[len(ends)-2:])) + 1
	flags := make([]byte, n)
	for i := 0; i < n; {
		flag, err := r.take(1)
		if err != nil {
			return nil, err
		}
		repeat := 1
		if flag[0]&8 != 0 {
			value, err := r.take(1)
			if err != nil {
				return nil, err
			}
			repeat += int(value[0])
		}
		if repeat > n-i {
			return nil, fmt.Errorf("glyph flag repeat overflow")
		}
		for j := 0; j < repeat; j++ {
			flags[i+j] = flag[0]
		}
		i += repeat
	}
	xs, ys := make([]int16, n), make([]int16, n)
	var previous int32
	for i, flag := range flags {
		value, err := r.coordinate(flag, 2, 16, previous)
		if err != nil {
			return nil, err
		}
		previous = int32(value)
		xs[i], err = scale(value)
		if err != nil {
			return nil, err
		}
	}
	previous = 0
	for i, flag := range flags {
		value, err := r.coordinate(flag, 4, 32, previous)
		if err != nil {
			return nil, err
		}
		previous = int32(value)
		ys[i], err = scale(value)
		if err != nil {
			return nil, err
		}
	}
	var xdata, ydata []byte
	px, py := int16(0), int16(0)
	for i := range flags {
		xf, err := coordinateDelta(xs[i], px, 2, 16, &xdata)
		if err != nil {
			return nil, err
		}
		yf, err := coordinateDelta(ys[i], py, 4, 32, &ydata)
		if err != nil {
			return nil, err
		}
		flags[i] = (flags[i] & 0xc1) | xf | yf
		px, py = xs[i], ys[i]
	}
	minx, maxx, miny, maxy := xs[0], xs[0], ys[0], ys[0]
	for i := range xs {
		minx = min(minx, xs[i])
		maxx = max(maxx, xs[i])
		miny = min(miny, ys[i])
		maxy = max(maxy, ys[i])
	}
	for i, v := range []int16{minx, miny, maxx, maxy} {
		be.PutUint16(out[2+2*i:], uint16(v))
	}
	out = append(out, flags...)
	out = append(out, xdata...)
	return append(out, ydata...), nil
}
