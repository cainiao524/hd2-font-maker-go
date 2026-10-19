package outlinecleanup

import (
	"encoding/json"
	"math"
	"os"
	"slices"
	"strings"
	"testing"

	"hd2font/native/internal/geometry"
)

func TestPythonOracle(t *testing.T) {
	b, err := os.ReadFile("testdata/oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		SHA256 string `json:"sha256"`
		Cases  []struct {
			Name            string
			Input, Expected geometry.Path
		}
	}
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Cases) < 200 || len(data.SHA256) != 64 {
		t.Fatal("incomplete oracle fixture")
	}
	for _, tc := range data.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			before := slices.Clone(tc.Input)
			got, err := Clean(tc.Input)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.Expected) {
				t.Fatalf("oracle mismatch\ngot  %v\nwant %v", got, tc.Expected)
			}
			if !slices.Equal(tc.Input, before) {
				t.Fatal("input mutated")
			}
			again, err := Clean(got)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(again, got) {
				t.Fatalf("not stable\nfirst %v\nagain %v", got, again)
			}
		})
	}
}

func TestRejectMalformedPath(t *testing.T) {
	move := geometry.Command{Verb: geometry.Move}
	line := geometry.Command{Verb: geometry.Line, Points: [6]float32{2, 3}}
	close := geometry.Command{Verb: geometry.Close}
	cases := []struct {
		name string
		path geometry.Path
	}{
		{"unknown-verb", geometry.Path{{Verb: 5}}},
		{"line-without-move", geometry.Path{line}},
		{"quad-without-move", geometry.Path{{Verb: geometry.Quad}}},
		{"cubic-without-move", geometry.Path{{Verb: geometry.Cubic}}},
		{"close-without-move", geometry.Path{close}},
		{"nested-move", geometry.Path{move, line, move, close}},
		{"unclosed", geometry.Path{move, line}},
		{"move-only", geometry.Path{move}},
		{"double-close", geometry.Path{move, close, close}},
		{"line-after-close", geometry.Path{move, close, line}},
		{"nan", geometry.Path{{Verb: geometry.Move, Points: [6]float32{float32(math.NaN())}}, close}},
		{"positive-infinity", geometry.Path{move, {Verb: geometry.Quad, Points: [6]float32{0, 0, 1, float32(math.Inf(1))}}, close}},
		{"negative-infinity", geometry.Path{move, {Verb: geometry.Cubic, Points: [6]float32{0, 0, float32(math.Inf(-1))}}, close}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Clean(tc.path)
			if err == nil || got != nil {
				t.Fatalf("expected error and no partial output, got %v, %v", got, err)
			}
		})
	}
}

func TestConvergenceThreshold(t *testing.T) {
	a := line(point{}, point{1, 0})
	for _, tc := range []struct {
		y    float64
		want bool
	}{{0, true}, {0.01414, true}, {0.01415, false}, {1, false}} {
		b := line(point{1, 0}, point{0, tc.y})
		if got := convergent(a, b); got != tc.want {
			t.Errorf("dy=%g: got %v, want %v", tc.y, got, tc.want)
		}
	}
	if convergent(line(point{}, point{}), a) {
		t.Fatal("zero tangent converges")
	}
}

func TestFlattenRejectsUnboundedWork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		size    float32
		message string
	}{
		{"depth", math.MaxFloat32, "subdivisions"},
		{"segments", 1e10, "flattened segments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := geometry.Path{{Verb: geometry.Move}, {Verb: geometry.Cubic, Points: [6]float32{0, 0, tc.size, tc.size, tc.size, 0}}, {Verb: geometry.Close}}
			got, err := Clean(p)
			if err == nil || !strings.Contains(err.Error(), tc.message) || got != nil {
				t.Fatalf("expected %s limit, got %v", tc.message, err)
			}
		})
	}
}

func TestFlattenBoundsRetracingProjection(t *testing.T) {
	s := segment{p: [4]point{{0, 0}, {30, 0}, {-30, 0}, {1, 0}}, n: 4}
	points, err := flatten(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) <= 2 {
		t.Fatal("collinear retrace was reduced to its endpoint chord")
	}
}
