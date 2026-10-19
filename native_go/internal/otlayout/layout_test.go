package otlayout

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"hd2font/native/internal/sfntio"
)

func TestScaleFontToolsOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/scale.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Tag     string
		Ratio         float64
		Input, Scaled []byte
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := Scale(c.Tag, c.Input, c.Ratio)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, c.Scaled) {
				t.Fatalf("scaled table differs from FontTools (%d / %d bytes)", len(got), len(c.Scaled))
			}
		})
	}
}
func loadPart(t *testing.T, path string) Part {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := sfntio.Parse(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	return Part{f.Tables["GSUB"], f.Tables["GPOS"], f.Tables["GDEF"]}
}
func TestMergeFeatureNamespaces(t *testing.T) {
	parts := []Part{loadPart(t, "testdata/base.ttf"), loadPart(t, "testdata/donor.ttf")}
	merged, err := Merge(parts)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"GSUB", "GPOS"} {
		b := buffer{data: merged[tag]}
		scripts := map[string]*script{}
		features := readFeatures(&b, 0)
		readScripts(&b, features, scripts)
		if b.err != nil {
			t.Fatal(b.err)
		}
		if len(scripts["DFLT"].languages[""].features) == 0 {
			t.Fatal("lost default language features")
		}
		for _, f := range features {
			if len(f.lookups) != 2 {
				t.Fatalf("%s feature %s lost donor lookups: %v", tag, f.tag, f.lookups)
			}
		}
	}
	g := buffer{data: merged["GDEF"]}
	mark := g.u16(12)
	if g.u16(mark+2) != 2 {
		t.Fatal("lost mark filtering set")
	}
	if c := classes(&g, g.u16(10)); c[7] == c[8] || c[7] == 0 || c[8] == 0 {
		t.Fatalf("mark attachment classes not separated: %v", c)
	}
}
