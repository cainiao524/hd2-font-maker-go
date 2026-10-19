package fontmeta

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"hd2font/native/internal/cffconv"
	"hd2font/native/internal/geometry"
	"hd2font/native/internal/sfntio"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testfixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func parse(t *testing.T, data []byte) *sfntio.Font {
	t.Helper()
	f, err := sfntio.Parse(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func encoded(t *testing.T, f *sfntio.Font) []byte {
	t.Helper()
	data, err := f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func generatedCFF(t *testing.T) []byte {
	t.Helper()
	f := parse(t, fixture(t, "base.otf"))
	var charstrings [][]byte
	for gid := 0; gid < 4; gid++ {
		var path geometry.Path
		if gid > 0 {
			path.MoveTo(20, 0)
			path.CubeTo(100, 700, 400, 700, float64(500+gid), 0)
			path.Close()
		}
		cs, err := cffconv.Encode(path, 600, false)
		if err != nil {
			t.Fatal(err)
		}
		charstrings = append(charstrings, cs)
	}
	var err error
	f.Tables["CFF "], err = cffconv.Table("HD2Custom", charstrings, [4]int16{-10, -20, 600, 700}, 2048)
	if err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint16(f.Tables["OS/2"][62:], 0x1c0)
	binary.BigEndian.PutUint16(f.Tables["head"][44:], 0x1c)
	return encoded(t, f)
}

func TestRegisterPreservesTablesAndCopiesNames(t *testing.T) {
	for _, source := range [][]byte{fixture(t, "donor.ttf"), generatedCFF(t)} {
		for _, name := range []string{"template-ttf.ttf", "template-cff.otf", "template-cid.otf"} {
			t.Run(fmt.Sprintf("%x-%s", source[:4], name), func(t *testing.T) {
				template := read(t, filepath.Join("testdata", name))
				before := append([]byte(nil), source...)
				original := append([]byte(nil), template...)
				out, err := Register(source, template)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(source, before) || !bytes.Equal(template, original) {
					t.Fatal("mutated input")
				}
				a, b := parse(t, source), parse(t, out)
				for tag, table := range a.Tables {
					if tag != "name" && tag != "OS/2" && tag != "head" && tag != "CFF " && !bytes.Equal(table, b.Tables[tag]) {
						t.Fatalf("changed %s", tag)
					}
				}
				names, err := readNames(b.Tables["name"])
				if err != nil {
					t.Fatal(err)
				}
				hash, refHash := sha256.Sum256(source), sha256.Sum256(template)
				if got, want := debugName(names, 3), fmt.Sprintf("HD2Custom-%x-%x", hash[:8], refHash[:6]); got != want {
					t.Fatalf("unique ID %q != %q", got, want)
				}
				if a.Tables["CFF "] != nil {
					x, e := parseCFF(a.Tables["CFF "])
					if e != nil {
						t.Fatal(e)
					}
					y, e := parseCFF(b.Tables["CFF "])
					if e != nil {
						t.Fatal(e)
					}
					if !bytes.Equal(a.Tables["CFF "][x.body:], b.Tables["CFF "][y.body:]) {
						t.Fatal("CFF body bytes changed")
					}
				}
			})
		}
	}
}

func TestAxes(t *testing.T) {
	got, err := Axes(fixture(t, "variable.ttf"), 0)
	want := []Axis{{"wght", 100, 400, 900}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("axes %v %v", got, err)
	}
	got, err = Axes(read(t, filepath.Join("..", "harfbuzz", "testdata", "two-axis.ttf")), 0)
	if err != nil || !reflect.DeepEqual(got, []Axis{{"wght", 100, 400, 900}, {"wdth", 75, 100, 125}}) {
		t.Fatalf("two axes %v %v", got, err)
	}
	if got, err = Axes(fixture(t, "base.ttf"), 0); err != nil || len(got) != 0 {
		t.Fatalf("static axes %v %v", got, err)
	}
	if got, err = Axes(read(t, filepath.Join("..", "harfbuzz", "testdata", "collection.ttc")), 1); err != nil || len(got) != 0 {
		t.Fatalf("collection axes %v %v", got, err)
	}
	for _, index := range []int{-1, 1} {
		if _, err := Axes(fixture(t, "base.ttf"), index); err == nil {
			t.Fatal("accepted invalid face index")
		}
	}
	f := parse(t, fixture(t, "variable.ttf"))
	binary.BigEndian.PutUint16(f.Tables["fvar"][10:], 19)
	if _, err := Axes(encoded(t, f), 0); err == nil {
		t.Fatal("accepted short fvar axis record")
	}
}

func TestNameLanguageTags(t *testing.T) {
	records := []nameRecord{{platform: 0, encoding: 4, language: 0x8000, id: 1, value: encodeUnicode("Family"), languageTag: encodeUnicode("zh-Hant")},
		{platform: 3, encoding: 1, language: 0x409, id: 6, value: encodeUnicode("FamilyPS")}}
	b, err := writeNames(records)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readNames(b)
	if err != nil || !reflect.DeepEqual(got, records) {
		t.Fatalf("language tags %v %v", got, err)
	}
}

func TestRegisterRejectsUnsupportedOutputAndMalformedTables(t *testing.T) {
	ref := fixture(t, "base.ttf")
	if _, err := Register([]byte("bad"), ref); err == nil {
		t.Fatal("accepted malformed SFNT")
	}
	if _, err := Register(read(t, filepath.Join("..", "harfbuzz", "testdata", "cid.otf")), ref); err == nil {
		t.Fatal("accepted unsupported CID output")
	}
	f := parse(t, ref)
	f.Tables["name"] = []byte{0, 0}
	if _, err := Register(encoded(t, f), ref); err == nil {
		t.Fatal("accepted truncated name table")
	}
	for _, b := range [][]byte{{1, 0, 4, 4}, {1, 0, 4, 4, 0, 1, 0}, {1, 0, 4, 4, 0, 1, 4, 255, 255, 255, 255}} {
		if _, err := parseCFF(b); err == nil {
			t.Fatal("accepted malformed CFF")
		}
	}
	if len(standardStrings) != 391 || standardStrings[0] != ".notdef" || standardStrings[390] != "Semibold" {
		t.Fatal("CFF standard string data")
	}
}

func TestOriginalFontToolsRegistrationOracle(t *testing.T) {
	python := os.Getenv("HD2_FONTTOOLS_PYTHON")
	if python == "" {
		t.Skip("set HD2_FONTTOOLS_PYTHON for the independent original implementation oracle")
	}
	type oracleCase struct {
		Name                     string
		Source, Template, Native []byte
	}
	var cases []oracleCase
	for _, kind := range []string{"ttf", "cff"} {
		source := fixture(t, "donor.ttf")
		if kind == "cff" {
			source = generatedCFF(t)
		}
		for _, name := range []string{"template-ttf.ttf", "template-cff.otf", "template-cid.otf"} {
			template := read(t, filepath.Join("testdata", name))
			out, err := Register(source, template)
			if err != nil {
				t.Fatal(err)
			}
			cases = append(cases, oracleCase{kind + "-" + name, source, template, out})
		}
	}
	data, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, "oracle.py", "verify")
	cmd.Stdin = bytes.NewReader(data)
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("FontTools oracle: %v\n%s\n%s", err, output, diagnostics.Bytes())
	}
	if path := os.Getenv("HD2_FONTMETA_ORACLE_REPORT"); path != "" {
		if err := os.WriteFile(path, output, 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("original fonts.register_font matches in %d TTF/CFF/template combinations", len(cases))
}
