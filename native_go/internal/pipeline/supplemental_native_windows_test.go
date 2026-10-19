package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"hd2font/native/internal/archiveio"
	"hd2font/native/internal/fontcore"
	"hd2font/native/internal/harfbuzz"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/sfntio"
)

var v13SelectedGroups = []string{"latin", "sc", "tc", "jp", "ko"}
var v13AuxiliaryGroups = []string{"arabic", "bengali", "devanagari", "thai"}
var supplementalSourceCharacters = []rune{'Z', 0x03a9, 0x0416, 0x636e, 0x4e2d, 0x570b, 0x3042, 0xac00}

func TestV13ScopeNeverSelectsAuxiliaryGroups(t *testing.T) {
	inv, options := v13ScopeFixture(t)
	options.Languages = []string{"latin", "sc", "tc", "jp", "ko"}
	generator := &fakeAtlas{}
	report, err := build(context.Background(), inv, &fakeOps{}, options, nil, generator.generate)
	if err != nil {
		t.Fatal(err)
	}
	coverage := coverageByGroup(report)
	for _, group := range v13SelectedGroups {
		if !coverage[group].Selected {
			t.Errorf("selected game group %s was omitted", group)
		}
	}
	for _, group := range append(append([]string{}, v13AuxiliaryGroups...), "emoji", "special") {
		if coverage[group].Selected {
			t.Errorf("out-of-scope group %s was automatically selected", group)
		}
	}
	files := zipContents(t, options.OutputZIP)
	for _, archive := range []string{"a000000000000001", "a000000000000002", "a000000000000003", "a000000000000004", "1111111111111111", "2222222222222222"} {
		if _, ok := files["Fonts/"+archive+".patch_0"]; ok {
			t.Errorf("out-of-scope archive %s was emitted", archive)
		}
	}
	verifyUnselectedRuntimeSlots(t, inv, files, coverage)
}

func verifyUnselectedRuntimeSlots(t *testing.T, inv *inventory.Inventory, files map[string][]byte, coverage map[string]inventory.Coverage) {
	t.Helper()
	for _, r := range inv.Resources {
		if r.Kind != "runtime_font" {
			continue
		}
		_, payload, _, _ := savedResource(t, files, r.Archive, r.Entry.NameID)
		got, err := archiveio.RuntimeParts(payload)
		if err != nil {
			t.Fatal(err)
		}
		old, err := archiveio.RuntimeParts(r.Payload)
		if err != nil {
			t.Fatal(err)
		}
		for i, face := range r.Faces {
			if coverage[face.Group].Selected {
				continue
			}
			if !bytes.Equal(got[i].Data, old[i].Data) || got[i].Metrics != old[i].Metrics {
				t.Errorf("unselected %s runtime slot %s[%d] changed", face.Group, r.Archive, i)
			}
		}
	}
}

func TestNativeV13GlyphPathScopeAndLayout(t *testing.T) {
	if os.Getenv("HD2_NATIVE_PIPELINE_TEST") != "1" {
		t.Skip("set HD2_NATIVE_PIPELINE_TEST=1 for native glyph integration")
	}
	bin, err := filepath.Abs(filepath.Join("..", "..", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	processor, err := fontcore.NewProcessor(bin, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer processor.Close()

	source, aliases := supplementalNativeSource(t)
	if err = supplementalVerifyShaping(t, processor, source); err != nil {
		t.Fatalf("fixture source does not shape AV ligature/kern: %v", err)
	}
	inv, options := v13ScopeFixture(t)
	if err = os.WriteFile(options.FontPath, source, 0600); err != nil {
		t.Fatal(err)
	}
	options.Languages = []string{"latin", "sc", "tc", "jp", "ko"}
	options.MatchWeights = false
	options.ProcessOutlines = false
	options.IncludeSymbols = false
	generator := &fakeAtlas{}
	report, err := build(context.Background(), inv, &nativeOps{Processor: processor}, options, nil, generator.generate)
	if err != nil {
		t.Fatal(err)
	}
	coverage := coverageByGroup(report)
	for _, group := range v13SelectedGroups {
		if !coverage[group].Selected {
			t.Errorf("native plan omitted %s", group)
		}
	}
	for _, group := range append(append([]string{}, v13AuxiliaryGroups...), "emoji", "special") {
		if coverage[group].Selected {
			t.Errorf("native plan selected auxiliary group %s", group)
		}
	}

	files := zipContents(t, options.OutputZIP)
	for _, r := range inv.Resources {
		if r.Kind != "ttf" || len(r.Faces) != 1 || !coverage[r.Faces[0].Group].Selected {
			continue
		}
		_, payload, _, _ := savedResource(t, files, r.Archive, r.Entry.NameID)
		if len(payload) < 16 {
			t.Fatalf("truncated native font %s", r.Archive)
		}
		face, err := inventory.InspectFace(payload[16:])
		if err != nil {
			t.Fatal(err)
		}
		for _, cp := range aliases {
			if _, ok := face.Chars[cp]; !ok {
				t.Errorf("%s native merge lost source U+%04X", r.Faces[0].Group, cp)
			}
		}
		font, err := sfntio.Parse(payload[16:], 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(font.Tables["GSUB"]) == 0 || len(font.Tables["GPOS"]) == 0 {
			t.Errorf("%s native merge lost GSUB/GPOS", r.Faces[0].Group)
		}
	}
	verifyUnselectedRuntimeSlots(t, inv, files, coverage)

	_, payload, _, _ := savedResource(t, files, "0de4b81e19a79aa8", 4)
	if err = supplementalVerifyShaping(t, processor, payload[16:]); err != nil {
		t.Fatalf("generated native font lost AV ligature/kern: %v", err)
	}
}

func supplementalNativeSource(t *testing.T) ([]byte, []rune) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "fontcore", "testdata", "layout.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	font, err := sfntio.Parse(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	aliases := []rune{'A', 'V', 0xe000, 0x4e2d, 0x636e, 0x03a9, 0x570b, 0x3042, 0xac00, 0x0645, 0x09ac, 0x0915, 0x0e01}
	cmap := make(map[rune]uint16, len(aliases))
	for i, cp := range aliases {
		gid := uint16(i%3 + 1)
		if i < 3 {
			gid = uint16(i + 1)
		}
		cmap[cp] = gid
	}
	font.Tables["cmap"] = sfntio.UnicodeCmap(cmap)
	source, err := font.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	face, err := inventory.InspectFace(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, cp := range aliases {
		if _, ok := face.Chars[cp]; !ok {
			t.Fatalf("aliased layout source lost U+%04X", cp)
		}
	}
	return source, aliases
}

func supplementalVerifyShaping(t *testing.T, processor *fontcore.Processor, raw []byte) error {
	t.Helper()
	face, err := inventory.InspectFace(raw)
	if err != nil {
		return err
	}
	ligature, ok := face.Cmap[0xe000]
	if !ok {
		return fmt.Errorf("missing U+E000 ligature mapping")
	}
	lig := supplementalShape(t, processor, raw, "AV", "liga=1", "kern=1")
	if len(lig) != 1 || lig[0].ID != uint32(ligature) {
		return fmt.Errorf("AV ligature shaped as %v, want U+E000 glyph %d", lig, ligature)
	}
	kernOn := supplementalShape(t, processor, raw, "AV", "liga=0", "kern=1")
	kernOff := supplementalShape(t, processor, raw, "AV", "liga=0", "kern=0")
	if len(kernOn) != 2 || len(kernOff) != 2 {
		return fmt.Errorf("AV without liga must shape as two glyphs: on=%v off=%v", kernOn, kernOff)
	}
	if kernOn[0].XAdvance == kernOff[0].XAdvance {
		return fmt.Errorf("GPOS A/V kern has no effect: on=%v off=%v", kernOn, kernOff)
	}
	if !slices.Equal([]uint32{kernOn[0].ID, kernOn[1].ID}, []uint32{kernOff[0].ID, kernOff[1].ID}) {
		return fmt.Errorf("kerning changed glyph identity: on=%v off=%v", kernOn, kernOff)
	}
	return nil
}

func supplementalShape(t *testing.T, processor *fontcore.Processor, raw []byte, text string, features ...string) []harfbuzz.Glyph {
	t.Helper()
	face, err := processor.HB.NewFace(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer face.Close()
	glyphs, err := face.Shape(text, harfbuzz.ShapeOptions{Script: "Latn", Language: "en", Direction: "ltr", Features: features})
	if err != nil {
		t.Fatal(err)
	}
	return glyphs
}
