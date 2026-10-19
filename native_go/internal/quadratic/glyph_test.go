package quadratic

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"hd2font/native/internal/geometry"
)

type glyphOracleCase struct {
	Name      string        `json:"name"`
	Curve     [4][2]float64 `json:"curve"`
	Tolerance float64       `json:"tolerance"`
}

type glyphOracleRequest struct {
	glyphOracleCase
	Reverse  bool         `json:"reverse"`
	Data     string       `json:"data"`
	GoSpline [][2]float64 `json:"go_spline"`
}

type glyphOracleShape struct {
	Coordinates [][2]int `json:"coordinates"`
	Flags       []int    `json:"flags"`
	Ends        []int    `json:"ends"`
	Contours    int      `json:"contours"`
	Bounds      [4]int   `json:"bounds"`
}

// Both implementations are serialized to glyf and independently decoded by
// fontTools. Compression flag bits are representation details; the expanded
// glyph.flags (on/off-curve and semantic flags) are compared exactly.
const glyphCompiledOracle = `
import importlib.machinery, json, sys
import fontTools
import fontTools.cu2qu.cu2qu as compiled
from fontTools.pens.cu2quPen import Cu2QuPen
from fontTools.pens.ttGlyphPen import TTGlyphPen
from fontTools.ttLib.tables._g_l_y_f import Glyph, table__g_l_y_f
assert any(compiled.__file__.endswith(s) for s in importlib.machinery.EXTENSION_SUFFIXES), compiled.__file__
assert Cu2QuPen._convert_curve.__globals__['curve_to_quadratic'] is compiled.curve_to_quadratic
def decoded(raw):
    table = table__g_l_y_f()
    table.glyphOrder = ['test']
    table.glyphs = {'test': Glyph(raw)}
    glyph = table['test']
    coords, ends, flags = glyph.getCoordinates(table)
    return dict(coordinates=[list(p) for p in coords], flags=list(flags), ends=list(ends),
                contours=glyph.numberOfContours,
                bounds=[getattr(glyph,k,0) for k in ('xMin','yMin','xMax','yMax')])
rows=[]
for case in json.load(sys.stdin):
    curve=[tuple(p) for p in case['curve']]
    sink=TTGlyphPen(None)
    pen=Cu2QuPen(sink,max_err=case['tolerance'],reverse_direction=case['reverse'])
    pen.moveTo(curve[0]); pen.curveTo(*curve[1:]); pen.closePath()
    before_round=[list(p) for p in sink.points]
    glyph=sink.glyph()
    table=table__g_l_y_f(); table.glyphOrder=['test']; table.glyphs={'test':glyph}
    reference=glyph.compile(table)
    rows.append(dict(expected=decoded(reference), actual=decoded(bytes.fromhex(case['data'])),
                     spline=compiled.curve_to_quadratic(curve,case['tolerance']), before_round=before_round))
print(json.dumps(dict(module=compiled.__file__,version=fontTools.__version__,rows=rows)))
`

func checkGlyphCompiledOracle(t *testing.T, cases []glyphOracleCase) {
	t.Helper()
	if os.Getenv("HD2_FONTTOOLS_ORACLE") == "" {
		t.Skip("set HD2_FONTTOOLS_ORACLE=1 to run the compiled FontTools Cu2QuPen glyf oracle")
	}
	python := os.Getenv("HD2_FONTTOOLS_PYTHON")
	if python == "" {
		python = filepath.Join("..", "..", "..", ".venv", "Scripts", "python.exe")
	}
	// Once opted in, absent Python/compiled cu2qu is a failure, never a skip.
	var requests []glyphOracleRequest
	for _, test := range cases {
		for _, reverse := range []bool{false, true} {
			var path geometry.Path
			path.MoveTo(test.Curve[0][0], test.Curve[0][1])
			path.CubeTo(test.Curve[1][0], test.Curve[1][1], test.Curve[2][0], test.Curve[2][1], test.Curve[3][0], test.Curve[3][1])
			path.Close()
			// geometry.Path is float32. Give Python those EXACT widened inputs;
			// never compare a float32 input on one side to the original doubles.
			actualInput := test
			actualInput.Curve[0] = [2]float64{float64(path[0].Points[0]), float64(path[0].Points[1])}
			for j := 1; j < 4; j++ {
				actualInput.Curve[j] = [2]float64{float64(path[1].Points[2*(j-1)]), float64(path[1].Points[2*(j-1)+1])}
			}
			glyph, err := Glyph(path, test.Tolerance, reverse)
			if err != nil {
				t.Fatalf("%s reverse=%v: %v", test.Name, reverse, err)
			}
			spline, err := Curve(actualInput.Curve, test.Tolerance)
			if err != nil {
				t.Fatal(err)
			}
			requests = append(requests, glyphOracleRequest{actualInput, reverse, hex.EncodeToString(glyph.Data), spline})
		}
	}
	input, err := json.Marshal(requests)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-B", "-X", "utf8", "-c", glyphCompiledOracle)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("compiled FontTools oracle: %v\n%s", err, stderr.String())
	}
	var response struct {
		Module  string `json:"module"`
		Version string `json:"version"`
		Rows    []struct {
			Expected    glyphOracleShape `json:"expected"`
			Actual      glyphOracleShape `json:"actual"`
			Spline      [][2]float64     `json:"spline"`
			BeforeRound [][2]float64     `json:"before_round"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("oracle JSON: %v\n%s", err, stdout.String())
	}
	if len(response.Rows) != len(requests) {
		t.Fatalf("oracle rows=%d want=%d", len(response.Rows), len(requests))
	}
	t.Logf("compiled FontTools %s: %s; curves=%d, encoded glyphs=%d", response.Version, response.Module, len(cases), len(requests))
	for i, got := range response.Rows {
		request := requests[i]
		t.Run(fmt.Sprintf("%s/reverse=%t", request.Name, request.Reverse), func(t *testing.T) {
			if !reflect.DeepEqual(got.Actual, got.Expected) {
				t.Errorf("encoded glyf differs (NO float/tie tolerance)\ninput=%#v tolerance=%.17g reverse=%t\nexpected=%+v\nactual=%+v\ncompiled_spline=%#v\nGo_Curve_spline=%#v\nTTGlyphPen_before_round=%#v", request.Curve, request.Tolerance, request.Reverse, got.Expected, got.Actual, got.Spline, request.GoSpline, got.BeforeRound)
			}
		})
	}
}

func TestGlyphRealMacronCompiledOracle(t *testing.T) {
	checkGlyphCompiledOracle(t, []glyphOracleCase{
		{"O-macron-lower-half-tie", [4][2]float64{{55, 360}, {55, 121}, {199, -29}, {421, -29}}, 1},
		{"O-macron-upper-half-tie", [4][2]float64{{421, -29}, {646, -29}, {792, 123}, {792, 364}}, 1},
	})
}

func TestGlyphExistingCurvesCompiledOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []glyphOracleCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 24 {
		t.Fatalf("expected dozens of existing curves, got %d", len(cases))
	}
	for i := range cases {
		cases[i].Name = fmt.Sprintf("existing-%03d", i)
	}
	// Use every stored case, including zero curves and repeated endpoints. Do not
	// filter out numerical failures, ties, or a different segment split count.
	checkGlyphCompiledOracle(t, cases)
}

func TestGlyphHalfBoundariesCompiledOracle(t *testing.T) {
	var cases []glyphOracleCase
	// The 1,1 / 2,1 cubic is exactly quadratic with control (1.5,1.5).
	// Probe signed half-grid ties and the adjacent representable float32 input
	// on each side, while cu2qu controls remain float64 until glyf serialization.
	for _, sign := range []float64{-1, 1} {
		for _, shift := range []float64{-128, -.5, 0, .5, 128} {
			for _, side := range []int{-1, 0, 1} {
				curve := [4][2]float64{{shift, shift}, {shift + sign, shift + sign}, {shift + 2*sign, shift + sign}, {shift + 3*sign, shift}}
				if side != 0 {
					direction := float32(math.Inf(side))
					curve[1][1] = float64(math.Nextafter32(float32(curve[1][1]), direction))
					curve[2][1] = float64(math.Nextafter32(float32(curve[2][1]), direction))
				}
				cases = append(cases, glyphOracleCase{fmt.Sprintf("half-sign%g-shift%g-side%d", sign, shift, side), curve, .01})
			}
		}
	}
	// More than one quadratic segment, including the real controls on opposite
	// sides of a mathematical half. Translation exercises both axes/signs.
	for i, seed := range [][4][2]float64{
		{{55, 360}, {55, 121}, {199, -29}, {421, -29}},
		{{421, -29}, {646, -29}, {792, 123}, {792, 364}},
		{{0, 0}, {100, 400}, {200, -100}, {400, 0}},
	} {
		for _, shift := range [][2]float64{{0, .5}, {.5, 0}, {-.5, -.5}, {-1000.5, -500.5}} {
			curve := seed
			for j := range curve {
				curve[j][0] += shift[0]
				curve[j][1] += shift[1]
			}
			cases = append(cases, glyphOracleCase{fmt.Sprintf("segmented-%d-translate-%g-%g", i, shift[0], shift[1]), curve, 1})
		}
	}
	checkGlyphCompiledOracle(t, cases)
}
