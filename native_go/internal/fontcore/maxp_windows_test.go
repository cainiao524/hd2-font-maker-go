//go:build windows && amd64

package fontcore

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"hd2font/native/internal/archiveio"
	"hd2font/native/internal/geometry"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/sfntio"
)

func maxpTestFont(t *testing.T, cp rune, id, upm, hintRequirement uint16) []byte {
	t.Helper()
	f, err := sfntio.Parse(readTest(t, "testdata/layout.ttf"), 0)
	if err != nil {
		t.Fatal(err)
	}
	var path geometry.Path
	path.MoveTo(0, 0)
	path.LineTo(0, 10)
	path.LineTo(10, 10)
	path.LineTo(10, 0)
	path.Close()
	g, err := sfntio.EncodeGlyph(path)
	if err != nil {
		t.Fatal(err)
	}
	hints := []byte{0xb0, 0, 0x21}
	at := 10 + 2*int(g.Contours)
	raw := append([]byte(nil), g.Data[:at]...)
	raw = append(raw, 0, byte(len(hints)))
	raw = append(raw, hints...)
	g.Data = append(raw, g.Data[at+2:]...)
	glyphs := []sfntio.Glyph{{}, g}
	for _, ids := range [][]uint16{{1, 1}, {2, 1, 1, 1}, {3}} {
		raw := maxpComposite(hints, ids...)
		binary.BigEndian.PutUint16(raw[6:], 10)
		binary.BigEndian.PutUint16(raw[8:], 10)
		glyphs = append(glyphs, storedGlyph(raw))
	}
	f.Tables["maxp"] = make([]byte, 32)
	binary.BigEndian.PutUint16(f.Tables["maxp"][14:], 2)
	for off := 16; off <= 24; off += 2 {
		binary.BigEndian.PutUint16(f.Tables["maxp"][off:], hintRequirement)
	}
	f.Tables["fpgm"], f.Tables["prep"], f.Tables["cvt "] = hints, []byte{0x22}, []byte{0, 1}
	if err := setTTFGlyphs(f, glyphs, []uint16{20, 20, 20, 20, 20}, make([]int16, 5), true); err != nil {
		t.Fatal(err)
	}
	f.Tables["cmap"] = sfntio.UnicodeCmap(map[rune]uint16{cp: id})
	binary.BigEndian.PutUint16(f.Tables["head"][18:], upm)
	for _, tag := range []string{"GSUB", "GPOS", "GDEF", "kern"} {
		delete(f.Tables, tag)
	}
	data, err := f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func maxpFontToolsOracle(t *testing.T, data []byte) {
	t.Helper()
	python := os.Getenv("HD2_MAXP_PYTHON")
	if python == "" {
		return
	}
	path := filepath.Join(t.TempDir(), "font.ttf")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	const script = `
import sys
from fontTools.ttLib import TTFont
f = TTFont(sys.argv[1])
m = f['maxp']
fields = ['maxPoints', 'maxContours', 'maxCompositePoints', 'maxCompositeContours', 'maxComponentElements', 'maxComponentDepth']
declared = {k: getattr(m, k) for k in fields}
m.recalc(f)
for k in fields:
    actual = getattr(m, k)
    assert actual <= declared[k] if k in fields[:2] else actual == declared[k], (k, declared[k], actual)
instructions = max((len(g.program.getBytecode()) if hasattr(g, 'program') else 0 for g in f['glyf'].glyphs.values()), default=0)
assert instructions <= m.maxSizeOfInstructions, (instructions, m.maxSizeOfInstructions)
print(declared)
`
	output, err := exec.Command(python, "-c", script, path).CombinedOutput()
	if err != nil {
		t.Fatalf("fontTools oracle: %v\n%s", err, output)
	}
	t.Logf("fontTools: %s", bytes.TrimSpace(output))
}

func TestMergeCompoundProfileMixedFallbacks(t *testing.T) {
	p := processor(t)
	ctx := context.Background()
	base := maxpTestFont(t, 'A', 1, 1000, 4)
	donors := [][]byte{maxpTestFont(t, 'B', 2, 1000, 77), maxpTestFont(t, 'C', 4, 2000, 99)}
	primary, err := p.subset(ctx, base, []uint32{'A'})
	if err != nil {
		t.Fatal(err)
	}
	before, err := sfntio.Parse(primary, 0)
	if err != nil {
		t.Fatal(err)
	}
	bm, err := fontMetrics(before)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := p.Merge(ctx, base, donors)
	if err != nil {
		t.Fatal(err)
	}
	after, err := sfntio.Parse(merged, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the source composite organization while scaling the donor, as 1.3 does.
	checkMaxp(t, after.Tables["maxp"], map[int]uint16{6: 4, 8: 1, 10: 20, 12: 5, 26: 3, 28: 4, 30: 3})
	if !bytes.Equal(before.Tables["maxp"][14:26], after.Tables["maxp"][14:26]) {
		t.Fatal("foreign bytecode requirements entered the merged profile")
	}
	for _, tag := range []string{"fpgm", "prep", "cvt "} {
		if !bytes.Equal(before.Tables[tag], after.Tables[tag]) {
			t.Fatalf("primary %s changed", tag)
		}
	}
	am, err := fontMetrics(after)
	if err != nil {
		t.Fatal(err)
	}
	for id := 0; id < am.count; id++ {
		raw := after.Tables["glyf"][am.offsets[id]:am.offsets[id+1]]
		if id < bm.count {
			want := before.Tables["glyf"][bm.offsets[id]:bm.offsets[id+1]]
			if !bytes.Equal(raw, want) {
				t.Fatalf("primary glyph %d changed", id)
			}
		} else {
			stripped, err := sfntio.StripGlyphHints(raw)
			if err != nil || !bytes.Equal(raw, stripped) {
				t.Fatalf("foreign glyph %d retains hints: %v", id, err)
			}
		}
	}
	outputFace, err := p.HB.NewFace(merged, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer outputFace.Close()
	outputInfo, err := inventory.InspectFace(merged)
	if err != nil {
		t.Fatal(err)
	}
	for i, source := range append([][]byte{base}, donors...) {
		cp := rune('A' + i)
		info, err := inventory.InspectFace(source)
		if err != nil {
			t.Fatal(err)
		}
		sourceFace, err := p.HB.NewFace(source, 0)
		if err != nil {
			t.Fatal(err)
		}
		want, err := sourceFace.Draw(uint32(info.Cmap[cp]), 0)
		sourceFace.Close()
		if err != nil {
			t.Fatal(err)
		}
		if info.UPM == 2000 {
			for j := range want {
				for k := range want[j].Points {
					want[j].Points[k] /= 2
				}
			}
		}
		got, err := outputFace.Draw(uint32(outputInfo.Cmap[cp]), 0)
		if err != nil || !geometry.EqualContours(want, got) {
			t.Fatalf("%c outline changed in merge: %v", cp, err)
		}
	}
	maxpFontToolsOracle(t, merged)
}

func TestTransformRepairsCompoundProfileWithoutOutlineChanges(t *testing.T) {
	p := processor(t)
	f, err := sfntio.Parse(maxpTestFont(t, 'C', 4, 1000, 4), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, off := range []int{10, 12, 28, 30} {
		binary.BigEndian.PutUint16(f.Tables["maxp"][off:], 0)
	}
	input, err := f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, clean := range []bool{false, true} {
		t.Run(fmt.Sprintf("clean=%v", clean), func(t *testing.T) {
			out, err := p.transform(context.Background(), input, transformOptions{ttf: true, clean: clean})
			if err != nil {
				t.Fatal(err)
			}
			after, err := sfntio.Parse(out, 0)
			if err != nil {
				t.Fatal(err)
			}
			checkMaxp(t, after.Tables["maxp"], map[int]uint16{10: 20, 12: 5, 28: 4, 30: 3})
			for _, tag := range []string{"glyf", "loca", "fpgm", "prep", "cvt "} {
				if !bytes.Equal(f.Tables[tag], after.Tables[tag]) {
					t.Fatalf("%s changed during profile repair", tag)
				}
			}
			if !bytes.Equal(f.Tables["maxp"][14:28], after.Tables["maxp"][14:28]) {
				t.Fatal("hint requirements changed during profile repair")
			}
			maxpFontToolsOracle(t, out)
		})
	}
}

// Opt-in regression against a local generated archive; all outputs are temporary.
func TestLocalGeneratedCompoundProfiles(t *testing.T) {
	path := os.Getenv("HD2_MAXP_ARCHIVE")
	if path == "" {
		t.Skip("local generated font diagnostic disabled")
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	p := processor(t)
	seen := map[[32]byte]bool{}
	count := 0
	check := func(data []byte) {
		t.Helper()
		digest := sha256.Sum256(data)
		if seen[digest] {
			return
		}
		seen[digest] = true
		before, err := sfntio.Parse(data, 0)
		if err != nil {
			t.Fatal(err)
		}
		if before.Tables["glyf"] == nil {
			return
		}
		out, err := p.transform(context.Background(), data, transformOptions{ttf: true})
		if err != nil {
			t.Fatal(err)
		}
		after, err := sfntio.Parse(out, 0)
		if err != nil {
			t.Fatal(err)
		}
		for tag, raw := range before.Tables {
			if tag == "maxp" {
				continue
			}
			other := after.Tables[tag]
			if tag == "head" {
				raw, other = bytes.Clone(raw), bytes.Clone(other)
				clear(raw[8:12])
				clear(other[8:12])
			}
			if !bytes.Equal(raw, other) {
				t.Fatalf("%x: %s changed during metadata-only repair", digest[:6], tag)
			}
		}
		if !bytes.Equal(before.Tables["maxp"][14:28], after.Tables["maxp"][14:28]) {
			t.Fatal("retained instruction metadata changed")
		}
		profile := after.Tables["maxp"]
		t.Logf("%x: compound points/contours/elements/depth = %d/%d/%d/%d", digest[:6], binary.BigEndian.Uint16(profile[10:]), binary.BigEndian.Uint16(profile[12:]), binary.BigEndian.Uint16(profile[28:]), binary.BigEndian.Uint16(profile[30:]))
		maxpFontToolsOracle(t, out)
		count++
	}
	for _, member := range archive.File {
		if !strings.HasPrefix(member.Name, "Fonts/") || !strings.HasSuffix(member.Name, ".patch_0") {
			continue
		}
		stream, err := member.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(stream)
		stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		reader, err := archiveio.NewReader(bytes.NewReader(data), uint64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		index, err := archiveio.ReadIndex(reader)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range index.Entries {
			switch entry.TypeID {
			case archiveio.ResourceHash("ttf"), archiveio.ResourceHash("otf"), archiveio.ResourceHash("runtime_font"):
				payload, err := reader.Read(entry.MainOffset, uint64(entry.MainSize))
				if err != nil {
					t.Fatal(err)
				}
				if entry.TypeID == archiveio.ResourceHash("runtime_font") {
					parts, err := archiveio.RuntimeParts(payload)
					if err != nil {
						t.Fatal(err)
					}
					for _, part := range parts {
						check(part.Data)
					}
				} else {
					check(payload[16:])
				}
			}
		}
	}
	if count == 0 {
		t.Fatal("archive contained no TrueType fonts")
	}
	t.Logf("checked %d distinct TrueType fonts", count)
}
