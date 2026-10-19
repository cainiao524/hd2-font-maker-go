package fontcore

import (
	"encoding/binary"
	"os"
	"testing"

	"github.com/tdewolff/font"
	"hd2font/native/internal/geometry"
	"hd2font/native/internal/sfntio"
)

func TestGlyphSetSideBearingCompensation(t *testing.T) {
	data, err := os.ReadFile("testdata/layout.ttf")
	if err != nil {
		t.Fatal(err)
	}
	tables, err := sfntio.Parse(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := font.ParseSFNT(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	id := parsed.GlyphIndex('A')
	before, err := readGlyph(tables, parsed, id)
	if err != nil {
		t.Fatal(err)
	}
	position := int(id)*4 + 2
	nmetrics := int(parsed.Hhea.NumberOfHMetrics)
	if int(id) >= nmetrics {
		position = 4*nmetrics + 2*(int(id)-nmetrics)
	}
	metric := tables.Tables["hmtx"]
	binary.BigEndian.PutUint16(metric[position:], binary.BigEndian.Uint16(metric[position:])+1)
	changed, err := tables.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err = font.ParseSFNT(changed, 0)
	if err != nil {
		t.Fatal(err)
	}
	after, err := readGlyph(tables, parsed, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatal("translated path length changed")
	}
	for i, p := range before {
		count := 0
		switch p.Verb {
		case geometry.Move, geometry.Line:
			count = 1
		case geometry.Quad:
			count = 2
		case geometry.Cubic:
			count = 3
		}
		for j := 0; j < count; j++ {
			p.Points[2*j]++
		}
		if p != after[i] {
			t.Fatalf("side bearing offset not applied at command %d", i)
		}
	}
}

func TestAuthoredFontWithoutFinalPadding(t *testing.T) {
	data, err := os.ReadFile("testdata/layout.ttf")
	if err != nil {
		t.Fatal(err)
	}
	f, err := sfntio.Parse(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Tables["zzzz"] = []byte{1}
	padded, err := f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	f, err = sfntio.Parse(padded[:len(padded)-3], 0)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseMetadata(f)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.NumGlyphs() != 4 {
		t.Fatal("glyph count changed after alignment")
	}
}
