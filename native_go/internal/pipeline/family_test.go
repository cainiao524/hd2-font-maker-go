package pipeline

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func familyFixture(t *testing.T) Options {
	t.Helper()
	_, options := pipelineFixture(t)
	raw, err := syntheticFont("Synthetic Pixel Bold", 700, 256, []rune{'?', 'A', 0x4e2d, 0x570b})
	if err != nil {
		t.Fatal(err)
	}
	bold := filepath.Join(t.TempDir(), "custom-bold.ttf")
	if err = os.WriteFile(bold, raw, 0600); err != nil {
		t.Fatal(err)
	}
	options.Family = []FontFace{{Path: bold}}
	options.Languages = []string{"sc", "tc", "latin"}
	return options
}

func TestFamilyFacesChooseWeights(t *testing.T) {
	inv, _ := pipelineFixture(t)
	options := familyFixture(t)
	ops := &fakeOps{}
	report, err := build(context.Background(), inv, ops, options, nil, (&fakeAtlas{}).generate)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.FamilyFaces) != 2 {
		t.Fatalf("family faces: %v", report.FamilyFaces)
	}
	methods := map[int]string{}
	faces := map[int]string{}
	for _, style := range report.StyleAdaptation {
		methods[style.TargetWeight], faces[style.TargetWeight] = style.Method, style.SourceName
	}
	if methods[700] != "family_face" || faces[700] != "Synthetic Pixel Bold" {
		t.Errorf("700 should use the Bold face as drawn: %s %s", methods[700], faces[700])
	}
	if methods[400] != "family_face" || faces[400] != "Synthetic Pixel" {
		t.Errorf("400 should use the Regular face as drawn: %s %s", methods[400], faces[400])
	}
	if methods[500] != "family_synthetic_emphasis" {
		t.Errorf("500 should embolden Regular: %s", methods[500])
	}
	for _, row := range report.Coverage {
		// U+570B is only in the Bold sibling; the family still covers it.
		if row.Group == "tc" && row.CustomCharacters != 3 {
			t.Errorf("tc coverage should count the sibling's glyphs: %d", row.CustomCharacters)
		}
	}
	if len(ops.styles) != 1 || ops.styles[0].weight != 500 {
		t.Errorf("emphasis should cover exactly 400 to 500: %+v", ops.styles)
	}
}

func TestDuplicateFamilyUsesSingleMediumAnchor(t *testing.T) {
	inv, options := pipelineFixture(t)
	raw, err := os.ReadFile(options.FontPath)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "copy.ttf")
	if err := os.WriteFile(copyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	options.Family = []FontFace{{Path: copyPath}}
	options.WeightFaces = map[int]int{500: 1}
	options.Languages = []string{"sc", "tc", "latin"}
	ops := &fakeOps{}
	report, err := build(context.Background(), inv, ops, options, nil, (&fakeAtlas{}).generate)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.FamilyFaces) != 0 {
		t.Fatalf("identical source copies became a family: %v", report.FamilyFaces)
	}
	methods := map[int]string{}
	for _, row := range report.StyleAdaptation {
		methods[row.TargetWeight] = row.Method
	}
	if methods[400] != "synthetic_reduction" || methods[500] != "authored_static" || methods[700] != "synthetic_emphasis" {
		t.Fatalf("duplicate source bypassed Medium anchor: %v", methods)
	}
	for _, call := range ops.styles {
		if call.weight > 600 {
			t.Fatalf("duplicate copy caused full 400-to-700 synthesis: %+v", call)
		}
	}
}

func TestEqualWeightDifferentCoverageRemainsFamily(t *testing.T) {
	inv, options := pipelineFixture(t)
	raw, err := syntheticFont("Supplement", 400, 256, []rune{'?', 'A', 0x570b})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "supplement.ttf")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	options.Family = []FontFace{{Path: path}}
	options.Languages = []string{"sc", "tc", "latin"}
	report, err := build(context.Background(), inv, &fakeOps{}, options, nil, (&fakeAtlas{}).generate)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.FamilyFaces) != 2 {
		t.Fatal("distinct same-weight coverage was discarded")
	}
	for _, row := range report.Coverage {
		if row.Group == "tc" && row.CustomCharacters != 3 {
			t.Fatalf("same-weight supplemental character lost: %+v", row)
		}
	}
}

func TestCollectionUsesNativeRegularMediumAndBold(t *testing.T) {
	inv, options := pipelineFixture(t)
	weights := []int{700, 500, 400}
	data := make([]byte, 12+4*len(weights))
	copy(data, "ttcf")
	binary.BigEndian.PutUint32(data[4:], 0x10000)
	binary.BigEndian.PutUint32(data[8:], uint32(len(weights)))
	for i, weight := range weights {
		face, err := syntheticFont(fmt.Sprintf("Native %d", weight), weight, 256, []rune{'?', 'A', 0x4e2d})
		if err != nil {
			t.Fatal(err)
		}
		offset := uint32(len(data))
		binary.BigEndian.PutUint32(data[12+4*i:], offset)
		for table := 0; table < int(binary.BigEndian.Uint16(face[4:])); table++ {
			record := 12 + 16*table + 8
			binary.BigEndian.PutUint32(face[record:], binary.BigEndian.Uint32(face[record:])+offset)
		}
		data = append(data, face...)
	}
	path := filepath.Join(t.TempDir(), "family.ttc")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	options.FontPath, options.FaceIndex = path, 2
	options.Family = []FontFace{{Path: path, FaceIndex: 0}, {Path: path, FaceIndex: 1}}
	options.SingleFontWeight = 700 // A stale single-face choice must not affect the family.
	options.Languages = []string{"sc", "tc"}
	ops := &fakeOps{}
	report, err := build(context.Background(), inv, ops, options, nil, (&fakeAtlas{}).generate)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops.styles) != 0 || len(report.FamilyFaces) != 3 {
		t.Fatalf("complete collection synthesized or lost faces: styles=%+v family=%v", ops.styles, report.FamilyFaces)
	}
	for _, row := range report.StyleAdaptation {
		if row.Method != "family_face" || row.SourceName != fmt.Sprintf("Native %d", row.TargetWeight) {
			t.Fatalf("collection did not use the matching native face: %+v", row)
		}
	}
}

func TestFamilyWeightPinAndVariableFonts(t *testing.T) {
	inv, _ := pipelineFixture(t)
	options := familyFixture(t)
	options.WeightFaces = map[int]int{700: -1}
	ops := &fakeOps{}
	report, err := build(context.Background(), inv, ops, options, nil, (&fakeAtlas{}).generate)
	if err != nil {
		t.Fatal(err)
	}
	for _, style := range report.StyleAdaptation {
		if style.TargetWeight == 700 && (style.Method != "family_synthetic_emphasis" || style.SourceName == "Synthetic Pixel Bold") {
			t.Errorf("pinned synthetic 700 used %s %s", style.Method, style.SourceName)
		}
	}

	inv, _ = pipelineFixture(t)
	options = familyFixture(t)
	options.Axes = map[string]float32{"wght": 400}
	ops = &fakeOps{axes: []axis{{"wght", 100, 400, 900}}}
	report, err = build(context.Background(), inv, ops, options, nil, (&fakeAtlas{}).generate)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.FamilyFaces) != 0 {
		t.Errorf("variable font used family faces: %v", report.FamilyFaces)
	}
}
