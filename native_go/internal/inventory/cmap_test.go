package inventory

import (
	"encoding/binary"
	"reflect"
	"testing"
)

func TestCmap4DeltaAndGlyphArray(t *testing.T) {
	// Three ordinary segments and the required sentinel. The glyph-array
	// segment has both a zero mapping and a nonzero glyph adjusted by idDelta.
	data := make([]byte, 54)
	for i, value := range []uint16{4, 54, 0, 8, 0, 0, 0, 65, 67, 69, 0xffff, 0, 65, 66, 68, 0xffff, 0xffc0, 1, 0xffbc, 1, 0, 6, 0, 0, 0, 4, 0} {
		binary.BigEndian.PutUint16(data[2*i:], value)
	}
	got, err := decodeCmap(data)
	if err != nil {
		t.Fatal(err)
	}
	// A -> 1, B -> missing, C -> 5, D -> missing, E -> 1.
	want := map[rune]uint16{'A': 1, 'C': 5, 'E': 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got, want)
	}
	broken := append([]byte(nil), data...)
	binary.BigEndian.PutUint16(broken[42:], 0xffff)
	if _, err = decodeCmap(broken); err == nil {
		t.Fatal("out-of-bounds glyph-array offset accepted")
	}
}

func TestCmap13AndInvalidGroups(t *testing.T) {
	data := make([]byte, 28)
	binary.BigEndian.PutUint16(data, 13)
	binary.BigEndian.PutUint32(data[4:], uint32(len(data)))
	binary.BigEndian.PutUint32(data[12:], 1)
	binary.BigEndian.PutUint32(data[16:], 0x10000)
	binary.BigEndian.PutUint32(data[20:], 0x10002)
	binary.BigEndian.PutUint32(data[24:], 8)
	got, err := decodeCmap(data)
	if err != nil || !reflect.DeepEqual(got, map[rune]uint16{0x10000: 8, 0x10001: 8, 0x10002: 8}) {
		t.Fatal(got, err)
	}
	binary.BigEndian.PutUint32(data[20:], 0xffffffff)
	if _, err = decodeCmap(data); err == nil {
		t.Fatal("unbounded cmap group accepted")
	}
}
