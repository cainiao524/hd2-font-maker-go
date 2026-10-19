package inventory_test

import (
	"os"
	"testing"

	"hd2font/native/internal/inventory"
	"hd2font/native/internal/sfntio"
)

func TestCompleteCmapWriter(t *testing.T) {
	data, err := os.ReadFile("../fontcore/testdata/layout.ttf")
	if err != nil {
		t.Fatal(err)
	}
	font, err := sfntio.Parse(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	m := map[rune]uint16{0: 1, 0xddac: 2, 0xffff: 3, 0x10ffff: 1}
	for i := 0; i < 50000; i++ {
		m[rune(i)] = uint16(1 + i%3)
	}
	font.Tables["cmap"] = sfntio.UnicodeCmap(m)
	raw, err := font.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	face, err := inventory.InspectFace(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(face.Cmap) != len(m) {
		t.Fatalf("cmap size %d/%d", len(face.Cmap), len(m))
	}
	for cp, gid := range m {
		if face.Cmap[cp] != gid {
			t.Fatalf("mapping changed U+%04X", cp)
		}
	}
}
