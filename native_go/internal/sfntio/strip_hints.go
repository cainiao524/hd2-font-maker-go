package sfntio

import (
	"encoding/binary"
	"fmt"
)

// StripGlyphHints preserves stored points/components. Donor instructions refer
// to a different font's global program/CVT and cannot be carried into a merge.
func StripGlyphHints(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if len(data) < 10 {
		return nil, fmt.Errorf("truncated glyph")
	}
	be := binary.BigEndian
	n := int(int16(be.Uint16(data)))
	out := append([]byte(nil), data...)
	if n >= 0 {
		p := 10 + 2*n
		if n == 0 && p == len(data) {
			return out, nil
		}
		if p+2 > len(data) {
			return nil, fmt.Errorf("truncated glyph instruction length")
		}
		count := int(be.Uint16(data[p:]))
		if p+2+count > len(data) {
			return nil, fmt.Errorf("truncated glyph instructions")
		}
		be.PutUint16(out[p:], 0)
		return append(out[:p+2], data[p+2+count:]...), nil
	}
	p := 10
	instructions := false
	for {
		if p+4 > len(data) {
			return nil, fmt.Errorf("truncated composite")
		}
		flags := be.Uint16(data[p:])
		if flags&256 != 0 {
			instructions = true
			be.PutUint16(out[p:], flags&^256)
		}
		p += 4
		if flags&1 != 0 {
			p += 4
		} else {
			p += 2
		}
		if flags&8 != 0 {
			p += 2
		} else if flags&64 != 0 {
			p += 4
		} else if flags&128 != 0 {
			p += 8
		}
		if p > len(data) {
			return nil, fmt.Errorf("truncated component transform")
		}
		if flags&32 == 0 {
			break
		}
	}
	if instructions {
		if p+2 > len(data) || p+2+int(be.Uint16(data[p:])) > len(data) {
			return nil, fmt.Errorf("truncated composite instructions")
		}
		out = out[:p]
	}
	return out, nil
}
