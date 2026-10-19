package cffconv

import (
	"bytes"
	"encoding/hex"
	"math"
	"testing"

	"hd2font/native/internal/geometry"
)

func TestType2IntegerEncoding(t *testing.T) {
	for _, tc := range []struct {
		value int32
		hex   string
	}{
		{-32768, "1c8000"}, {-1132, "1cfb94"}, {-1131, "feff"}, {-108, "fb00"},
		{-107, "20"}, {-1, "8a"}, {0, "8b"}, {107, "f6"}, {108, "f700"},
		{1131, "faff"}, {1132, "1c046c"}, {32767, "1c7fff"},
	} {
		if got := hex.EncodeToString(appendInt(nil, tc.value)); got != tc.hex {
			t.Errorf("%d: %s want %s", tc.value, got, tc.hex)
		}
	}
}

func TestEncodeEmptyGlyph(t *testing.T) {
	for _, tc := range []struct {
		width uint16
		hex   string
	}{{0, "8b0e"}, {600, "f8ec0e"}, {32767, "1c7fff0e"}} {
		for _, reverse := range []bool{false, true} {
			got, err := Encode(nil, tc.width, reverse)
			if err != nil || hex.EncodeToString(got) != tc.hex {
				t.Fatalf("width=%d reverse=%v: %x %v", tc.width, reverse, got, err)
			}
		}
	}
}

func TestEncodeRoundsAbsoluteQuadraticControls(t *testing.T) {
	var path geometry.Path
	path.MoveTo(0.5, -0.5)
	path.QuadTo(1.5, -1.5, 3.5, 2.5)
	path.Close()
	before := append(geometry.Path(nil), path...)
	got, err := Encode(path, 600, false)
	// The elevated controls round to (1,-1),(2,0), and the end to (4,3).
	const want = "f8ec8c8b158b8a8c8c8d8e080e"
	if err != nil || hex.EncodeToString(got) != want {
		t.Fatalf("%x %v want %s", got, err, want)
	}
	for i := range path {
		if path[i] != before[i] {
			t.Fatal("input path was mutated")
		}
	}
	path = nil
	path.MoveTo(-1.5, -2.5)
	path.LineTo(-0.5, -3.5)
	got, err = Encode(path, 0, false)
	if err != nil || hex.EncodeToString(got) != "8b8a89158c8a050e" {
		t.Fatalf("negative half rounding: %x %v", got, err)
	}
}

func TestEncodeReversedContourAndCurrentPoint(t *testing.T) {
	var path geometry.Path
	path.MoveTo(0, 0)
	path.LineTo(0, 10)
	path.LineTo(10, 10)
	path.LineTo(10, 0)
	path.Close()
	got, err := Encode(path, 600, true)
	if err != nil || hex.EncodeToString(got) != "f8ec8b8b15958b058b9505818b050e" {
		t.Fatalf("reversed square: %x %v", got, err)
	}
	path.MoveTo(20, 20)
	path.LineTo(21, 21)
	path.Close()
	got, err = Encode(path, 600, false)
	// The first close is implicit: the current point stays at its final (10,0).
	if err != nil || !bytes.Contains(got, []byte{149, 159, 21, 140, 140, 5, 14}) {
		t.Fatalf("current point after close: %x %v", got, err)
	}
}

func TestEncodeRejectsInvalidInput(t *testing.T) {
	move := geometry.Command{Verb: geometry.Move}
	for name, path := range map[string]geometry.Path{
		"no initial move":  {{Verb: geometry.Line}},
		"orphan close":     {{Verb: geometry.Close}},
		"line after close": {move, {Verb: geometry.Close}, {Verb: geometry.Line}},
		"unknown verb":     {move, {Verb: 99}},
		"nan":              {{Verb: geometry.Move, Points: [6]float32{float32(math.NaN())}}},
		"infinity":         {move, {Verb: geometry.Quad, Points: [6]float32{0, 0, float32(math.Inf(1))}}},
		"huge absolute":    {{Verb: geometry.Move, Points: [6]float32{math.MaxFloat32}}},
		"large delta":      {{Verb: geometry.Move, Points: [6]float32{-20000, 0}}, {Verb: geometry.Line, Points: [6]float32{20000, 0}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Encode(path, 600, false); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
	for _, width := range []uint16{32768, 65535} {
		if _, err := Encode(nil, width, false); err == nil {
			t.Fatal("accepted width outside literal range")
		}
	}
	path := make(geometry.Path, 22001)
	path[0] = move
	for i := 1; i < len(path); i++ {
		path[i].Verb = geometry.Line
	}
	if _, err := Encode(path, 600, false); err == nil {
		t.Fatal("accepted charstring exceeding 65535 bytes")
	}
}

func benchmarkPath() geometry.Path {
	var path geometry.Path
	path.MoveTo(-10, 0)
	for i := 0; i < 64; i++ {
		path.QuadTo(float64(i*7), 100.5, float64(i*7+3), 0.5)
	}
	path.QuadTo(-30, -100, -10, 0)
	path.Close()
	return path
}

func BenchmarkEncodeQuadratic(b *testing.B) {
	path := benchmarkPath()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Encode(path, 600, true); err != nil {
			b.Fatal(err)
		}
	}
}
