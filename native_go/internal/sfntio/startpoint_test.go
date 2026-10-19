package sfntio

import (
	"encoding/binary"
	"testing"

	"hd2font/native/internal/geometry"
)

func TestDecodeUsesFirstExplicitOnCurve(t *testing.T) {
	// Off/on/off/on order: fontTools starts at the first explicit on-curve,
	// not the final point. This ordering affects Skia's later grid rounding.
	data := make([]byte, 14)
	binary.BigEndian.PutUint16(data, 1)
	binary.BigEndian.PutUint16(data[6:], 100)
	binary.BigEndian.PutUint16(data[8:], 100)
	binary.BigEndian.PutUint16(data[10:], 3)
	data = append(data, 0, 1, 0, 1)
	for _, delta := range []int16{0, 50, 50, -50, 50, 50, -50, -50} {
		data = binary.BigEndian.AppendUint16(data, uint16(delta))
	}
	p, err := DecodeGlyph(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) == 0 || p[0].Verb != geometry.Move || p[0].Points[0] != 50 || p[0].Points[1] != 100 {
		t.Fatalf("wrong contour start: %v", p)
	}
}
