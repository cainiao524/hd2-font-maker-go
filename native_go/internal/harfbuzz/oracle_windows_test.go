//go:build windows && amd64

package harfbuzz

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"hd2font/native/internal/geometry"
)

func extra(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestShapeAndOffsetLayout(t *testing.T) {
	e := engine(t)
	for _, name := range []string{"donor.ttf", "donor.otf", "cid.otf"} {
		t.Run(name, func(t *testing.T) {
			var data []byte
			if name == "cid.otf" {
				data = extra(t, name)
			} else {
				data = fixture(t, name)
			}
			out, actual, err := e.Remap(data, map[uint32]uint32{1: 4, 2: 5})
			if err != nil {
				t.Fatal(err)
			}
			for _, features := range [][]string{nil, {"liga=0", "kern=1"}, {"liga=0", "kern=0"}} {
				options := ShapeOptions{Script: "Latn", Language: "en", Direction: "ltr", Features: features}
				before, err := e.Shape(data, "BW", options)
				if err != nil {
					t.Fatal(err)
				}
				after, err := e.Shape(out, "BW", options)
				if err != nil {
					t.Fatal(err)
				}
				for i := range before {
					before[i].ID = actual[before[i].ID]
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("shape mismatch: %v != %v", before, after)
				}
				if len(features) == 0 && (len(after) != 1 || after[0].ID != 6) {
					t.Fatalf("ligature not applied: %v", after)
				}
				if len(features) != 0 && (len(after) != 2 || after[0].ID != 4 || after[1].ID != 5) {
					t.Fatalf("unexpected glyph IDs %v", after)
				}
				if len(features) != 0 && features[1] == "kern=1" && after[0].XAdvance != 480 {
					t.Fatalf("kerning not applied: %v", after)
				}
			}
		})
	}
	data := fixture(t, "base.ttf")
	if out, err := e.Shape(data, "", ShapeOptions{}); err != nil || len(out) != 0 {
		t.Fatalf("empty shape %v %v", out, err)
	}
	if _, err := e.Shape(data, "AV", ShapeOptions{Features: []string{"bad==value"}}); err == nil {
		t.Fatal("accepted invalid feature")
	}
	if _, err := e.Shape(data, "AV", ShapeOptions{Direction: "invalid"}); err == nil {
		t.Fatal("accepted invalid direction")
	}
}

func TestAllAxesPinnedAndCollection(t *testing.T) {
	e := engine(t)
	for _, tc := range []struct {
		name string
		axes map[string]float32
	}{
		{"default", nil}, {"weight", map[string]float32{"wght": 900}},
		{"both", map[string]float32{"wght": 900, "wdth": 125}},
	} {
		out, err := e.Instance(extra(t, "two-axis.ttf"), 0, tc.axes)
		if err != nil {
			t.Fatal(err)
		}
		assertStatic(t, out)
		want := extra(t, "two-axis-"+tc.name+".ttf")
		for gid := uint32(0); gid < 4; gid++ {
			a, err := e.NativeDraw(out, gid, 0)
			if err != nil {
				t.Fatal(err)
			}
			b, err := e.NativeDraw(want, gid, 0)
			if err != nil || !reflect.DeepEqual(a, b) {
				t.Fatalf("two-axis %s glyph %d: %v", tc.name, gid, err)
			}
		}
		if !reflect.DeepEqual(parsed(t, out).Tables["hmtx"], parsed(t, want).Tables["hmtx"]) {
			t.Fatalf("two-axis %s metrics", tc.name)
		}
	}
	out, err := e.Instance(extra(t, "collection.ttc"), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	shaped, err := e.Shape(out, "BW", ShapeOptions{})
	if err != nil || len(shaped) != 1 || shaped[0].ID != 3 {
		t.Fatalf("collection face 1: %v %v", shaped, err)
	}
	if _, err := e.Instance(extra(t, "collection.ttc"), 2, nil); err == nil {
		t.Fatal("accepted invalid collection face index")
	}
	out, err = e.Instance(extra(t, "variable-cff2.otf"), 0, map[string]float32{"wght": 900})
	if err != nil {
		t.Fatal(err)
	}
	assertStatic(t, out)
	if len(parsed(t, out).Tables["CFF "]) == 0 {
		t.Fatal("variable CFF2 did not downgrade")
	}
}

type oracleCase struct {
	Name           string
	Data, Expected []byte
	Paths          []geometry.Path
}

func TestFontToolsOracle(t *testing.T) {
	python := os.Getenv("HD2_FONTTOOLS_PYTHON")
	if python == "" {
		t.Skip("set HD2_FONTTOOLS_PYTHON to run the independent test-only FontTools oracle")
	}
	e := engine(t)
	var cases []oracleCase
	add := func(name string, data, expected []byte) {
		t.Helper()
		f, err := e.NewFace(data, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		count := binary.BigEndian.Uint16(parsed(t, data).Tables["maxp"][4:])
		c := oracleCase{Name: name, Data: data, Expected: expected}
		for gid := uint32(0); gid < uint32(count); gid++ {
			path, err := f.Draw(gid, 0)
			if err != nil {
				t.Fatalf("%s glyph %d: %v", name, gid, err)
			}
			c.Paths = append(c.Paths, path)
		}
		cases = append(cases, c)
	}
	for _, name := range []string{"donor.ttf", "donor.otf", "cff2.otf"} {
		data := fixture(t, name)
		add("draw-"+name, data, nil)
		if name == "cff2.otf" {
			out, err := e.Instance(data, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			add("converted-cff2", out, fixture(t, "base.otf"))
			continue
		}
		out, _, err := e.Remap(data, map[uint32]uint32{1: 4, 2: 5})
		if err != nil {
			t.Fatal(err)
		}
		add("offset-"+name, out, nil)
	}
	for _, name := range []string{"cid.otf", "composites.ttf"} {
		add("draw-"+name, extra(t, name), nil)
	}
	data := extra(t, "cid.otf")
	out, _, err := e.Remap(data, map[uint32]uint32{1: 4, 2: 5})
	if err != nil {
		t.Fatal(err)
	}
	add("offset-cid", out, nil)
	out, err = e.Subset(data, []uint32{66, 87})
	if err != nil {
		t.Fatal(err)
	}
	add("cid-subset", out, nil)
	out, err = e.Instance(extra(t, "variable-cff2.otf"), 0, map[string]float32{"wght": 900})
	if err != nil {
		t.Fatal(err)
	}
	add("variable-cff2-instance", out, extra(t, "variable-cff2-900.otf"))
	for _, tc := range []struct {
		name string
		axes map[string]float32
	}{
		{"default", nil}, {"weight", map[string]float32{"wght": 900}},
		{"both", map[string]float32{"wght": 900, "wdth": 125}},
	} {
		out, err := e.Instance(extra(t, "two-axis.ttf"), 0, tc.axes)
		if err != nil {
			t.Fatal(err)
		}
		add("two-axis-"+tc.name, out, extra(t, "two-axis-"+tc.name+".ttf"))
	}
	request, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, "oracle.py", "verify")
	cmd.Stdin = bytes.NewReader(request)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("FontTools oracle: %v\n%s", err, output)
	}
	if path := os.Getenv("HD2_HARFBUZZ_ORACLE_REPORT"); path != "" {
		if err := os.WriteFile(path, output, 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("FontTools independently validated %d font cases, including GSUB/GPOS/GDEF offset IDs and outlines", len(cases))
}

func BenchmarkSharedFaceDraw(b *testing.B) {
	e, err := Open(filepath.Join("..", "..", "bin"))
	if err != nil {
		b.Fatal(err)
	}
	defer e.Close()
	data, err := os.ReadFile(filepath.Join("testdata", "composites.ttf"))
	if err != nil {
		b.Fatal(err)
	}
	f, err := e.NewFace(data, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := f.Draw(6, 0); err != nil {
			b.Fatal(err)
		}
	}
}
