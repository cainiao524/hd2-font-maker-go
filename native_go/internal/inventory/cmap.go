package inventory

import (
	"encoding/binary"
	"fmt"
)

func bestCmap(data []byte) (map[rune]uint16, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("missing or truncated cmap")
	}
	count := int(binary.BigEndian.Uint16(data[2:]))
	if 4+8*count > len(data) {
		return nil, fmt.Errorf("truncated cmap records")
	}
	for _, preferred := range [][2]uint16{{3, 10}, {0, 6}, {0, 4}, {3, 1}, {0, 3}, {0, 2}, {0, 1}, {0, 0}} {
		for i := 0; i < count; i++ {
			r := data[4+8*i:]
			if binary.BigEndian.Uint16(r) != preferred[0] || binary.BigEndian.Uint16(r[2:]) != preferred[1] {
				continue
			}
			at := uint64(binary.BigEndian.Uint32(r[4:]))
			if at+2 > uint64(len(data)) {
				return nil, fmt.Errorf("cmap offset exceeds table")
			}
			return decodeCmap(data[int(at):])
		}
	}
	return nil, nil
}

func decodeCmap(data []byte) (map[rune]uint16, error) {
	if len(data) < 6 {
		return nil, fmt.Errorf("truncated cmap subtable")
	}
	format := binary.BigEndian.Uint16(data)
	length := uint64(binary.BigEndian.Uint16(data[2:]))
	if format >= 8 {
		if len(data) < 12 {
			return nil, fmt.Errorf("truncated long cmap header")
		}
		length = uint64(binary.BigEndian.Uint32(data[4:]))
	}
	if length > uint64(len(data)) || length < 6 {
		return nil, fmt.Errorf("invalid cmap length")
	}
	data = data[:int(length)]
	result := make(map[rune]uint16)
	put := func(cp uint32, gid uint32) error {
		if cp > 0x10ffff || gid > 0xffff {
			return fmt.Errorf("cmap codepoint or glyph exceeds supported range")
		}
		if gid != 0 {
			result[rune(cp)] = uint16(gid)
		}
		return nil
	}
	switch format {
	case 0:
		if len(data) < 262 {
			return nil, fmt.Errorf("truncated format 0 cmap")
		}
		for cp, gid := range data[6:262] {
			_ = put(uint32(cp), uint32(gid))
		}
	case 4:
		if len(data) < 16 {
			return nil, fmt.Errorf("truncated format 4 cmap")
		}
		count := int(binary.BigEndian.Uint16(data[6:])) / 2
		if count == 0 || binary.BigEndian.Uint16(data[6:])%2 != 0 || 16+8*count > len(data) {
			return nil, fmt.Errorf("invalid format 4 segments")
		}
		for i := 0; i < count; i++ {
			end := uint32(binary.BigEndian.Uint16(data[14+2*i:]))
			start := uint32(binary.BigEndian.Uint16(data[16+2*count+2*i:]))
			delta := binary.BigEndian.Uint16(data[16+4*count+2*i:])
			rangeAt := 16 + 6*count + 2*i
			offset := int(binary.BigEndian.Uint16(data[rangeAt:]))
			if start > end {
				return nil, fmt.Errorf("reversed cmap segment")
			}
			for cp := start; cp <= end && cp != 0xffff; cp++ {
				gid := uint16(cp) + delta
				if offset != 0 {
					at := rangeAt + offset + 2*int(cp-start)
					if at+2 > len(data) {
						return nil, fmt.Errorf("cmap glyph array exceeds subtable")
					}
					gid = binary.BigEndian.Uint16(data[at:])
					if gid != 0 {
						gid += delta
					}
				}
				_ = put(cp, uint32(gid))
			}
		}
	case 6, 10:
		var start, count uint32
		at := 10
		if format == 6 {
			if len(data) < 10 {
				return nil, fmt.Errorf("truncated format 6 cmap")
			}
			start, count = uint32(binary.BigEndian.Uint16(data[6:])), uint32(binary.BigEndian.Uint16(data[8:]))
		} else {
			if len(data) < 20 {
				return nil, fmt.Errorf("truncated format 10 cmap")
			}
			start, count, at = binary.BigEndian.Uint32(data[12:]), binary.BigEndian.Uint32(data[16:]), 20
		}
		if uint64(at)+2*uint64(count) > uint64(len(data)) || uint64(start)+uint64(count) > 0x110000 {
			return nil, fmt.Errorf("invalid trimmed cmap array")
		}
		for i := uint32(0); i < count; i++ {
			_ = put(start+i, uint32(binary.BigEndian.Uint16(data[at+2*int(i):])))
		}
	case 12, 13:
		if len(data) < 16 {
			return nil, fmt.Errorf("truncated grouped cmap")
		}
		count := uint64(binary.BigEndian.Uint32(data[12:]))
		if 16+12*count > uint64(len(data)) {
			return nil, fmt.Errorf("truncated cmap groups")
		}
		var previous uint32
		for i := 0; i < int(count); i++ {
			r := data[16+12*i:]
			start, end, gid := binary.BigEndian.Uint32(r), binary.BigEndian.Uint32(r[4:]), binary.BigEndian.Uint32(r[8:])
			if start > end || end > 0x10ffff || (i > 0 && start <= previous) {
				return nil, fmt.Errorf("invalid cmap group range")
			}
			previous = end
			for cp := start; cp <= end; cp++ {
				value := uint64(gid)
				if format == 12 {
					value += uint64(cp - start)
				}
				if value > 0xffff {
					return nil, fmt.Errorf("cmap glyph exceeds uint16")
				}
				if err := put(cp, uint32(value)); err != nil {
					return nil, err
				}
			}
		}
	default:
		return nil, fmt.Errorf("unsupported Unicode cmap format %d", format)
	}
	return result, nil
}
