package sfntio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"hd2font/native/internal/geometry"
)

type compoundPathCase struct {
	name   string
	glyphs [][]byte
	id     uint16
	want   geometry.Path
}

// Keep the original integer control point 101: rounding the transformed point
// before passing it to the path would turn 50.5 into 51 (and 25.25 into 25).
func compoundPathCurve() []byte {
	data := simpleTestGlyph([]glyphTestPoint{{11, -3}, {101, 103}, {203, -3}})
	data[15] &^= 1 // Point 1 is an off-curve quadratic control point.
	return data
}

func compoundPathFixtures() []compoundPathCase {
	curve := compoundPathCurve()
	makeComposite := func(component testComponent) []byte {
		// Deliberately unrelated header bounds: drawing must use the points,
		// not a bbox translation. Instructions must not alter the path.
		return compositeTestGlyph([]testComponent{component}, [4]int16{-777, -91, 1234, 888}, []byte{0xb0, 0, 0x2e})
	}
	var cases []compoundPathCase
	for _, transform := range []struct {
		name string
		raw  []int16
	}{
		{"translate", nil},
		{"half", []int16{0x2000}},
		{"xy", []int16{0x2000, 0x1000}},
		{"matrix", []int16{0x2000, 0x1000, -0x3000, 0x6000}},
		{"reflect", []int16{-0x4000, 0x2000}},
	} {
		for _, flags := range []string{"default", "scaled", "unscaled"} {
			component := testComponent{glyph: 0, xy: true, a: -17, b: 13,
				transform: transform.raw, scaledOffset: flags == "scaled", unscaledOffset: flags == "unscaled"}
			for _, nested := range []bool{false, true} {
				components := []testComponent{component}
				glyphs := [][]byte{curve, makeComposite(component)}
				name := transform.name + "/" + flags
				if nested {
					// Signed word offsets and noncommuting transforms exercise both
					// transform composition and the translation at each level.
					outer := testComponent{glyph: 1, xy: true, words: true, a: -301, b: 457,
						transform:    []int16{0x6000, -0x1000, 0x2000, 0x3000},
						scaledOffset: flags == "scaled", unscaledOffset: flags == "unscaled"}
					components = append(components, outer)
					glyphs = append(glyphs, makeComposite(outer))
					name += "/nested"
				}
				cases = append(cases, compoundPathCase{name, glyphs, uint16(len(glyphs) - 1), compoundPathExpectedCurve(components)})
			}
		}
	}
	// Exact half- and quarter-unit expectations, with no translations.
	half := testComponent{glyph: 0, xy: true, transform: []int16{0x2000}}
	quarter := half
	quarter.glyph = 1
	cases = append(cases,
		compoundPathCase{"fraction/half", [][]byte{curve, makeComposite(half)}, 1, compoundPathExpectedCurve([]testComponent{half})},
		compoundPathCase{"fraction/quarter", [][]byte{curve, makeComposite(half), makeComposite(quarter)}, 2, compoundPathExpectedCurve([]testComponent{half, quarter})},
	)
	// Multiple uses of the same child must each keep their own transform.
	shifted := testComponent{glyph: 0, xy: true, a: 71, b: -11, transform: []int16{0x1000, 0x2000}, scaledOffset: true}
	multi := compositeTestGlyph([]testComponent{half, shifted}, [4]int16{}, nil)
	wantMulti := append(compoundPathExpectedCurve([]testComponent{half}), compoundPathExpectedCurve([]testComponent{shifted})...)
	cases = append(cases, compoundPathCase{"two components", [][]byte{curve, multi}, 1, wantMulti})
	return cases
}

// Test-side arithmetic uses successive component transforms on the original
// control points, with a single float32 conversion at the geometry boundary.
func compoundPathExpectedCurve(components []testComponent) geometry.Path {
	points := [][2]float64{{11, -3}, {101, 103}, {203, -3}}
	for _, component := range components {
		xx, xy, yx, yy := 1.0, 0.0, 0.0, 1.0
		f := func(i int) float64 { return float64(component.transform[i]) / 16384 }
		switch len(component.transform) {
		case 1:
			xx, yy = f(0), f(0)
		case 2:
			xx, yy = f(0), f(1)
		case 4:
			xx, xy, yx, yy = f(0), f(1), f(2), f(3)
		}
		for i, point := range points {
			points[i] = [2]float64{point[0]*xx + point[1]*yx + float64(component.a), point[0]*xy + point[1]*yy + float64(component.b)}
		}
	}
	var path geometry.Path
	path.MoveTo(points[0][0], points[0][1])
	path.QuadTo(points[1][0], points[1][1], points[2][0], points[2][1])
	path.Close()
	return path
}

func TestDecodeCompositeGlyphPreservesFractionalDrawCoordinates(t *testing.T) {
	for _, fixture := range compoundPathFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			glyf, offsets := buildGlyf(fixture.glyphs...)
			before, beforeOffsets := bytes.Clone(glyf), slices.Clone(offsets)
			got, err := DecodeCompositeGlyph(glyf, offsets, fixture.id)
			if err != nil {
				t.Fatal(err)
			}
			assertCompoundPathEqual(t, got, fixture.want)
			if !bytes.Equal(glyf, before) || !slices.Equal(offsets, beforeOffsets) {
				t.Fatal("drawing changed glyph bytes or offsets")
			}
			if fixture.name == "fraction/half" || fixture.name == "fraction/quarter" {
				control := [2]float32{50.5, 51.5}
				if fixture.name == "fraction/quarter" {
					control = [2]float32{25.25, 25.75}
				}
				found := false
				for _, command := range got {
					if command.Verb == geometry.Quad && command.Points[0] == control[0] && command.Points[1] == control[1] {
						found = true
					}
				}
				if !found {
					t.Fatalf("fractional control point %v was rounded or lost: %v", control, got)
				}
			}
		})
	}
}

type compoundPathEdge struct {
	verb   uint32
	start  [2]float32
	points [6]float32
}

// Convert each closed contour to directed edges, including the implicit closing
// line. Compare cyclic rotations only: controls, orientation and contour order
// remain significant. No grid rounding or geometry simplification is allowed.
func compoundPathEdges(t *testing.T, path geometry.Path) [][]compoundPathEdge {
	t.Helper()
	var contours [][]compoundPathEdge
	var edges []compoundPathEdge
	var start, current [2]float32
	open := false
	for _, command := range path {
		switch command.Verb {
		case geometry.Move:
			if open {
				t.Fatal("unclosed contour before move")
			}
			start = [2]float32{command.Points[0], command.Points[1]}
			current, open = start, true
		case geometry.Line, geometry.Quad, geometry.Cubic:
			if !open {
				t.Fatal("segment outside contour")
			}
			edge := compoundPathEdge{verb: command.Verb, start: current}
			n := map[uint32]int{geometry.Line: 2, geometry.Quad: 4, geometry.Cubic: 6}[command.Verb]
			copy(edge.points[:n], command.Points[:n])
			edges = append(edges, edge)
			current = [2]float32{command.Points[n-2], command.Points[n-1]}
		case geometry.Close:
			if !open {
				t.Fatal("close outside contour")
			}
			if current != start {
				edges = append(edges, compoundPathEdge{verb: geometry.Line, start: current, points: [6]float32{start[0], start[1]}})
			}
			contours = append(contours, edges)
			edges, open = nil, false
		default:
			t.Fatalf("unexpected path verb %d", command.Verb)
		}
	}
	if open {
		t.Fatal("unclosed final contour")
	}
	return contours
}

func assertCompoundPathEqual(t *testing.T, got, want geometry.Path) {
	t.Helper()
	a, b := compoundPathEdges(t, got), compoundPathEdges(t, want)
	if len(a) != len(b) {
		t.Fatalf("contour count %d, want %d", len(a), len(b))
	}
	for contour := range a {
		if len(a[contour]) != len(b[contour]) {
			t.Fatalf("contour %d edge count %d, want %d", contour, len(a[contour]), len(b[contour]))
		}
		matched := len(a[contour]) == 0
		for shift := 0; shift < len(a[contour]) && !matched; shift++ {
			matched = true
			for i, edge := range a[contour] {
				if edge != b[contour][(i+shift)%len(b[contour])] {
					matched = false
					break
				}
			}
		}
		if !matched {
			t.Fatalf("contour %d differs (including fractional controls/direction):\ngot  %v\nwant %v", contour, a[contour], b[contour])
		}
	}
}

func TestDecodeCompositeGlyphFontToolsDrawOracle(t *testing.T) {
	if os.Getenv("HD2_FONTTOOLS_ORACLE") == "" {
		t.Skip("set HD2_FONTTOOLS_ORACLE=1 to compare with FontTools Glyph.draw")
	}
	python := os.Getenv("HD2_FONTTOOLS_PYTHON")
	if python == "" {
		python = filepath.Join("..", "..", "..", ".venv", "Scripts", "python.exe")
	}
	if _, err := os.Stat(python); err != nil {
		t.Skipf("FontTools Python not available: %v", err)
	}
	fixtures := compoundPathFixtures()
	// Exercise the pen protocol's all-off-curve qCurveTo(..., None) case too.
	offCurve := simpleTestGlyph([]glyphTestPoint{{11, -3}, {101, 103}, {203, -3}})
	for i := 14; i < 17; i++ {
		offCurve[i] &^= 1
	}
	fixtures = append(fixtures, compoundPathCase{"all off-curve implied midpoints", [][]byte{offCurve,
		compositeTestGlyph([]testComponent{{glyph: 0, xy: true, a: -7, b: 9, transform: []int16{0x2000}}}, [4]int16{}, nil)}, 1, nil})
	requests := make([]map[string]any, len(fixtures))
	for i, fixture := range fixtures {
		glyphs := make([]string, len(fixture.glyphs))
		for j, raw := range fixture.glyphs {
			glyphs[j] = hexEncode(raw)
		}
		requests[i] = map[string]any{"glyphs": glyphs, "id": fixture.id}
	}
	input, err := json.Marshal(requests)
	if err != nil {
		t.Fatal(err)
	}
	const script = `
import json, sys
from fontTools.pens.recordingPen import DecomposingRecordingPen
from fontTools.pens.basePen import BasePen
from fontTools.ttLib.tables._g_l_y_f import Glyph, table__g_l_y_f

# This is Glyph.draw's component pen protocol, not getCoordinates. No metrics
# wrapper is used for children; each component emits its own x/y transform.
class Drawable:
    def __init__(self, table, name): self.table, self.name = table, name
    def draw(self, pen): self.table[self.name].draw(pen, self.table)

# BasePen independently expands multi-control qCurveTo and implied midpoints.
class Segments(BasePen):
    def __init__(self): super().__init__(None); self.rows = []
    def _moveTo(self, p): self.rows.append([0, *p])
    def _lineTo(self, p): self.rows.append([1, *p])
    def _qCurveToOne(self, c, p): self.rows.append([2, *c, *p])
    def _curveToOne(self, a, b, p): self.rows.append([3, *a, *b, *p])
    def _closePath(self): self.rows.append([4])
    def _endPath(self): raise AssertionError('unexpected open contour')

results = []
for case in json.load(sys.stdin):
    table = table__g_l_y_f()
    table.glyphOrder = ['g%d' % i for i in range(len(case['glyphs']))]
    table.glyphs = {name: Glyph(bytes.fromhex(raw)) for name, raw in zip(table.glyphOrder, case['glyphs'])}
    glyphSet = {name: Drawable(table, name) for name in table.glyphOrder}
    glyph = table[table.glyphOrder[case['id']]]
    assert glyph.isComposite()
    recorded = []
    for offset in (0, 733):
        pen = DecomposingRecordingPen(glyphSet)
        glyph.draw(pen, table, offset=offset)
        recorded.append(pen.value)
    # lsb-xMin is represented by offset. Glyph.draw ignores it for composites.
    assert recorded[0] == recorded[1], 'composite unexpectedly applied top-level offset'
    segments = Segments()
    pen.replay(segments)
    results.append(segments.rows)
print(json.dumps(results))
`
	cmd := exec.Command(python, "-c", script)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "ModuleNotFoundError") {
			t.Skipf("FontTools is not installed: %s", stderr.String())
		}
		t.Fatalf("FontTools Glyph.draw oracle: %v\n%s", err, stderr.String())
	}
	var results [][][]float64
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("decode FontTools result: %v\n%s", err, stdout.String())
	}
	if len(results) != len(fixtures) {
		t.Fatalf("oracle returned %d fixtures, want %d", len(results), len(fixtures))
	}
	for i, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			var want geometry.Path
			for _, row := range results[i] {
				if len(row) == 0 || len(row) > 7 {
					t.Fatal(fmt.Sprintf("invalid oracle command: %v", row))
				}
				command := geometry.Command{Verb: uint32(row[0])}
				for j, value := range row[1:] {
					command.Points[j] = float32(value)
				}
				want = append(want, command)
			}
			glyf, offsets := buildGlyf(fixture.glyphs...)
			got, err := DecodeCompositeGlyph(glyf, offsets, fixture.id)
			if err != nil {
				t.Fatal(err)
			}
			assertCompoundPathEqual(t, got, want)
		})
	}
}
