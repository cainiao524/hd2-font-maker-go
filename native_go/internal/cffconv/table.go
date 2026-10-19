package cffconv

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Table returns one name-keyed CFF1 table. charstrings are valid standalone
// Type 2 programs (normally Encode results), ordered by GID, with .notdef at
// index zero. Other glyph names are gid1, gid2, etc. No subroutines are installed.
// upm only sets FontMatrix to [1/upm 0 0 1/upm 0 0]; glyph bytes are unchanged.
// bbox and all other sfnt tables/metrics are the caller's responsibility.
func Table(name string, charstrings [][]byte, bbox [4]int16, upm uint16) ([]byte, error) {
	if len(name) == 0 || len(name) > 127 {
		return nil, fmt.Errorf("CFF name must contain 1 to 127 ASCII characters")
	}
	for _, c := range []byte(name) {
		if c < 33 || c > 126 || strings.ContainsRune("[](){}<>/%", rune(c)) {
			return nil, fmt.Errorf("CFF name contains a forbidden character")
		}
	}
	if upm == 0 {
		return nil, fmt.Errorf("CFF units per em must be nonzero")
	}
	if bbox[0] > bbox[2] || bbox[1] > bbox[3] {
		return nil, fmt.Errorf("invalid CFF bounding box")
	}
	// .notdef uses SID 0; generated names use SIDs 391 through 64999.
	if len(charstrings) == 0 || len(charstrings) > 64610 {
		return nil, fmt.Errorf("name-keyed CFF requires 1 to 64610 glyphs with generated names")
	}
	for gid, cs := range charstrings {
		if len(cs) == 0 || len(cs) > maxCharString || cs[len(cs)-1] != 14 {
			return nil, fmt.Errorf("glyph %d must be a complete Type 2 charstring ending in endchar, at most %d bytes", gid, maxCharString)
		}
	}
	names := make([][]byte, len(charstrings)-1)
	for i := range names {
		names[i] = []byte("gid" + strconv.Itoa(i+1))
	}
	stringIndex, err := makeIndex(names)
	if err != nil {
		return nil, err
	}
	charIndex, err := makeIndex(charstrings)
	if err != nil {
		return nil, err
	}
	nameIndex, err := makeIndex([][]byte{[]byte(name)})
	if err != nil {
		return nil, err
	}
	charset := []byte{0} // .notdef is implicit, including for a one-glyph table.
	if len(charstrings) > 1 {
		charset = []byte{2, 1, 135, 0, 0} // format 2, first SID 391.
		binary.BigEndian.PutUint16(charset[3:], uint16(len(charstrings)-2))
	}
	private := []byte{139, 20, 139, 21} // defaultWidthX=0, nominalWidthX=0.
	top := make([]byte, 0, 80)
	for _, v := range bbox {
		top = appendInt(top, int32(v))
	}
	top = append(top, 5) // FontBBox.
	top = appendReal(top, 1/float64(upm))
	top = append(top, 139, 139)
	top = appendReal(top, 1/float64(upm))
	top = append(top, 139, 139, 12, 7) // FontMatrix.
	// Fixed-width DICT offsets keep index sizes stable while placing the data.
	charsetSlot := len(top) + 1
	top = append(top, 29, 0, 0, 0, 0, 15)
	charSlot := len(top) + 1
	top = append(top, 29, 0, 0, 0, 0, 17)
	top = appendInt(top, int32(len(private)))
	privateSlot := len(top) + 1
	top = append(top, 29, 0, 0, 0, 0, 18)
	topIndex, err := makeIndex([][]byte{top})
	if err != nil {
		return nil, err
	}
	charsetOffset := int64(4) + int64(len(nameIndex)) + int64(len(topIndex)) + int64(len(stringIndex)) + 2
	charOffset := charsetOffset + int64(len(charset))
	privateOffset := charOffset + int64(len(charIndex))
	total := privateOffset + int64(len(private))
	if total > math.MaxInt32 {
		return nil, fmt.Errorf("CFF table exceeds signed DICT offset range")
	}
	binary.BigEndian.PutUint32(top[charsetSlot:], uint32(charsetOffset))
	binary.BigEndian.PutUint32(top[charSlot:], uint32(charOffset))
	binary.BigEndian.PutUint32(top[privateSlot:], uint32(privateOffset))
	// Replace the sole object's bytes; its index header and length stay fixed.
	copy(topIndex[len(topIndex)-len(top):], top)
	out := make([]byte, 0, int(total))
	out = append(out, 1, 0, 4, 4)
	out = append(out, nameIndex...)
	out = append(out, topIndex...)
	out = append(out, stringIndex...)
	out = append(out, 0, 0) // Empty Global Subr INDEX.
	out = append(out, charset...)
	out = append(out, charIndex...)
	out = append(out, private...)
	return out, nil
}

func makeIndex(objects [][]byte) ([]byte, error) {
	if len(objects) > math.MaxUint16 {
		return nil, fmt.Errorf("CFF INDEX has more than 65535 objects")
	}
	if len(objects) == 0 {
		return []byte{0, 0}, nil
	}
	last := uint64(1)
	for _, object := range objects {
		last += uint64(len(object))
	}
	if last > math.MaxUint32 {
		return nil, fmt.Errorf("CFF INDEX data exceeds 32-bit offsets")
	}
	offSize := 1
	for last >= uint64(1)<<(8*offSize) {
		offSize++
	}
	headerSize := 3 + (len(objects)+1)*offSize
	if uint64(headerSize)+last-1 > uint64(math.MaxInt) {
		return nil, fmt.Errorf("CFF INDEX exceeds addressable storage")
	}
	out := make([]byte, headerSize, headerSize+int(last)-1)
	binary.BigEndian.PutUint16(out, uint16(len(objects)))
	out[2] = byte(offSize)
	offset := uint32(1)
	for i := 0; i <= len(objects); i++ {
		for j := 0; j < offSize; j++ {
			out[3+i*offSize+j] = byte(offset >> uint(8*(offSize-1-j)))
		}
		if i < len(objects) {
			offset += uint32(len(objects[i]))
		}
	}
	for _, object := range objects {
		out = append(out, object...)
	}
	return out, nil
}

func appendReal(dst []byte, value float64) []byte {
	dst = append(dst, 30)
	digits := strconv.FormatFloat(value, 'f', -1, 64)
	pending := byte(0)
	half := false
	put := func(nibble byte) {
		if half {
			dst = append(dst, pending<<4|nibble)
		} else {
			pending = nibble
		}
		half = !half
	}
	for _, c := range digits {
		if c == '.' {
			put(10)
		} else {
			put(byte(c - '0'))
		}
	}
	put(15)
	if half {
		put(15)
	}
	return dst
}
