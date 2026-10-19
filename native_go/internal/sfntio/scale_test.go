package sfntio

import (
	"bytes"
	"encoding/binary"
	"testing"

	"hd2font/native/internal/geometry"
)

func TestScaleStoredGridAndHintPreservation(t *testing.T) {
	var p geometry.Path
	p.MoveTo(10, 20)
	p.QuadTo(50, 101, 100, 20)
	p.Close()
	g, err := EncodeGlyph(p)
	if err != nil {
		t.Fatal(err)
	}
	count := int(binary.BigEndian.Uint16(g.Data))
	at := 10 + 2*count
	hinted := append([]byte(nil), g.Data[:at]...)
	hinted = append(hinted, 0, 3, 0xb0, 0, 0x2e)
	hinted = append(hinted, g.Data[at+2:]...)
	scaled, err := ScaleGlyphData(hinted, 1.024)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(scaled[at:at+5], []byte{0, 3, 0xb0, 0, 0x2e}) {
		t.Fatal("hint bytecode changed")
	}
	path, err := DecodeGlyph(scaled)
	if err != nil {
		t.Fatal(err)
	}
	if path[0].Points[0] != 10 || path[1].Points[1] != 103 || path[1].Points[2] != 102 {
		t.Fatalf("stored grid scaling: %v", path)
	}
	stripped, err := StripGlyphHints(scaled)
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(stripped[at:]) != 0 {
		t.Fatal("hints not removed")
	}
	again, err := DecodeGlyph(stripped)
	if err != nil || !bytes.Equal(encodeCommands(path), encodeCommands(again)) {
		t.Fatal("stripping changed geometry")
	}
}
func encodeCommands(p geometry.Path) []byte {
	var b []byte
	for _, c := range p {
		b = binary.LittleEndian.AppendUint32(b, c.Verb)
		for _, v := range c.Points {
			b = binary.LittleEndian.AppendUint32(b, uint32(v*2))
		}
	}
	return b
}
func TestCompositeScalePromotesByteArguments(t *testing.T) {
	data := make([]byte, 10)
	binary.BigEndian.PutUint16(data, 65535)
	data = append(data, 0, 2, 0, 3, 100, 20)
	got, err := ScaleGlyphData(data, 4)
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(got[10:]) != 3 || binary.BigEndian.Uint16(got[14:]) != 400 || binary.BigEndian.Uint16(got[16:]) != 80 {
		t.Fatalf("composite translations not promoted: %x", got)
	}
}
