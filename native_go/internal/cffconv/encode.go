// Package cffconv converts prepared geometry to Type 2 charstrings and writes
// standalone name-keyed CFF1 table bytes. It does not merge or rescale fonts.
package cffconv

import (
	"fmt"
	"math"

	"hd2font/native/internal/geometry"
)

const maxCharString = 65535

type point struct{ x, y float64 }

// Encode encodes one unhinted Type 2 glyph, including its explicit advance width.
// reverse reverses each closed contour while preserving its start point, as in
// cff_conversion.py. A Move or end of input implicitly closes the prior contour.
// Quadratics become exact cubics before absolute points are rounded using
// floor(x+.5); relative operands are then differences of those rounded points.
// The caller supplies decomposed, already prepared coordinates. Widths and
// relative operands outside the signed 16-bit Type 2 literal range are errors.
func Encode(path geometry.Path, advance uint16, reverse bool) ([]byte, error) {
	if advance > math.MaxInt16 {
		return nil, fmt.Errorf("advance %d exceeds the Type 2 literal range with nominalWidthX=0", advance)
	}
	e := encoder{data: make([]byte, 0, min(len(path), maxCharString/9)*9+4)}
	e.data = appendInt(e.data, int32(advance))
	for start := 0; start < len(path); {
		if path[start].Verb != geometry.Move {
			return nil, fmt.Errorf("command %d: contour must begin with Move", start)
		}
		end := start + 1
		for end < len(path) && path[end].Verb != geometry.Move && path[end].Verb != geometry.Close {
			end++
		}
		contour := path[start:end]
		for i, cmd := range contour {
			count := 0
			switch cmd.Verb {
			case geometry.Move, geometry.Line:
				count = 2
			case geometry.Quad:
				count = 4
			case geometry.Cubic:
				count = 6
			default:
				return nil, fmt.Errorf("command %d: unsupported geometry verb %d", start+i, cmd.Verb)
			}
			for _, v := range cmd.Points[:count] {
				if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
					return nil, fmt.Errorf("command %d: non-finite coordinate", start+i)
				}
			}
		}
		e.contour(contour, reverse)
		if e.err != nil {
			return nil, e.err
		}
		start = end
		if start < len(path) && path[start].Verb == geometry.Close {
			start++
		}
	}
	e.data = append(e.data, 14) // endchar closes the final contour.
	if len(e.data) > maxCharString {
		return nil, fmt.Errorf("Type 2 charstring exceeds %d bytes", maxCharString)
	}
	return e.data, nil
}

type encoder struct {
	data []byte
	x, y int64
	err  error
}

func endpoint(c geometry.Command) point {
	i := 0
	if c.Verb == geometry.Quad {
		i = 2
	} else if c.Verb == geometry.Cubic {
		i = 4
	}
	return point{float64(c.Points[i]), float64(c.Points[i+1])}
}

func (e *encoder) contour(c geometry.Path, reverse bool) {
	first := endpoint(c[0])
	e.move(first)
	if !reverse {
		for i := 1; i < len(c) && e.err == nil; i++ {
			e.segment(c[i], endpoint(c[i-1]), false)
		}
		return
	}
	last := endpoint(c[len(c)-1])
	if last != first {
		e.line(last)
	}
	for i := len(c) - 1; i > 0 && e.err == nil; i-- {
		// ReverseContourPen leaves the reversed first line implicit in closePath,
		// except for a duplicate point. Closing curves must always be emitted.
		if i == 1 && c[i].Verb == geometry.Line && endpoint(c[i]) != first {
			break
		}
		e.segment(c[i], endpoint(c[i-1]), true)
	}
}

func (e *encoder) segment(c geometry.Command, from point, reverse bool) {
	to := endpoint(c)
	if c.Verb == geometry.Line {
		if reverse {
			to = from
		}
		e.line(to)
		return
	}
	p := c.Points
	c1 := point{float64(p[0]), float64(p[1])}
	c2 := point{float64(p[2]), float64(p[3])}
	if c.Verb == geometry.Quad {
		// Keep the unrounded quadratic start/control/end for degree elevation.
		control := c1
		c1 = point{from.x + (2.0/3)*(control.x-from.x), from.y + (2.0/3)*(control.y-from.y)}
		c2 = point{to.x + (2.0/3)*(control.x-to.x), to.y + (2.0/3)*(control.y-to.y)}
	}
	if reverse {
		c1, c2, to = c2, c1, from
	}
	e.relative(c1)
	e.relative(c2)
	e.relative(to)
	e.op(8) // rrcurveto, six operands.
}

func (e *encoder) move(p point) { e.relative(p); e.op(21) }
func (e *encoder) line(p point) { e.relative(p); e.op(5) }

func (e *encoder) relative(p point) {
	if e.err != nil {
		return
	}
	x, y := math.Floor(p.x+0.5), math.Floor(p.y+0.5)
	// Bounding absolute integers also makes subtraction/conversion well-defined.
	if x < math.MinInt32 || x > math.MaxInt32 || y < math.MinInt32 || y > math.MaxInt32 {
		e.err = fmt.Errorf("rounded coordinate exceeds the supported integer range")
		return
	}
	dx, dy := int64(x)-e.x, int64(y)-e.y
	if dx < math.MinInt16 || dx > math.MaxInt16 || dy < math.MinInt16 || dy > math.MaxInt16 {
		e.err = fmt.Errorf("relative coordinate (%d,%d) exceeds the Type 2 literal range", dx, dy)
		return
	}
	e.data = appendInt(e.data, int32(dx))
	e.data = appendInt(e.data, int32(dy))
	e.x, e.y = int64(x), int64(y)
}

func (e *encoder) op(op byte) {
	if e.err != nil {
		return
	}
	e.data = append(e.data, op)
	if len(e.data) >= maxCharString {
		e.err = fmt.Errorf("Type 2 charstring exceeds %d bytes", maxCharString)
	}
}

// appendInt handles signed 16-bit literals, shared by Type 2 and CFF DICT.
func appendInt(dst []byte, value int32) []byte {
	switch {
	case value >= -107 && value <= 107:
		return append(dst, byte(value+139))
	case value >= 108 && value <= 1131:
		v := value - 108
		return append(dst, byte(247+v/256), byte(v))
	case value >= -1131 && value <= -108:
		v := -value - 108
		return append(dst, byte(251+v/256), byte(v))
	default:
		return append(dst, 28, byte(value>>8), byte(value))
	}
}
