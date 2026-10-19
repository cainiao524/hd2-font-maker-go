package fontcore

import (
	"encoding/binary"
	"fmt"
)

// ttfMaximumProfile reads the final glyph IDs, including repeated and nested
// components. Only the retained font's profile supplies bytecode requirements.
func ttfMaximumProfile(previous []byte, glyphs [][]byte) ([]byte, error) {
	if len(glyphs) > 65535 {
		return nil, fmt.Errorf("maximum profile glyph count exceeds uint16")
	}
	profile := make([]byte, 32)
	copy(profile, previous)
	be := binary.BigEndian
	be.PutUint32(profile, 0x10000)
	be.PutUint16(profile[4:], uint16(len(glyphs)))
	be.PutUint16(profile[14:], max(uint16(1), be.Uint16(profile[14:])))
	for _, offset := range []int{10, 12, 28, 30} {
		be.PutUint16(profile[offset:], 0)
	}
	update := func(offset, value int) error {
		if value > 65535 {
			return fmt.Errorf("maximum profile field at %d exceeds uint16", offset)
		}
		be.PutUint16(profile[offset:], max(uint16(value), be.Uint16(profile[offset:])))
		return nil
	}
	type totals struct{ points, contours, depth int }
	cache := make([]totals, len(glyphs))
	state := make([]uint8, len(glyphs))
	var visit func(int) (totals, error)
	visit = func(id int) (totals, error) {
		var sum totals
		if id >= len(glyphs) {
			return sum, fmt.Errorf("component glyph %d is out of range", id)
		}
		if state[id] == 2 {
			return cache[id], nil
		}
		if state[id] == 1 {
			return sum, fmt.Errorf("cyclic component reference at glyph %d", id)
		}
		state[id] = 1
		raw := glyphs[id]
		if len(raw) == 0 {
			state[id] = 2
			return sum, nil
		}
		if len(raw) < 10 {
			return sum, fmt.Errorf("glyph %d: truncated header", id)
		}
		contours := int(int16(be.Uint16(raw)))
		at := 10
		instructions := false
		if contours >= 0 {
			sum.contours = contours
			at += 2 * contours
			if at > len(raw) {
				return sum, fmt.Errorf("glyph %d: truncated contour endpoints", id)
			}
			if contours > 0 {
				sum.points = int(be.Uint16(raw[at-2:])) + 1
			}
			// A zero-contour glyph may consist of its header alone.
			instructions = contours > 0 || at < len(raw)
			if err := update(6, sum.points); err != nil {
				return sum, err
			}
			if err := update(8, sum.contours); err != nil {
				return sum, err
			}
		} else {
			elements := 0
			for {
				if at+4 > len(raw) {
					return sum, fmt.Errorf("glyph %d: truncated component", id)
				}
				flags, child := be.Uint16(raw[at:]), int(be.Uint16(raw[at+2:]))
				at += 4
				if flags&1 != 0 {
					at += 4
				} else {
					at += 2
				}
				if flags&8 != 0 {
					at += 2
				} else if flags&64 != 0 {
					at += 4
				} else if flags&128 != 0 {
					at += 8
				}
				if at > len(raw) {
					return sum, fmt.Errorf("glyph %d: truncated component arguments or transform", id)
				}
				part, err := visit(child)
				if err != nil {
					return sum, err
				}
				sum.points += part.points
				sum.contours += part.contours
				sum.depth = max(sum.depth, part.depth+1)
				elements++
				if sum.points > 65535 || sum.contours > 65535 || elements > 65535 {
					return sum, fmt.Errorf("glyph %d: composite maximum exceeds uint16", id)
				}
				instructions = instructions || flags&256 != 0
				if flags&32 == 0 {
					break
				}
			}
			for _, field := range [][2]int{{10, sum.points}, {12, sum.contours}, {28, elements}, {30, sum.depth}} {
				if err := update(field[0], field[1]); err != nil {
					return sum, err
				}
			}
		}
		if instructions {
			if at+2 > len(raw) {
				return sum, fmt.Errorf("glyph %d: truncated instruction length", id)
			}
			size := int(be.Uint16(raw[at:]))
			if at+2+size > len(raw) {
				return sum, fmt.Errorf("glyph %d: truncated instructions", id)
			}
			if err := update(26, size); err != nil {
				return sum, err
			}
		}
		cache[id], state[id] = sum, 2
		return sum, nil
	}
	for id := range glyphs {
		if _, err := visit(id); err != nil {
			return nil, err
		}
	}
	return profile, nil
}
