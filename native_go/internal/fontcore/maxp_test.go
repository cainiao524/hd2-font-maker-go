package fontcore

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"hd2font/native/internal/sfntio"
)

func maxpSimple(points, contours int, hints []byte) []byte {
	be := binary.BigEndian
	raw := make([]byte, 10)
	be.PutUint16(raw, uint16(contours))
	for i := 1; i <= contours; i++ {
		raw = be.AppendUint16(raw, uint16(i*points/contours-1))
	}
	raw = be.AppendUint16(raw, uint16(len(hints)))
	raw = append(raw, hints...)
	return append(raw, bytes.Repeat([]byte{0x31}, points)...)
}

func maxpComposite(hints []byte, ids ...uint16) []byte {
	be := binary.BigEndian
	raw := make([]byte, 10)
	be.PutUint16(raw, 0xffff)
	for i, id := range ids {
		flags := uint16(2)
		if i+1 < len(ids) {
			flags |= 32
		} else if len(hints) > 0 {
			flags |= 256
		}
		raw = be.AppendUint16(raw, flags)
		raw = be.AppendUint16(raw, id)
		raw = append(raw, 0, 0)
	}
	if len(hints) > 0 {
		raw = be.AppendUint16(raw, uint16(len(hints)))
		raw = append(raw, hints...)
	}
	return raw
}

func checkMaxp(t *testing.T, profile []byte, want map[int]uint16) {
	t.Helper()
	if len(profile) != 32 || binary.BigEndian.Uint32(profile) != 0x10000 {
		t.Fatalf("invalid TrueType profile: %x", profile)
	}
	for offset, value := range want {
		if got := binary.BigEndian.Uint16(profile[offset:]); got != value {
			t.Errorf("maxp offset %d: got %d, want %d", offset, got, value)
		}
	}
}

func TestMaximumProfileRecursiveComponents(t *testing.T) {
	// Forward references, shared children and an empty component. Five copies
	// of a 16-point contour expand to the reported 80 points / 5 contours.
	glyphs := [][]byte{
		nil,
		maxpComposite(nil, 3, 0),
		maxpSimple(16, 1, nil),
		maxpComposite(nil, 4, 2, 2, 2),
		maxpComposite(nil, 2, 2),
	}
	before := make([][]byte, len(glyphs))
	for i, raw := range glyphs {
		before[i] = bytes.Clone(raw)
	}
	profile, err := ttfMaximumProfile(nil, glyphs)
	if err != nil {
		t.Fatal(err)
	}
	checkMaxp(t, profile, map[int]uint16{4: 5, 6: 16, 8: 1, 10: 80, 12: 5, 14: 1, 26: 0, 28: 4, 30: 3})
	for i, raw := range glyphs {
		if !bytes.Equal(raw, before[i]) {
			t.Fatalf("glyph %d mutated", i)
		}
	}
}

func TestMaximumProfileComponentRecordFormats(t *testing.T) {
	for _, flags := range []uint16{0, 1, 2, 3, 8, 9, 64, 65, 128, 129} {
		raw := maxpComposite(nil, 0)
		binary.BigEndian.PutUint16(raw[10:], flags)
		extra := 0
		if flags&1 != 0 {
			extra += 2
		}
		if flags&8 != 0 {
			extra += 2
		} else if flags&64 != 0 {
			extra += 4
		} else if flags&128 != 0 {
			extra += 8
		}
		raw = append(raw, make([]byte, extra)...)
		// Put instructions on the first component, as accepted by the existing
		// hint stripper, and a byte-argument component after its transform.
		binary.BigEndian.PutUint16(raw[10:], flags|32|256)
		raw = append(raw, maxpComposite(nil, 0)[10:]...)
		raw = append(raw, 0, 3, 0xb0, 0, 0x21)
		profile, err := ttfMaximumProfile(nil, [][]byte{maxpSimple(4, 1, nil), raw})
		if err != nil {
			t.Fatalf("flags %x: %v", flags, err)
		}
		checkMaxp(t, profile, map[int]uint16{10: 8, 12: 2, 26: 3, 28: 2, 30: 1})
	}
}

func TestMaximumProfileRetainsSimpleAndHintRequirements(t *testing.T) {
	previous := make([]byte, 32)
	be := binary.BigEndian
	for offset, value := range map[int]uint16{6: 100, 8: 20, 10: 300, 12: 100, 14: 2, 16: 7, 18: 8, 20: 9, 22: 10, 24: 11, 26: 12, 28: 40, 30: 10} {
		be.PutUint16(previous[offset:], value)
	}
	before := bytes.Clone(previous)
	glyphs := [][]byte{maxpSimple(4, 1, []byte{0xb0, 0, 0x21}), maxpComposite(bytes.Repeat([]byte{0x22}, 13), 0)}
	profile, err := ttfMaximumProfile(previous, glyphs)
	if err != nil {
		t.Fatal(err)
	}
	checkMaxp(t, profile, map[int]uint16{6: 100, 8: 20, 10: 4, 12: 1, 26: 13, 28: 1, 30: 1})
	if !bytes.Equal(previous, before) || !bytes.Equal(profile[14:26], before[14:26]) {
		t.Fatal("retained bytecode requirements changed")
	}
	profile, err = ttfMaximumProfile(profile, glyphs[:1])
	if err != nil {
		t.Fatal(err)
	}
	checkMaxp(t, profile, map[int]uint16{10: 0, 12: 0, 28: 0, 30: 0})
	profile, err = ttfMaximumProfile(nil, [][]byte{maxpSimple(0, 0, []byte{0x22, 0x22}), make([]byte, 10)})
	if err != nil {
		t.Fatal(err)
	}
	checkMaxp(t, profile, map[int]uint16{6: 0, 8: 0, 26: 2})
}

func TestMaximumProfileInvalidComponents(t *testing.T) {
	overflow := [][]byte{maxpSimple(1, 1, nil)}
	for i := 0; i < 16; i++ {
		overflow = append(overflow, maxpComposite(nil, uint16(i), uint16(i)))
	}
	for _, tc := range []struct {
		name, message string
		glyphs        [][]byte
	}{
		{"cycle", "cyclic", [][]byte{maxpComposite(nil, 1), maxpComposite(nil, 0)}},
		{"missing", "out of range", [][]byte{maxpComposite(nil, 1)}},
		{"header", "truncated header", [][]byte{{0}}},
		{"component", "truncated component", [][]byte{maxpComposite(nil, 0)[:12]}},
		{"arguments", "arguments or transform", [][]byte{maxpComposite(nil, 0)[:15]}},
		{"instructions", "truncated instructions", [][]byte{maxpSimple(4, 1, nil), maxpComposite([]byte{0x22}, 0)[:18]}},
		{"overflow", "exceeds uint16", overflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ttfMaximumProfile(nil, tc.glyphs); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("got %v, want %q", err, tc.message)
			}
		})
	}
}

func TestSetTTFGlyphsRecomputesCompoundProfile(t *testing.T) {
	for _, keepHints := range []bool{false, true} {
		f := &sfntio.Font{Tables: map[string][]byte{"head": make([]byte, 54), "hhea": make([]byte, 36), "maxp": make([]byte, 32), "fpgm": {0x22}, "prep": {0x22}, "cvt ": {0, 1}}}
		for off := 14; off <= 26; off += 2 {
			binary.BigEndian.PutUint16(f.Tables["maxp"][off:], 2)
		}
		var hints []byte
		if keepHints {
			hints = []byte{0xb0, 0, 0x21}
		}
		glyphs := []sfntio.Glyph{
			{Data: maxpSimple(16, 1, hints), Contours: 1, Points: 16},
			{Data: maxpComposite(hints, 0, 0, 0, 0, 0), Contours: 0xffff},
		}
		if err := setTTFGlyphs(f, glyphs, []uint16{20, 20}, []int16{0, 0}, keepHints); err != nil {
			t.Fatal(err)
		}
		checkMaxp(t, f.Tables["maxp"], map[int]uint16{6: 16, 8: 1, 10: 80, 12: 5, 26: uint16(len(hints)), 28: 5, 30: 1})
		for off := 14; off <= 24; off += 2 {
			want := uint16(0)
			if keepHints {
				want = 2
			} else if off == 14 {
				want = 1
			}
			checkMaxp(t, f.Tables["maxp"], map[int]uint16{off: want})
		}
		for _, tag := range []string{"fpgm", "prep", "cvt "} {
			if (len(f.Tables[tag]) > 0) != keepHints {
				t.Fatalf("keepHints=%v: %s retention changed", keepHints, tag)
			}
		}
		for i, glyph := range glyphs {
			at := binary.BigEndian.Uint32(f.Tables["loca"][4*i:])
			if !bytes.Equal(f.Tables["glyf"][int(at):int(at)+len(glyph.Data)], glyph.Data) {
				t.Fatalf("keepHints=%v: glyph %d changed", keepHints, i)
			}
		}
	}
}
