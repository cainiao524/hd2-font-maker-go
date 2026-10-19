package otlayout

import (
	"fmt"
	"math"
	"math/bits"
)

type scaler struct {
	buffer
	ratio float64
	seen  map[int]bool
}

func (s *scaler) dimension(p int, unsigned bool) {
	if s.seen[p] || s.err != nil {
		return
	}
	s.seen[p] = true
	v := s.u16(p)
	if !unsigned {
		v = int(int16(v))
	}
	n := math.Floor(float64(v)*s.ratio + .5)
	low, high := -32768., 32767.
	if unsigned {
		low, high = 0, 65535
	}
	if n < low || n > high {
		s.err = fmt.Errorf("scaled font dimension exceeds storage")
		return
	}
	s.put16(p, int(uint16(int(n))))
}
func (s *scaler) anchor(p int) {
	if p == 0 {
		return
	}
	format := s.u16(p)
	if format < 1 || format > 3 {
		s.err = fmt.Errorf("unsupported anchor format %d", format)
		return
	}
	s.dimension(p+2, false)
	s.dimension(p+4, false)
}
func valueSize(format int) int { return 2 * bits.OnesCount(uint(format)) }
func (s *scaler) values(p, format int) int {
	if format&^255 != 0 {
		s.err = fmt.Errorf("reserved positioning value bits")
		return p
	}
	for bit := 0; bit < 8; bit++ {
		if format&(1<<bit) != 0 {
			if bit < 4 {
				s.dimension(p, false)
			}
			p += 2
		}
	}
	return p
}
func (s *scaler) anchors(p, count int) {
	if !s.check(p, count*2) {
		return
	}
	for i := 0; i < count; i++ {
		if o := s.u16(p + 2*i); o != 0 {
			s.anchor(p + o)
		}
	}
}
func (s *scaler) markArray(p int) {
	n := s.u16(p)
	if !s.check(p+2, n*4) {
		return
	}
	for i := 0; i < n; i++ {
		if o := s.u16(p + 4 + 4*i); o != 0 {
			s.anchor(p + o)
		}
	}
}
func (s *scaler) position(kind, p int, depth int) {
	if s.err != nil {
		return
	}
	if depth > 2 {
		s.err = fmt.Errorf("nested positioning extensions")
		return
	}
	format := s.u16(p)
	switch kind {
	case 1:
		vf := s.u16(p + 4)
		if format == 1 {
			s.values(p+6, vf)
		} else if format == 2 {
			n := s.u16(p + 6)
			size := valueSize(vf)
			if !s.check(p+8, n*size) {
				return
			}
			for i := 0; i < n; i++ {
				s.values(p+8+i*size, vf)
			}
		} else {
			s.err = fmt.Errorf("single positioning format")
		}
	case 2:
		f1, f2 := s.u16(p+4), s.u16(p+6)
		size := valueSize(f1) + valueSize(f2)
		if format == 1 {
			n := s.u16(p + 8)
			if !s.check(p+10, n*2) {
				return
			}
			for i := 0; i < n; i++ {
				set := p + s.u16(p+10+2*i)
				count := s.u16(set)
				if !s.check(set+2, count*(2+size)) {
					return
				}
				q := set + 2
				for j := 0; j < count; j++ {
					q = s.values(q+2, f1)
					q = s.values(q, f2)
				}
			}
		} else if format == 2 {
			n := s.u16(p+12) * s.u16(p+14)
			if size == 0 {
				return
			}
			if !s.check(p+16, n*size) {
				return
			}
			q := p + 16
			for i := 0; i < n; i++ {
				q = s.values(q, f1)
				q = s.values(q, f2)
			}
		} else {
			s.err = fmt.Errorf("pair positioning format")
		}
	case 3:
		if format != 1 {
			s.err = fmt.Errorf("cursive positioning format")
			return
		}
		n := s.u16(p + 4)
		if !s.check(p+6, n*4) {
			return
		}
		for i := 0; i < n*2; i++ {
			if o := s.u16(p + 6 + i*2); o != 0 {
				s.anchor(p + o)
			}
		}
	case 4, 5, 6:
		if format != 1 {
			s.err = fmt.Errorf("mark positioning format")
			return
		}
		classCount := s.u16(p + 6)
		s.markArray(p + s.u16(p+8))
		array := p + s.u16(p+10)
		n := s.u16(array)
		if kind == 5 {
			if !s.check(array+2, n*2) {
				return
			}
			for i := 0; i < n; i++ {
				attach := array + s.u16(array+2+2*i)
				count := s.u16(attach)
				if !s.check(attach+2, count*classCount*2) {
					return
				}
				for j := 0; j < count*classCount; j++ {
					if o := s.u16(attach + 2 + 2*j); o != 0 {
						s.anchor(attach + o)
					}
				}
			}
		} else {
			if !s.check(array+2, n*classCount*2) {
				return
			}
			for i := 0; i < n*classCount; i++ {
				if o := s.u16(array + 2 + 2*i); o != 0 {
					s.anchor(array + o)
				}
			}
		}
	case 7, 8: // Context records refer to other lookups; they hold no distances.
	case 9:
		if format != 1 {
			s.err = fmt.Errorf("positioning extension format")
			return
		}
		s.position(s.u16(p+2), p+s.u32(p+4), depth+1)
	default:
		s.err = fmt.Errorf("unsupported positioning lookup %d", kind)
	}
}
func (s *scaler) gpos() {
	list := s.u16(8)
	n := s.u16(list)
	if !s.check(list+2, 2*n) {
		return
	}
	for i := 0; i < n; i++ {
		p := list + s.u16(list+2+2*i)
		kind, count := s.u16(p), s.u16(p+4)
		if !s.check(p+6, 2*count) {
			return
		}
		for j := 0; j < count; j++ {
			s.position(kind, p+s.u16(p+6+2*j), 0)
		}
	}
}
func (s *scaler) gdef() {
	if list := s.u16(8); list != 0 {
		n := s.u16(list + 2)
		if !s.check(list+4, n*2) {
			return
		}
		for i := 0; i < n; i++ {
			glyph := list + s.u16(list+4+2*i)
			count := s.u16(glyph)
			if !s.check(glyph+2, count*2) {
				return
			}
			for j := 0; j < count; j++ {
				caret := glyph + s.u16(glyph+2+2*j)
				format := s.u16(caret)
				if format == 1 || format == 3 {
					s.dimension(caret+2, false)
				} else if format != 2 {
					s.err = fmt.Errorf("caret format")
				}
			}
		}
	}
}
func (s *scaler) baseCoord(p int) {
	if p == 0 {
		return
	}
	f := s.u16(p)
	if f < 1 || f > 3 {
		s.err = fmt.Errorf("BASE coordinate format")
		return
	}
	s.dimension(p+2, false)
}
func (s *scaler) minMax(p int) {
	for _, a := range []int{0, 2} {
		if o := s.u16(p + a); o != 0 {
			s.baseCoord(p + o)
		}
	}
	n := s.u16(p + 4)
	if !s.check(p+6, n*8) {
		return
	}
	for i := 0; i < n; i++ {
		for _, a := range []int{4, 6} {
			if o := s.u16(p + 6 + i*8 + a); o != 0 {
				s.baseCoord(p + o)
			}
		}
	}
}
func (s *scaler) base() {
	for _, a := range []int{4, 6} {
		axis := s.u16(a)
		if axis == 0 {
			continue
		}
		o := s.u16(axis + 2)
		if o == 0 {
			continue
		}
		list := axis + o
		n := s.u16(list)
		if !s.check(list+2, 6*n) {
			return
		}
		for i := 0; i < n; i++ {
			script := list + s.u16(list+6+6*i)
			if o := s.u16(script); o != 0 {
				values := script + o
				count := s.u16(values + 2)
				if !s.check(values+4, count*2) {
					return
				}
				for j := 0; j < count; j++ {
					s.baseCoord(values + s.u16(values+4+2*j))
				}
			}
			if o := s.u16(script + 2); o != 0 {
				s.minMax(script + o)
			}
			count := s.u16(script + 4)
			if !s.check(script+6, count*6) {
				return
			}
			for j := 0; j < count; j++ {
				s.minMax(script + s.u16(script+10+6*j))
			}
		}
	}
}
func (s *scaler) mathValues(p, count int) {
	if !s.check(p, 4*count) {
		return
	}
	for i := 0; i < count; i++ {
		s.dimension(p+4*i, false)
	}
}
func (s *scaler) mathTable() {
	if p := s.u16(4); p != 0 {
		s.dimension(p+4, true)
		s.dimension(p+6, true)
		s.mathValues(p+8, 51)
	}
	if p := s.u16(6); p != 0 {
		for _, a := range []int{0, 2} {
			if o := s.u16(p + a); o != 0 {
				q := p + o
				s.mathValues(q+4, s.u16(q+2))
			}
		}
		if o := s.u16(p + 6); o != 0 {
			q := p + o
			n := s.u16(q + 2)
			if !s.check(q+4, n*8) {
				return
			}
			for i := 0; i < n*4; i++ {
				if o := s.u16(q + 4 + 2*i); o != 0 {
					k := q + o
					heights := s.u16(k)
					s.mathValues(k+2, heights*2+1)
				}
			}
		}
	}
	if p := s.u16(8); p != 0 {
		s.dimension(p, true)
		n := s.u16(p+6) + s.u16(p+8)
		if !s.check(p+10, 2*n) {
			return
		}
		for i := 0; i < n; i++ {
			q := p + s.u16(p+10+2*i)
			count := s.u16(q + 2)
			if !s.check(q+4, 4*count) {
				return
			}
			for j := 0; j < count; j++ {
				s.dimension(q+6+4*j, true)
			}
			if o := s.u16(q); o != 0 {
				a := q + o
				s.dimension(a, false)
				parts := s.u16(a + 4)
				if !s.check(a+6, parts*10) {
					return
				}
				for j := 0; j < parts; j++ {
					for _, k := range []int{2, 4, 6} {
						s.dimension(a+6+j*10+k, true)
					}
				}
			}
		}
	}
}

// Scale changes dimensional fields only. Glyph IDs, flags, device deltas,
// coverage, lookup order and all relative pointers remain byte-for-byte intact.
func Scale(tag string, data []byte, ratio float64) ([]byte, error) {
	if ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return nil, fmt.Errorf("invalid font scale")
	}
	s := scaler{buffer: buffer{data: append([]byte(nil), data...)}, ratio: ratio, seen: map[int]bool{}}
	switch tag {
	case "GPOS":
		s.gpos()
	case "GDEF":
		s.gdef()
	case "BASE":
		s.base()
	case "MATH":
		s.mathTable()
	default:
		return nil, fmt.Errorf("unsupported dimensional layout table %q", tag)
	}
	return s.data, s.err
}
