package fontcore

import (
	"encoding/binary"
	"fmt"
	"math"

	"hd2font/native/internal/otlayout"
	"hd2font/native/internal/sfntio"
)

type metrics struct {
	count    int
	upm      uint16
	advances []uint16
	bearings []int16
	offsets  []uint32
	ttf      bool
}

func fontMetrics(f *sfntio.Font) (*metrics, error) {
	be := binary.BigEndian
	if len(f.Tables["head"]) < 54 || len(f.Tables["hhea"]) < 36 || len(f.Tables["maxp"]) < 6 {
		return nil, fmt.Errorf("incomplete font metrics")
	}
	m := &metrics{count: int(be.Uint16(f.Tables["maxp"][4:])), upm: be.Uint16(f.Tables["head"][18:])}
	_, m.ttf = f.Tables["glyf"]
	if m.count == 0 || m.upm == 0 {
		return nil, fmt.Errorf("font has no glyphs or units")
	}
	n := int(be.Uint16(f.Tables["hhea"][34:]))
	h := f.Tables["hmtx"]
	if n == 0 || n > m.count || len(h) < 4*n+2*(m.count-n) {
		return nil, fmt.Errorf("invalid horizontal metrics")
	}
	m.advances = make([]uint16, m.count)
	m.bearings = make([]int16, m.count)
	for i := 0; i < m.count; i++ {
		m.advances[i] = be.Uint16(h[4*min(i, n-1):])
		off := 4*i + 2
		if i >= n {
			off = 4*n + 2*(i-n)
		}
		m.bearings[i] = int16(be.Uint16(h[off:]))
	}
	if m.ttf {
		format := be.Uint16(f.Tables["head"][50:])
		size := 2
		if format == 1 {
			size = 4
		} else if format != 0 {
			return nil, fmt.Errorf("unsupported glyph location format")
		}
		loca := f.Tables["loca"]
		if len(loca) < size*(m.count+1) {
			return nil, fmt.Errorf("truncated glyph locations")
		}
		m.offsets = make([]uint32, m.count+1)
		for i := range m.offsets {
			if size == 2 {
				m.offsets[i] = uint32(be.Uint16(loca[2*i:])) * 2
			} else {
				m.offsets[i] = be.Uint32(loca[4*i:])
			}
			if uint64(m.offsets[i]) > uint64(len(f.Tables["glyf"])) || (i > 0 && m.offsets[i] < m.offsets[i-1]) {
				return nil, fmt.Errorf("invalid glyph location")
			}
		}
	}
	return m, nil
}
func scaledInt(value int, ratio float64) int { return int(math.Floor(float64(value)*ratio + .5)) }

func scaleMetadata(f *sfntio.Font, target uint16) error {
	be := binary.BigEndian
	if len(f.Tables["head"]) < 54 {
		return fmt.Errorf("missing font header")
	}
	source := be.Uint16(f.Tables["head"][18:])
	if source == target {
		return nil
	}
	if source == 0 || target < 16 || target > 16384 {
		return fmt.Errorf("invalid font units")
	}
	r := float64(target) / float64(source)
	scale := func(data []byte, offset int, unsigned bool) error {
		if offset+2 > len(data) {
			return nil
		}
		value := int(be.Uint16(data[offset:]))
		if !unsigned {
			value = int(int16(value))
		}
		n := scaledInt(value, r)
		if (!unsigned && (n < math.MinInt16 || n > math.MaxInt16)) || (unsigned && (n < 0 || n > math.MaxUint16)) {
			return fmt.Errorf("scaled font metric exceeds storage")
		}
		be.PutUint16(data[offset:], uint16(n))
		return nil
	}
	for tag, offsets := range map[string][]int{"head": {36, 38, 40, 42}, "post": {8, 10}, "hhea": {4, 6, 8, 12, 14, 16, 22}, "vhea": {4, 6, 8, 12, 14, 16, 22}, "OS/2": {2, 10, 12, 14, 16, 18, 20, 22, 24, 26, 28, 68, 70, 72, 86, 88}} {
		for _, o := range offsets {
			if err := scale(f.Tables[tag], o, false); err != nil {
				return err
			}
		}
	}
	for tag, offsets := range map[string][]int{"hhea": {10}, "vhea": {10}, "OS/2": {74, 76}} {
		for _, o := range offsets {
			if err := scale(f.Tables[tag], o, true); err != nil {
				return err
			}
		}
	}
	for _, pair := range [][2]string{{"hhea", "hmtx"}, {"vhea", "vmtx"}} {
		header, table := f.Tables[pair[0]], f.Tables[pair[1]]
		if len(header) < 36 || len(table) == 0 {
			continue
		}
		n := int(be.Uint16(header[34:]))
		for i := 0; i < len(table); i += 2 {
			if err := scale(table, i, i < 4*n && i%4 == 0); err != nil {
				return err
			}
		}
	}
	if vorg := f.Tables["VORG"]; len(vorg) >= 8 {
		if err := scale(vorg, 4, false); err != nil {
			return err
		}
		n := int(be.Uint16(vorg[6:]))
		if len(vorg) < 8+4*n {
			return fmt.Errorf("truncated vertical origins")
		}
		for i := 0; i < n; i++ {
			if err := scale(vorg, 10+4*i, false); err != nil {
				return err
			}
		}
	}
	for i := 0; i < len(f.Tables["cvt "]); i += 2 {
		if err := scale(f.Tables["cvt "], i, false); err != nil {
			return err
		}
	}
	for _, tag := range []string{"GPOS", "GDEF", "BASE", "MATH"} {
		if data := f.Tables[tag]; len(data) > 0 {
			out, err := otlayout.Scale(tag, data, r)
			if err != nil {
				return fmt.Errorf("scale %s: %w", tag, err)
			}
			f.Tables[tag] = out
		}
	}
	if kern := f.Tables["kern"]; len(kern) > 0 {
		if len(kern) < 4 || be.Uint16(kern) != 0 {
			return fmt.Errorf("unsupported legacy kerning version")
		}
		n := int(be.Uint16(kern[2:]))
		p := 4
		for i := 0; i < n; i++ {
			if p+6 > len(kern) {
				return fmt.Errorf("truncated kerning table")
			}
			length, format := int(be.Uint16(kern[p+2:])), be.Uint16(kern[p+4:])>>8
			if p+length > len(kern) || length < 14 || format != 0 {
				return fmt.Errorf("unsupported legacy kerning format")
			}
			count := int(be.Uint16(kern[p+6:]))
			if 14+6*count > length {
				return fmt.Errorf("invalid kerning pairs")
			}
			for j := 0; j < count; j++ {
				if err := scale(kern, p+18+6*j, false); err != nil {
					return err
				}
			}
			p += length
		}
	}
	be.PutUint16(f.Tables["head"][18:], target)
	return nil
}
