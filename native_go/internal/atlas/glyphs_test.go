package atlas

import (
	"bytes"
	"encoding/binary"
	"math"
	"reflect"
	"testing"
)

func tableFixture(keys []uint32, kerns int) []byte {
	b := make([]byte, 88+32*len(keys)+12*kerns)
	copy(b, []byte("FIXTURE!"))
	putFloat(b, 8, 16)
	putFloat(b, 12, 20)
	putFloat(b, 16, 12)
	for i, n := range []uint32{uint32(len(keys)), 88, uint32(kerns), uint32(88 + 32*len(keys)), 0} {
		binary.LittleEndian.PutUint32(b[68+i*4:], n)
	}
	for i, key := range keys {
		binary.LittleEndian.PutUint32(b[88+4*i:], key)
		putFloat(b, 88+4*len(keys)+28*i+24, 9.6)
	}
	return b
}

func TestGlyphKeysLayout(t *testing.T) {
	want := []uint32{65, 0, 0x570b, 0xffffffff}
	original := tableFixture(want, 2)
	got, err := GlyphKeys(original)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("keys=%v err=%v", got, err)
	}
	cases := map[string][]byte{
		"short": original[:87], "truncated": original[:len(original)-1],
		"trailing": append(bytes.Clone(original), 0),
	}
	for name, offset := range map[string]int{"offset": 72, "kern offset": 80, "overflow count": 68, "overflow kerns": 76} {
		b := bytes.Clone(original)
		binary.LittleEndian.PutUint32(b[offset:], math.MaxUint32)
		cases[name] = b
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := GlyphKeys(b); err == nil {
				t.Fatal("accepted invalid table")
			}
		})
	}
}

func TestReplaceGlyphTable(t *testing.T) {
	original := tableFixture([]uint32{65, 0, 66, 65}, 2)
	before := bytes.Clone(original)
	g := Glyph{Unicode: 65, Advance: 1, AtlasBounds: &Bounds{Left: 2, Top: 3, Right: 10, Bottom: 15}, PlaneBounds: &Bounds{Left: 0.25, Top: -0.5}}
	glyphs := map[rune]Glyph{'?': {Advance: 0.5}, 'A': g, ' ': {Advance: 0.25}}
	out, err := ReplaceGlyphTable(original, 64, 128, glyphs, 32, 0x570b, 65)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, before) {
		t.Fatal("mutated original table")
	}
	keys, err := GlyphKeys(out)
	if err != nil || !reflect.DeepEqual(keys, []uint32{0, 32, 65, 66, 0x570b}) {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
	if len(out) != 248 || !bytes.Equal(out[:8], original[:8]) {
		t.Fatal("bad header/size")
	}
	for off, want := range map[int]float64{8: 32, 12: 40, 16: 24, 20: 1.0 / 64, 24: 1.0 / 128, 36: 8, 64: 16} {
		if got := readFloat(out, off); got != want {
			t.Errorf("field %d=%g want %g", off, got, want)
		}
	}
	for off, want := range map[int]uint32{68: 5, 72: 88, 76: 0, 80: 248, 84: 0} {
		if got := binary.LittleEndian.Uint32(out[off:]); got != want {
			t.Errorf("field %d=%d want %d", off, got, want)
		}
	}
	wantA := [7]float64{2, 3, 8, 12, 8, -24, 32}
	for i, want := range wantA {
		if got := readFloat(out, 88+20+28*2+4*i); got != want {
			t.Errorf("A[%d]=%g want %g", i, got, want)
		}
	}
	for _, i := range []int{0, 3, 4} {
		if !bytes.Equal(out[40:68], out[108+28*i:136+28*i]) {
			t.Fatalf("missing key %d did not use default", keys[i])
		}
	}
	if got := readFloat(out, 108+28+24); got != 8 {
		t.Errorf("space advance=%g", got)
	}
}

func TestDefaultGlyphAndInvalidMetrics(t *testing.T) {
	original := tableFixture([]uint32{0, 65}, 0)
	for _, tc := range []struct {
		name    string
		glyphs  map[rune]Glyph
		advance float64
		fail    bool
	}{
		{"question", map[rune]Glyph{'?': {Advance: 0.5}}, 16, false},
		{"square preferred", map[rune]Glyph{'?': {Advance: 0.5}, 0x25a1: {Advance: 0.75}}, 24, false},
		{"missing default", map[rune]Glyph{'A': {Advance: 1}}, 0, true},
		{"missing plane", map[rune]Glyph{'?': {AtlasBounds: &Bounds{}}}, 0, true},
		{"nan", map[rune]Glyph{'?': {Advance: math.NaN()}}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ReplaceGlyphTable(original, 64, 64, tc.glyphs)
			if tc.fail {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := readFloat(out, 64); got != tc.advance {
				t.Errorf("default advance=%g", got)
			}
		})
	}
	putFloat(original, 8, 0)
	if _, err := ReplaceGlyphTable(original, 1, 1, map[rune]Glyph{'?': {}}); err == nil {
		t.Fatal("accepted zero original size")
	}
}

func TestResolveBinding(t *testing.T) {
	const id = uint64(0x3da89f2c15b53d65)
	binding, ok := LookupBinding(id)
	if !ok || binding.FontName != "M XiangHe Hei TC Bold" || binding.TextureID != 0x86667f650dca00f9 {
		t.Fatal("incorrect TC binding")
	}
	if len(bindings) != 13 {
		t.Fatalf("binding count=%d", len(bindings))
	}
	keys := []uint32{33, 34, 35, 36, 37, 38, 39, 40}
	font := FontMetrics{Name: binding.FontName, UnitsPerEm: 1000, Advances: map[rune]uint16{}}
	for _, cp := range keys {
		font.Advances[rune(cp)] = 600
	}
	payload := tableFixture(keys, 1)
	fonts := []FontMetrics{{Name: "unrelated"}, font}
	textures := []Texture{{ID: binding.TextureID}, {ID: 42}}
	fi, ti, err := ResolveBinding(id, payload, textures, fonts)
	if err != nil || fi != 1 || ti != 0 {
		t.Fatalf("indexes=(%d,%d) err=%v", fi, ti, err)
	}
	bad := bytes.Clone(payload)
	putFloat(bad, 88+4*len(keys)+24, 9.61)
	if _, _, err := ResolveBinding(id, bad, textures, fonts); err == nil {
		t.Fatal("accepted changed advance")
	}
	if _, _, err := ResolveBinding(id, tableFixture(keys[:7], 0), textures, fonts); err == nil {
		t.Fatal("accepted insufficient probe glyphs")
	}
	if _, _, err := ResolveBinding(id, payload, append(textures, textures[0]), fonts); err == nil {
		t.Fatal("accepted ambiguous texture")
	}
	if _, _, err := ResolveBinding(id, payload, textures, fonts[:1]); err == nil {
		t.Fatal("accepted missing font")
	}
	if _, _, err := ResolveBinding(42, nil, textures, fonts); err == nil {
		t.Fatal("accepted unknown ambiguous binding")
	}
	if fi, ti, err := ResolveBinding(42, nil, textures[:1], fonts[:1]); err != nil || fi != 0 || ti != 0 {
		t.Fatal("single-pair fallback failed")
	}
}
