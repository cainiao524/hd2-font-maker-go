//go:build windows && amd64

package fontcore

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"hd2font/native/internal/geometry"
	"hd2font/native/internal/harfbuzz"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/otlayout"
	"hd2font/native/internal/sfntio"
)

func TestV13UnchangedNormalizationRetainsHintsAndBearing(t *testing.T) {
	p := processor(t)
	data := readTest(t, "testdata/layout.ttf")
	f, _ := sfntio.Parse(data, 0)
	m, _ := fontMetrics(f)
	be := binary.BigEndian
	var path geometry.Path
	path.MoveTo(50, 0)
	path.QuadTo(0, 50, 50, 100)
	path.LineTo(100, 100)
	path.LineTo(100, 0)
	path.Close()
	path, err := p.Geometry.Process(path, geometry.Options{Simplify: true, Clockwise: true})
	if err != nil {
		t.Fatal(err)
	}
	g, err := sfntio.EncodeGlyph(path)
	if err != nil {
		t.Fatal(err)
	}
	be.PutUint16(g.Data[2:], 25)
	at := 10 + 2*int(g.Contours)
	hinted := append([]byte(nil), g.Data[:at]...)
	hinted = append(hinted, 0, 3, 0xb0, 0, 0x2e)
	hinted = append(hinted, g.Data[at+2:]...)
	var glyf []byte
	loca := make([]byte, (m.count+1)*4)
	for i := 0; i < m.count; i++ {
		be.PutUint32(loca[4*i:], uint32(len(glyf)))
		raw := f.Tables["glyf"][m.offsets[i]:m.offsets[i+1]]
		if i == 1 {
			raw = hinted
		}
		glyf = append(glyf, raw...)
		if len(glyf)%2 != 0 {
			glyf = append(glyf, 0)
		}
	}
	be.PutUint32(loca[4*m.count:], uint32(len(glyf)))
	f.Tables["glyf"], f.Tables["loca"] = glyf, loca
	be.PutUint16(f.Tables["head"][50:], 1)
	n := int(be.Uint16(f.Tables["hhea"][34:]))
	bearingAt := 6
	if n == 1 {
		bearingAt = 4
	}
	be.PutUint16(f.Tables["hmtx"][bearingAt:], 25)
	data, err = f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Prepare(context.Background(), data, 0, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	ff, _ := sfntio.Parse(out, 0)
	mm, _ := fontMetrics(ff)
	raw := ff.Tables["glyf"][mm.offsets[1]:mm.offsets[2]]
	if mm.bearings[1] != 25 || int16(be.Uint16(raw[2:])) != 0 {
		t.Fatal("normalization changed bearing or failed stored bounds refresh")
	}
	nc := int(be.Uint16(raw))
	at = 10 + nc*2
	if be.Uint16(raw[at:]) != 3 {
		t.Fatal("unchanged glyph lost source hint instructions")
	}
}

func processor(t *testing.T) *Processor {
	t.Helper()
	p, err := NewProcessor(filepath.Join("..", "..", "bin"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}
func readTest(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func shape(t *testing.T, p *Processor, data []byte, text string, features ...string) []harfbuzz.Glyph {
	t.Helper()
	f, err := p.HB.NewFace(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, err := f.Shape(text, harfbuzz.ShapeOptions{Script: "Latn", Language: "en", Features: features})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestNativeFallbackMergeAndProfiles(t *testing.T) {
	p := processor(t)
	a := readTest(t, "testdata/layout.ttf")
	b := readTest(t, "testdata/donor.ttf")
	ctx := context.Background()
	merged, err := p.Merge(ctx, a, [][]byte{b})
	if err != nil {
		t.Fatal(err)
	}
	face, err := inventory.InspectFace(merged)
	if err != nil {
		t.Fatal(err)
	}
	for _, cp := range []rune{'A', 'V', 'B', 'W', 0xe000, 0xe001} {
		if face.Cmap[cp] == 0 {
			t.Fatalf("missing U+%04X", cp)
		}
	}
	for _, test := range []struct {
		text    string
		cp      rune
		advance int32
	}{{"AV", 0xe000, 520}, {"BW", 0xe001, 480}} {
		lig := shape(t, p, merged, test.text, "liga=1", "kern=1")
		if len(lig) != 1 || lig[0].ID != uint32(face.Cmap[test.cp]) {
			t.Fatalf("lost ligature %s: %v", test.text, lig)
		}
		kern := shape(t, p, merged, test.text, "liga=0", "kern=1")
		if len(kern) != 2 || kern[0].XAdvance != test.advance {
			t.Fatalf("lost kerning %s: %v", test.text, kern)
		}
	}
	for _, ttf := range []bool{true, false} {
		for _, upm := range []uint16{512, 1000, 2048} {
			for _, clean := range []bool{false, true} {
				out, metrics, err := p.Conform(ctx, merged, ttf, upm, a, clean)
				if err != nil {
					t.Fatalf("profile %v/%d/%v: %v", ttf, upm, clean, err)
				}
				parsed, err := inventory.InspectFace(out)
				if err != nil {
					t.Fatal(err)
				}
				if parsed.UPM != upm || len(parsed.Cmap) != len(face.Cmap) {
					t.Fatal("profile changed coverage or units")
				}
				if metrics[0] != float32(scaledInt(800, float64(upm)/1000)) {
					t.Fatal("line height changed")
				}
				kern := shape(t, p, out, "BW", "liga=0", "kern=1")
				want := int32(scaledInt(600, float64(upm)/1000) + scaledInt(-120, float64(upm)/1000))
				if kern[0].XAdvance != want {
					t.Fatalf("scaled positioning mismatch: %v vs %d", kern, want)
				}
			}
		}
	}
}

func TestMergedContextAndMarkNamespaces(t *testing.T) {
	p := processor(t)
	a := readTest(t, "../otlayout/testdata/base.ttf")
	b := readTest(t, "../otlayout/testdata/donor.ttf")
	fa, _ := sfntio.Parse(a, 0)
	fb, _ := sfntio.Parse(b, 0)
	parts := []otlayout.Part{{GSUB: fa.Tables["GSUB"], GPOS: fa.Tables["GPOS"], GDEF: fa.Tables["GDEF"]}, {GSUB: fb.Tables["GSUB"], GPOS: fb.Tables["GPOS"], GDEF: fb.Tables["GDEF"]}}
	merged, err := otlayout.Merge(parts)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range merged {
		fa.Tables[k] = v
	}
	out, err := fa.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		text   string
		source []byte
	}{{"AV", a}, {"BW", b}, {"A\u0301", a}, {"B\u0302", b}, {"\u0301\u0301", a}, {"\u0302\u0302", b}} {
		want := shape(t, p, test.source, test.text, "liga=0", "calt=1", "kern=1", "mark=1", "mkmk=1")
		got := shape(t, p, out, test.text, "liga=0", "calt=1", "kern=1", "mark=1", "mkmk=1")
		if len(want) != len(got) {
			t.Fatalf("shape count %q", test.text)
		}
		for i := range want {
			if want[i] != got[i] {
				t.Fatalf("shape differs %q: %v != %v", test.text, got, want)
			}
		}
	}
}
