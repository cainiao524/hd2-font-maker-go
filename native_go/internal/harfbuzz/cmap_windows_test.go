//go:build windows && amd64

package harfbuzz

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	"hd2font/native/internal/sfntio"
)

func cmap12(t *testing.T, data []byte) map[rune]uint16 {
	t.Helper()
	b := parsed(t, data).Tables["cmap"]
	be := binary.BigEndian
	for i := 0; i < int(be.Uint16(b[2:])); i++ {
		r := b[4+8*i:]
		at := int(be.Uint32(r[4:]))
		if be.Uint16(b[at:]) != 12 {
			continue
		}
		result := map[rune]uint16{}
		for j := 0; j < int(be.Uint32(b[at+12:])); j++ {
			g := b[at+16+12*j:]
			first, last, gid := be.Uint32(g), be.Uint32(g[4:]), be.Uint32(g[8:])
			for cp := first; cp <= last; cp++ {
				result[rune(cp)] = uint16(gid + cp - first)
			}
		}
		return result
	}
	t.Fatal("missing output format 12 cmap")
	return nil
}

func syntheticCmapFont(t *testing.T, name string, mapping map[rune]uint16, format uint16) []byte {
	t.Helper()
	f := parsed(t, fixture(t, name))
	b := sfntio.UnicodeCmap(mapping)
	if format != 0 {
		be := binary.BigEndian
		found := false
		for i := 0; i < int(be.Uint16(b[2:])); i++ {
			r := b[4+8*i : 4+8*(i+1)]
			at := int(be.Uint32(r[4:]))
			if be.Uint16(b[at:]) != format {
				continue
			}
			size := int(be.Uint16(b[at+2:]))
			if format == 12 {
				size = int(be.Uint32(b[at+4:]))
			}
			out := make([]byte, 12)
			be.PutUint16(out[2:], 1)
			copy(out[4:8], r[:4])
			be.PutUint32(out[8:], 12)
			b = append(out, b[at:at+size]...)
			found = true
			break
		}
		if !found {
			t.Fatalf("fixture lacks format %d", format)
		}
	}
	f.Tables["cmap"] = b
	data, err := f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSubsetOpaqueSurrogateCmapCodes(t *testing.T) {
	e := engine(t)
	mapping := map[rune]uint16{65: 1, 86: 2, 0xddac: 3, 0xddb5: 3}
	for _, ext := range []string{"ttf", "otf"} {
		for _, format := range []uint16{0, 4, 12} {
			t.Run(fmt.Sprintf("%s-format%d", ext, format), func(t *testing.T) {
				data := syntheticCmapFont(t, "base."+ext, mapping, format)
				out, err := e.Subset(data, []uint32{0xddac})
				if err != nil {
					t.Fatal(err)
				}
				if got := cmap12(t, out); !reflect.DeepEqual(got, map[rune]uint16{0xddac: 3}) {
					t.Fatalf("surrogate-only map: %v", got)
				}
				before, err := e.NativeDraw(data, 3, 0)
				if err != nil {
					t.Fatal(err)
				}
				after, err := e.NativeDraw(out, 3, 0)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("surrogate glyph lost: %v", err)
				}
				out, err = e.Subset(data, []uint32{65, 86, 0xddac, 0xddb5})
				if err != nil {
					t.Fatal(err)
				}
				if got := cmap12(t, out); !reflect.DeepEqual(got, mapping) {
					t.Fatalf("full map: %v", got)
				}
			})
		}
	}
}

func TestSubsetFull43653CodeRepertoire(t *testing.T) {
	e := engine(t)
	mapping := make(map[rune]uint16, 43653)
	codes := make([]uint32, 0, 43653)
	for cp := uint32(0); cp < 43643; cp++ {
		mapping[rune(cp)] = uint16(1 + cp%3)
		codes = append(codes, cp)
	}
	for cp := uint32(0xddac); cp <= 0xddb5; cp++ {
		mapping[rune(cp)] = 3
		codes = append(codes, cp)
	}
	data := syntheticCmapFont(t, "base.ttf", mapping, 0)
	out, err := e.Subset(data, codes)
	if err != nil {
		t.Fatal(err)
	}
	if got := cmap12(t, out); !reflect.DeepEqual(got, mapping) {
		t.Fatalf("repertoire changed: input=%d output=%d surrogate DDAC=%d", len(mapping), len(got), got[0xddac])
	}
}
