package pipeline

import (
	"context"
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
