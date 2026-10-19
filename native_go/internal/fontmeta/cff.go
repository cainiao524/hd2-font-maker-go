package fontmeta

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type dictNumber struct {
	raw   []byte
	value float64
}
type dictEntry struct {
	op   uint16
	args []dictNumber
}
type cffHeader struct {
	names, strings    [][]byte
	top               []dictEntry
	globalStart, body int
}

func readIndex(data []byte, at int) ([][]byte, int, error) {
	if at < 0 || at > len(data)-2 {
		return nil, 0, fmt.Errorf("truncated CFF INDEX")
	}
	n := int(binary.BigEndian.Uint16(data[at:]))
	if n == 0 {
		return nil, at + 2, nil
	}
	if at+3 > len(data) {
		return nil, 0, fmt.Errorf("truncated CFF INDEX size")
	}
	size := int(data[at+2])
	if size < 1 || size > 4 {
		return nil, 0, fmt.Errorf("invalid CFF INDEX size")
	}
	base := at + 3 + (n+1)*size
	if base > len(data) {
		return nil, 0, fmt.Errorf("truncated CFF INDEX offsets")
	}
	objects := make([][]byte, 0, n)
	previous := uint64(1)
	for i := 0; i <= n; i++ {
		var offset uint64
		for _, b := range data[at+3+i*size : at+3+(i+1)*size] {
			offset = offset<<8 | uint64(b)
		}
		if offset < previous || offset-1 > uint64(len(data)-base) || i == 0 && offset != 1 {
			return nil, 0, fmt.Errorf("invalid CFF INDEX offset")
		}
		if i > 0 {
			objects = append(objects, data[base+int(previous)-1:base+int(offset)-1])
		}
		previous = offset
	}
	return objects, base + int(previous) - 1, nil
}

func makeIndex(objects [][]byte) ([]byte, error) {
	if len(objects) > 65535 {
		return nil, fmt.Errorf("CFF INDEX count overflow")
	}
	if len(objects) == 0 {
		return []byte{0, 0}, nil
	}
	last := uint64(1)
	for _, o := range objects {
		last += uint64(len(o))
	}
	if last > math.MaxUint32 {
		return nil, fmt.Errorf("CFF INDEX offset overflow")
	}
	size := 1
	for last >= uint64(1)<<(8*size) {
		size++
	}
	out := make([]byte, 3+(len(objects)+1)*size)
	binary.BigEndian.PutUint16(out, uint16(len(objects)))
	out[2] = byte(size)
	offset := uint32(1)
	for i := 0; i <= len(objects); i++ {
		for j := 0; j < size; j++ {
			out[3+i*size+j] = byte(offset >> uint(8*(size-1-j)))
		}
		if i < len(objects) {
			offset += uint32(len(objects[i]))
		}
	}
	for _, o := range objects {
		out = append(out, o...)
	}
	return out, nil
}

func readDict(data []byte) ([]dictEntry, error) {
	var entries []dictEntry
	var args []dictNumber
	for at := 0; at < len(data); {
		start := at
		b := data[at]
		at++
		if b <= 21 {
			op := uint16(b)
			if b == 12 {
				if at >= len(data) {
					return nil, fmt.Errorf("truncated CFF operator")
				}
				op = 0x0c00 | uint16(data[at])
				at++
			}
			entries = append(entries, dictEntry{op, args})
			args = nil
			continue
		}
		var value float64
		switch {
		case b >= 32 && b <= 246:
			value = float64(int(b) - 139)
		case b >= 247 && b <= 254:
			if at >= len(data) {
				return nil, fmt.Errorf("truncated CFF integer")
			}
			v := int(data[at])
			at++
			if b <= 250 {
				value = float64((int(b)-247)*256 + v + 108)
			} else {
				value = float64(-(int(b)-251)*256 - v - 108)
			}
		case b == 28:
			if at+2 > len(data) {
				return nil, fmt.Errorf("truncated CFF short")
			}
			value = float64(int16(binary.BigEndian.Uint16(data[at:])))
			at += 2
		case b == 29:
			if at+4 > len(data) {
				return nil, fmt.Errorf("truncated CFF long")
			}
			value = float64(int32(binary.BigEndian.Uint32(data[at:])))
			at += 4
		case b == 30:
			var text strings.Builder
			done := false
			for at < len(data) && !done {
				v := data[at]
				at++
				for _, n := range []byte{v >> 4, v & 15} {
					switch {
					case n <= 9:
						text.WriteByte('0' + n)
					case n == 10:
						text.WriteByte('.')
					case n == 11:
						text.WriteByte('E')
					case n == 12:
						text.WriteString("E-")
					case n == 14:
						text.WriteByte('-')
					case n == 15:
						done = true
					default:
						return nil, fmt.Errorf("reserved CFF real digit")
					}
					if done {
						break
					}
				}
			}
			if !done {
				return nil, fmt.Errorf("unterminated CFF real")
			}
			var err error
			value, err = strconv.ParseFloat(text.String(), 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("invalid CFF real")
			}
		default:
			return nil, fmt.Errorf("unsupported CFF DICT byte %d", b)
		}
		args = append(args, dictNumber{data[start:at], value})
	}
	if len(args) != 0 {
		return nil, fmt.Errorf("unterminated CFF DICT operands")
	}
	return entries, nil
}

func parseCFF(data []byte) (*cffHeader, error) {
	if len(data) < 4 || data[0] != 1 || data[2] < 4 || int(data[2]) > len(data) {
		return nil, fmt.Errorf("invalid CFF1 header")
	}
	names, at, err := readIndex(data, int(data[2]))
	if err != nil {
		return nil, err
	}
	tops, at, err := readIndex(data, at)
	if err != nil {
		return nil, err
	}
	if len(names) != 1 || len(tops) != 1 {
		return nil, fmt.Errorf("CFF registration requires one font")
	}
	strs, at, err := readIndex(data, at)
	if err != nil {
		return nil, err
	}
	_, end, err := readIndex(data, at)
	if err != nil {
		return nil, err
	}
	top, err := readDict(tops[0])
	if err != nil {
		return nil, err
	}
	return &cffHeader{names, strs, top, at, end}, nil
}

func integer(n dictNumber) (int, error) {
	if n.value < 0 || n.value > math.MaxInt32 || n.value != math.Trunc(n.value) {
		return 0, fmt.Errorf("invalid nonnegative CFF integer")
	}
	return int(n.value), nil
}
func number(value int) dictNumber {
	b := []byte{29, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(b[1:], uint32(value))
	return dictNumber{b, float64(value)}
}
func find(entries []dictEntry, op uint16) *dictEntry {
	for i := range entries {
		if entries[i].op == op {
			return &entries[i]
		}
	}
	return nil
}
func sidString(header *cffHeader, n dictNumber) ([]byte, error) {
	id, err := integer(n)
	if err != nil {
		return nil, err
	}
	if id < len(standardStrings) {
		return []byte(standardStrings[id]), nil
	}
	id -= len(standardStrings)
	if id >= len(header.strings) {
		return nil, fmt.Errorf("CFF SID outside String INDEX")
	}
	return header.strings[id], nil
}
func latin1(value string) ([]byte, error) {
	out := make([]byte, 0, len(value))
	for _, r := range value {
		if r > 255 {
			return nil, fmt.Errorf("CFF identity cannot encode %q as Latin-1", value)
		}
		out = append(out, byte(r))
	}
	return out, nil
}

func registerCFF(data, template []byte, names []nameRecord) ([]byte, error) {
	h, err := parseCFF(data)
	if err != nil {
		return nil, err
	}
	global, _, err := readIndex(data, h.globalStart)
	if err != nil {
		return nil, err
	}
	if len(global) != 0 || find(h.top, 0x0c1e) != nil || find(h.top, 0x0c24) != nil || find(h.top, 0x0c25) != nil {
		return nil, fmt.Errorf("output CFF must be name-keyed without subroutines")
	}
	if private := find(h.top, 18); private != nil {
		if len(private.args) != 2 {
			return nil, fmt.Errorf("invalid CFF Private DICT")
		}
		size, e := integer(private.args[0])
		if e != nil {
			return nil, e
		}
		offset, e := integer(private.args[1])
		if e != nil {
			return nil, e
		}
		if offset > len(data) || size > len(data)-offset {
			return nil, fmt.Errorf("CFF Private DICT outside table")
		}
		entries, e := readDict(data[offset : offset+size])
		if e != nil {
			return nil, e
		}
		if find(entries, 19) != nil {
			return nil, fmt.Errorf("output CFF contains local subroutines")
		}
	}
	cs := find(h.top, 17)
	if cs == nil || len(cs.args) != 1 {
		return nil, fmt.Errorf("missing CFF CharStrings")
	}
	csOffset, err := integer(cs.args[0])
	if err != nil {
		return nil, err
	}
	if _, _, err = readIndex(data, csOffset); err != nil {
		return nil, err
	}
	identity := map[uint16][]byte{}
	fontNames := h.names
	var italic *dictNumber
	if len(template) > 0 {
		ref, e := parseCFF(template)
		if e != nil {
			return nil, e
		}
		fontNames = ref.names
		for _, op := range []uint16{2, 3, 4} {
			if entry := find(ref.top, op); entry != nil {
				if len(entry.args) != 1 {
					return nil, fmt.Errorf("invalid CFF identity operands")
				}
				identity[op], err = sidString(ref, entry.args[0])
				if err != nil {
					return nil, err
				}
			}
		}
		zero := number(0)
		italic = &zero
		if entry := find(ref.top, 0x0c02); entry != nil {
			if len(entry.args) != 1 {
				return nil, fmt.Errorf("invalid CFF ItalicAngle")
			}
			italic = &entry.args[0]
		}
	} else {
		ps := debugName(names, 6)
		if ps == "" {
			ps = "HD2Custom"
		}
		encoded, e := latin1(ps)
		if e != nil {
			return nil, e
		}
		fontNames = [][]byte{encoded}
		for op, id := range map[uint16]uint16{2: 4, 3: 1} {
			value := debugName(names, id)
			if value == "" {
				return nil, fmt.Errorf("template missing CFF identity name ID %d", id)
			}
			identity[op], err = latin1(value)
			if err != nil {
				return nil, err
			}
		}
	}
	strs := append([][]byte(nil), h.strings...)
	sids := make(map[string]int, len(standardStrings)+len(strs))
	for i, s := range standardStrings {
		sids[s] = i
	}
	for i, s := range strs {
		if _, ok := sids[string(s)]; !ok {
			sids[string(s)] = 391 + i
		}
	}
	updates := map[uint16]dictNumber{}
	for _, op := range []uint16{2, 3, 4} {
		value, ok := identity[op]
		if !ok {
			continue
		}
		sid, ok := sids[string(value)]
		if !ok {
			sid = 391 + len(strs)
			if sid >= 65000 {
				return nil, fmt.Errorf("name-keyed CFF SID capacity exhausted")
			}
			strs = append(strs, value)
			sids[string(value)] = sid
		}
		updates[op] = number(sid)
	}
	if italic != nil {
		updates[0x0c02] = *italic
	}
	entries := make([]dictEntry, 0, len(h.top)+len(updates))
	for _, entry := range h.top {
		if value, ok := updates[entry.op]; ok {
			entries = append(entries, dictEntry{entry.op, []dictNumber{value}})
			delete(updates, entry.op)
		} else {
			entries = append(entries, entry)
		}
	}
	for _, op := range []uint16{2, 3, 4, 0x0c02} {
		if value, ok := updates[op]; ok {
			entries = append(entries, dictEntry{op, []dictNumber{value}})
		}
	}
	encode := func(delta int) ([]byte, error) {
		var result []byte
		for _, entry := range entries {
			for i, arg := range entry.args {
				relocate := entry.op == 17 || entry.op == 18 && i == 1 || entry.op == 15 && arg.value > 2 || entry.op == 16 && arg.value > 1
				if relocate {
					offset, e := integer(arg)
					if e != nil {
						return nil, e
					}
					if offset < h.body || offset > len(data) || int64(offset)+int64(delta) > math.MaxInt32 || offset+delta < 0 {
						return nil, fmt.Errorf("unsupported CFF offset outside body")
					}
					result = append(result, number(offset+delta).raw...)
				} else {
					result = append(result, arg.raw...)
				}
			}
			if entry.op >= 0x0c00 {
				result = append(result, 12, byte(entry.op))
			} else {
				result = append(result, byte(entry.op))
			}
		}
		return result, nil
	}
	nameIndex, err := makeIndex(fontNames)
	if err != nil {
		return nil, err
	}
	stringIndex, err := makeIndex(strs)
	if err != nil {
		return nil, err
	}
	top, err := encode(0)
	if err != nil {
		return nil, err
	}
	topIndex, err := makeIndex([][]byte{top})
	if err != nil {
		return nil, err
	}
	prefix := int(data[2]) + len(nameIndex) + len(topIndex) + len(stringIndex) + (h.body - h.globalStart)
	top, err = encode(prefix - h.body)
	if err != nil {
		return nil, err
	}
	topIndex, err = makeIndex([][]byte{top})
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), data[:data[2]]...)
	out = append(out, nameIndex...)
	out = append(out, topIndex...)
	out = append(out, stringIndex...)
	out = append(out, data[h.globalStart:h.body]...)
	// Every byte after the indexes is retained, including CharStrings, charset,
	// Private DICT widths, and any padding. Only absolute Top DICT offsets move.
	out = append(out, data[h.body:]...)
	return out, nil
}
