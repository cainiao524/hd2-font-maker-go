package cffconv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"testing"
	"time"

	"hd2font/native/internal/geometry"
)

type oracleGlyph struct {
	Label   string
	Path    geometry.Path
	Advance uint16
	Reverse bool
}

func oracleFixtures() []oracleGlyph {
	fixtures := []oracleGlyph{{Label: "empty", Advance: 0}, {Label: "space", Advance: 600}, {Label: "max-width", Advance: 32767}}
	add := func(label string, path geometry.Path) {
		fixtures = append(fixtures, oracleGlyph{label, path, 613, false}, oracleGlyph{label + "-reversed", path, 613, true})
	}
	var p geometry.Path
	p.MoveTo(-0.5, -10.5)
	p.LineTo(30.5, -10.5)
	p.LineTo(30.5, 80.5)
	p.LineTo(-0.5, 80.5)
	p.Close()
	add("implicit-closing-line", p)
	p = nil
	p.MoveTo(-10.5, -20.5)
	p.QuadTo(-50.5, 125.5, 75.5, 100.5)
	p.QuadTo(120.5, -50.5, -10.5, -20.5)
	p.Close()
	add("quadratic-closure", p)
	p = nil
	p.MoveTo(0.5, -0.5)
	p.QuadTo(1.5, -1.5, 3.5, 2.5)
	p.CubeTo(-2.5, -3.5, -8.5, 12.5, 0.5, -0.5)
	p.Close()
	p.MoveTo(-50.5, -40.5)
	p.LineTo(-50.5, -40.5)
	p.LineTo(-20.5, -10.5)
	p.Close()
	add("mixed-curves-multiple-contours", p)
	p = nil
	p.MoveTo(21.5, 32.5)
	p.Close()
	add("point-contour", p)
	p = nil
	p.MoveTo(0, 0)
	p.LineTo(5, 0)
	p.LineTo(0, 0)
	p.Close()
	add("explicit-line-closure", p)
	p = nil
	p.MoveTo(2, 3)
	p.QuadTo(9, 18, -4, 11)
	add("implicit-end-closure", p)
	// Seeded synthetic contours expose rounding drift and contour-current-point
	// mistakes without depending on any installed or game-provided fonts.
	rng := rand.New(rand.NewPCG(37, 91))
	coordinate := func() float64 { return float64(rng.IntN(2001)-1000) / 4 }
	for i := 0; i < 80; i++ {
		p = nil
		for contour := 0; contour < 3; contour++ {
			sx, sy := coordinate(), coordinate()
			p.MoveTo(sx, sy)
			for j := 0; j < 12; j++ {
				switch rng.IntN(3) {
				case 0:
					p.LineTo(coordinate(), coordinate())
				case 1:
					p.QuadTo(coordinate(), coordinate(), coordinate(), coordinate())
				case 2:
					p.CubeTo(coordinate(), coordinate(), coordinate(), coordinate(), coordinate(), coordinate())
				}
			}
			if i%2 == 0 {
				p.QuadTo(coordinate(), coordinate(), sx, sy)
			}
			p.Close()
		}
		add(fmt.Sprintf("seeded-%d", i), p)
	}
	return fixtures
}

func TestFontToolsOracle(t *testing.T) {
	python := os.Getenv("CFFCONV_ORACLE_PYTHON")
	if python == "" {
		t.Skip("set CFFCONV_ORACLE_PYTHON to a development Python with fontTools")
	}
	fixtures := oracleFixtures()
	charstrings := make([][]byte, len(fixtures))
	for i, fixture := range fixtures {
		var err error
		charstrings[i], err = Encode(fixture.Path, fixture.Advance, fixture.Reverse)
		if err != nil {
			t.Fatalf("%s: %v", fixture.Label, err)
		}
	}
	type oracleTable struct {
		Name   string
		Data   []byte
		BBox   [4]int16
		UPM    uint16
		Glyphs []oracleGlyph
	}
	request := struct{ Tables []oracleTable }{}
	for _, upm := range []uint16{1000, 2048, 16384} {
		bbox := [4]int16{-1000, -1000, 1000, 1000}
		name := fmt.Sprintf("Synthetic%d", upm)
		data, err := Table(name, charstrings, bbox, upm)
		if err != nil {
			t.Fatal(err)
		}
		request.Tables = append(request.Tables, oracleTable{name, data, bbox, upm, fixtures})
	}
	single, err := Table("OnlyNotdef", charstrings[:1], [4]int16{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	request.Tables = append(request.Tables, oracleTable{"OnlyNotdef", single, [4]int16{}, 1000, fixtures[:1]})
	input, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "testdata/fonttools_oracle.py")
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fontTools oracle: %v\n%s", err, output)
	}
	t.Log(string(output))
}
