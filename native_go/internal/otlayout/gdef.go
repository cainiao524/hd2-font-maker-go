package otlayout

import (
	"fmt"
	"sort"
)

type caret struct {
	format, value int
	device        []byte
}

func glyphKeys[V any](m map[uint16]V) []uint16 {
	keys := make([]uint16, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
func encodeCoverage(keys []uint16) []byte {
	data := []byte{0, 1}
	data = append16(data, len(keys))
	for _, k := range keys {
		data = append16(data, int(k))
	}
	return data
}
func encodeClasses(values map[uint16]uint16) []byte {
	keys := glyphKeys(values)
	data := []byte{0, 2, 0, 0}
	count := 0
	for i := 0; i < len(keys); {
		first, last, value := keys[i], keys[i], values[keys[i]]
		i++
		for i < len(keys) && int(keys[i]) == int(last)+1 && values[keys[i]] == value {
			last = keys[i]
			i++
		}
		data = append16(data, int(first))
		data = append16(data, int(last))
		data = append16(data, int(value))
		count++
	}
	put16(data, 2, count)
	return data
}
func deviceBytes(b *buffer, p int) []byte {
	first, last, format := b.u16(p), b.u16(p+2), b.u16(p+4)
	if format == 0x8000 {
		b.err = fmt.Errorf("instance GDEF variation devices before merge")
		return nil
	}
	if last < first || format < 1 || format > 3 {
		b.err = fmt.Errorf("invalid device table")
		return nil
	}
	n := 6 + 2*((last-first+1)*(1<<format)+15)/16
	// The ceiling applies to the number of packed uint16 words.
	n = 6 + 2*(((last-first+1)*(1<<format)+15)/16)
	if !b.check(p, n) {
		return nil
	}
	return append([]byte(nil), b.data[p:p+n]...)
}
func mergeGDEF(parts []Part) ([]byte, []int, []int, error) {
	markBases, classBases := make([]int, len(parts)), make([]int, len(parts))
	glyphClasses, attachClasses := map[uint16]uint16{}, map[uint16]uint16{}
	attachments := map[uint16][]uint16{}
	carets := map[uint16][]caret{}
	var sets [][]uint16
	classBase := 0
	present := false
	for i, part := range parts {
		markBases[i], classBases[i] = len(sets), classBase
		if len(part.GDEF) == 0 {
			continue
		}
		present = true
		b := buffer{data: part.GDEF}
		v := b.u32(0)
		if v != 0x10000 && v != 0x10002 && v != 0x10003 {
			return nil, nil, nil, fmt.Errorf("unsupported GDEF version")
		}
		if v == 0x10003 && b.u32(14) != 0 {
			return nil, nil, nil, fmt.Errorf("instance GDEF variation store before merge")
		}
		for g, c := range classes(&b, b.u16(4)) {
			if _, ok := glyphClasses[g]; !ok {
				glyphClasses[g] = c
			}
		}
		maxClass := 0
		for g, c := range classes(&b, b.u16(10)) {
			maxClass = max(maxClass, int(c))
			if _, ok := attachClasses[g]; !ok {
				attachClasses[g] = uint16(int(c) + classBase)
			}
		}
		classBase += maxClass
		if classBase > 255 {
			return nil, nil, nil, fmt.Errorf("too many attachment classes")
		}
		if p := b.u16(6); p != 0 {
			keys := coverage(&b, p+b.u16(p))
			n := b.u16(p + 2)
			if len(keys) != n {
				return nil, nil, nil, fmt.Errorf("GDEF attachment coverage count")
			}
			if !b.check(p+4, n*2) {
				return nil, nil, nil, b.err
			}
			for j, g := range keys {
				q := p + b.u16(p+4+2*j)
				n := b.u16(q)
				if !b.check(q+2, 2*n) {
					return nil, nil, nil, b.err
				}
				points := make([]uint16, n)
				for k := range points {
					points[k] = uint16(b.u16(q + 2 + 2*k))
				}
				if _, ok := attachments[g]; !ok {
					attachments[g] = points
				}
			}
		}
		if p := b.u16(8); p != 0 {
			keys := coverage(&b, p+b.u16(p))
			n := b.u16(p + 2)
			if len(keys) != n {
				return nil, nil, nil, fmt.Errorf("GDEF caret coverage count")
			}
			if !b.check(p+4, n*2) {
				return nil, nil, nil, b.err
			}
			for j, g := range keys {
				q := p + b.u16(p+4+2*j)
				n := b.u16(q)
				if !b.check(q+2, n*2) {
					return nil, nil, nil, b.err
				}
				rows := make([]caret, n)
				for k := range rows {
					at := q + b.u16(q+2+2*k)
					c := caret{format: b.u16(at), value: b.u16(at + 2)}
					if c.format == 3 {
						if o := b.u16(at + 4); o != 0 {
							c.device = deviceBytes(&b, at+o)
						}
					} else if c.format != 1 && c.format != 2 {
						return nil, nil, nil, fmt.Errorf("unsupported ligature caret")
					}
					rows[k] = c
				}
				if _, ok := carets[g]; !ok {
					carets[g] = rows
				}
			}
		}
		if v >= 0x10002 {
			if p := b.u16(12); p != 0 {
				if b.u16(p) != 1 {
					return nil, nil, nil, fmt.Errorf("unsupported mark set format")
				}
				n := b.u16(p + 2)
				if !b.check(p+4, n*4) {
					return nil, nil, nil, b.err
				}
				for j := 0; j < n; j++ {
					sets = append(sets, coverage(&b, p+b.u32(p+4+j*4)))
				}
			}
		}
		if b.err != nil {
			return nil, nil, nil, b.err
		}
	}
	if !present {
		return nil, markBases, classBases, nil
	}
	result := make([]byte, 14)
	put32(result, 0, 0x10002)
	add := func(field int, data []byte) error {
		if len(result) > 65535 {
			return fmt.Errorf("GDEF offset exceeds 16 bits")
		}
		put16(result, field, len(result))
		result = append(result, data...)
		return nil
	}
	if len(glyphClasses) > 0 {
		if err := add(4, encodeClasses(glyphClasses)); err != nil {
			return nil, nil, nil, err
		}
	}
	if len(attachClasses) > 0 {
		if err := add(10, encodeClasses(attachClasses)); err != nil {
			return nil, nil, nil, err
		}
	}
	if len(attachments) > 0 {
		keys := glyphKeys(attachments)
		data := make([]byte, 4+2*len(keys))
		put16(data, 2, len(keys))
		for i, g := range keys {
			if len(data) > 65535 {
				return nil, nil, nil, fmt.Errorf("attachment offset overflow")
			}
			put16(data, 4+2*i, len(data))
			data = append16(data, len(attachments[g]))
			for _, v := range attachments[g] {
				data = append16(data, int(v))
			}
		}
		if len(data) > 65535 {
			return nil, nil, nil, fmt.Errorf("attachment coverage offset overflow")
		}
		put16(data, 0, len(data))
		data = append(data, encodeCoverage(keys)...)
		if err := add(6, data); err != nil {
			return nil, nil, nil, err
		}
	}
	if len(carets) > 0 {
		keys := glyphKeys(carets)
		data := make([]byte, 4+2*len(keys))
		put16(data, 2, len(keys))
		for i, g := range keys {
			if len(data) > 65535 {
				return nil, nil, nil, fmt.Errorf("ligature offset overflow")
			}
			base := len(data)
			put16(data, 4+2*i, base)
			rows := carets[g]
			data = append16(data, len(rows))
			data = append(data, make([]byte, 2*len(rows))...)
			for j, c := range rows {
				if len(data)-base > 65535 {
					return nil, nil, nil, fmt.Errorf("caret offset overflow")
				}
				put16(data, base+2+2*j, len(data)-base)
				data = append16(data, c.format)
				data = append16(data, c.value)
				if c.format == 3 {
					offset := 0
					if len(c.device) > 0 {
						offset = 6
					}
					data = append16(data, offset)
					data = append(data, c.device...)
				}
			}
		}
		if len(data) > 65535 {
			return nil, nil, nil, fmt.Errorf("caret coverage offset overflow")
		}
		put16(data, 0, len(data))
		data = append(data, encodeCoverage(keys)...)
		if err := add(8, data); err != nil {
			return nil, nil, nil, err
		}
	}
	if len(sets) > 0 {
		data := make([]byte, 4+4*len(sets))
		put16(data, 0, 1)
		put16(data, 2, len(sets))
		for i, set := range sets {
			put32(data, 4+4*i, len(data))
			data = append(data, encodeCoverage(set)...)
		}
		if err := add(12, data); err != nil {
			return nil, nil, nil, err
		}
	}
	return result, markBases, classBases, nil
}
