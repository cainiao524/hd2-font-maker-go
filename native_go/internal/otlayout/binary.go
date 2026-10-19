// Package otlayout preserves OpenType positioning and substitution programs
// while composing disjoint glyph spaces or changing font units.
package otlayout

import (
	"encoding/binary"
	"fmt"
	"math"
)

type buffer struct {
	data []byte
	err  error
}

func (b *buffer) check(p, n int) bool {
	if b.err != nil {
		return false
	}
	if p < 0 || n < 0 || p > len(b.data)-n {
		b.err = fmt.Errorf("OpenType range %d+%d exceeds %d bytes", p, n, len(b.data))
		return false
	}
	return true
}
func (b *buffer) u16(p int) int {
	if !b.check(p, 2) {
		return 0
	}
	return int(binary.BigEndian.Uint16(b.data[p:]))
}
func (b *buffer) u32(p int) int {
	if !b.check(p, 4) {
		return 0
	}
	return int(binary.BigEndian.Uint32(b.data[p:]))
}
func (b *buffer) put16(p, n int) {
	if n < 0 || n > 65535 {
		b.err = fmt.Errorf("OpenType uint16 overflow: %d", n)
		return
	}
	if b.check(p, 2) {
		binary.BigEndian.PutUint16(b.data[p:], uint16(n))
	}
}
func (b *buffer) put32(p, n int) {
	if n < 0 || uint64(n) > math.MaxUint32 {
		b.err = fmt.Errorf("OpenType uint32 overflow")
		return
	}
	if b.check(p, 4) {
		binary.BigEndian.PutUint32(b.data[p:], uint32(n))
	}
}
func (b *buffer) tag(p int) string {
	if !b.check(p, 4) {
		return ""
	}
	return string(b.data[p : p+4])
}
func put16(b []byte, p, n int)        { binary.BigEndian.PutUint16(b[p:], uint16(n)) }
func put32(b []byte, p, n int)        { binary.BigEndian.PutUint32(b[p:], uint32(n)) }
func append16(b []byte, n int) []byte { return binary.BigEndian.AppendUint16(b, uint16(n)) }

func coverage(b *buffer, p int) []uint16 {
	var result []uint16
	switch b.u16(p) {
	case 1:
		n := b.u16(p + 2)
		if !b.check(p+4, n*2) {
			return nil
		}
		for i := 0; i < n; i++ {
			result = append(result, uint16(b.u16(p+4+2*i)))
		}
	case 2:
		n := b.u16(p + 2)
		if !b.check(p+4, n*6) {
			return nil
		}
		for i := 0; i < n; i++ {
			q := p + 4 + 6*i
			a, z, index := b.u16(q), b.u16(q+2), b.u16(q+4)
			if z < a || index != len(result) {
				b.err = fmt.Errorf("invalid coverage range")
				return nil
			}
			for g := a; g <= z; g++ {
				result = append(result, uint16(g))
			}
		}
	default:
		b.err = fmt.Errorf("unsupported coverage format")
	}
	return result
}
func classes(b *buffer, p int) map[uint16]uint16 {
	result := map[uint16]uint16{}
	if p == 0 {
		return result
	}
	switch b.u16(p) {
	case 1:
		first, n := b.u16(p+2), b.u16(p+4)
		if !b.check(p+6, n*2) || first+n > 65536 {
			return nil
		}
		for i := 0; i < n; i++ {
			v := b.u16(p + 6 + 2*i)
			if v != 0 {
				result[uint16(first+i)] = uint16(v)
			}
		}
	case 2:
		n := b.u16(p + 2)
		if !b.check(p+4, n*6) {
			return nil
		}
		for i := 0; i < n; i++ {
			q := p + 4 + 6*i
			first, last, v := b.u16(q), b.u16(q+2), b.u16(q+4)
			if first > last {
				b.err = fmt.Errorf("invalid class range")
				return nil
			}
			for g := first; g <= last; g++ {
				if v != 0 {
					result[uint16(g)] = uint16(v)
				}
			}
		}
	default:
		b.err = fmt.Errorf("unsupported class definition")
	}
	return result
}
