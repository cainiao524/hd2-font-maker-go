package sfntio

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type glyphTestPoint struct {
	x, y int16
}

type testComponent struct {
	glyph uint16
	xy    bool
	words bool
	a, b  int32

	// transform is one, two or four raw F2Dot14 values. The order for a 2x2
	// transform is xx, xy, yx, yy, matching the component record and FontTools.
	transform []int16

	scaledOffset   bool
	unscaledOffset bool
}

func simpleTestGlyph(points []glyphTestPoint) []byte {
	if len(points) == 0 {
		return nil
	}
	data := make([]byte, 10)
	binary.BigEndian.PutUint16(data, 1)
	minX, maxX := points[0].x, points[0].x
	minY, maxY := points[0].y, points[0].y
	for _, point := range points[1:] {
		minX = min(minX, point.x)
		maxX = max(maxX, point.x)
		minY = min(minY, point.y)
		maxY = max(maxY, point.y)
	}
	for i, value := range []int16{minX, minY, maxX, maxY} {
		binary.BigEndian.PutUint16(data[2+2*i:], uint16(value))
	}
	data = binary.BigEndian.AppendUint16(data, uint16(len(points)-1))
	data = binary.BigEndian.AppendUint16(data, 0) // No instructions.
	flags := make([]byte, len(points))
	for i := range flags {
		flags[i] = 1 // On curve, full signed x/y deltas.
	}
	data = append(data, flags...)
	var previous int16
	for _, point := range points {
		delta := int32(point.x) - int32(previous)
		if delta < math.MinInt16 || delta > math.MaxInt16 {
			panic("test point x delta exceeds int16")
		}
		data = binary.BigEndian.AppendUint16(data, uint16(int16(delta)))
		previous = point.x
	}
	previous = 0
	for _, point := range points {
		delta := int32(point.y) - int32(previous)
		if delta < math.MinInt16 || delta > math.MaxInt16 {
			panic("test point y delta exceeds int16")
		}
		data = binary.BigEndian.AppendUint16(data, uint16(int16(delta)))
		previous = point.y
	}
	return data
}

func compositeTestGlyph(components []testComponent, bbox [4]int16, instructions []byte) []byte {
	data := make([]byte, 10)
	binary.BigEndian.PutUint16(data, 0xffff) // numberOfContours = -1
	for i, value := range bbox {
		binary.BigEndian.PutUint16(data[2+2*i:], uint16(value))
	}
	for i, component := range components {
		flags := uint16(0)
		if component.words {
			flags |= componentArgsAreWords
		}
		if component.xy {
			flags |= componentArgsAreXY
		}
		if i+1 < len(components) {
			flags |= componentMore
		}
		if i == len(components)-1 && instructions != nil {
			flags |= componentHaveInstr
		}
		switch len(component.transform) {
		case 0:
		case 1:
			flags |= componentHaveScale
		case 2:
			flags |= componentHaveXYScale
		case 4:
			flags |= componentHave2x2
		default:
			panic("test transform must contain 1, 2 or 4 values")
		}
		if component.scaledOffset {
			flags |= componentScaledOffset
		}
		if component.unscaledOffset {
			flags |= componentUnscaledOffset
		}
		data = binary.BigEndian.AppendUint16(data, flags)
		data = binary.BigEndian.AppendUint16(data, component.glyph)
		if component.words {
			if component.xy {
				data = binary.BigEndian.AppendUint16(data, uint16(int16(component.a)))
				data = binary.BigEndian.AppendUint16(data, uint16(int16(component.b)))
			} else {
				data = binary.BigEndian.AppendUint16(data, uint16(component.a))
				data = binary.BigEndian.AppendUint16(data, uint16(component.b))
			}
		} else if component.xy {
			data = append(data, byte(int8(component.a)), byte(int8(component.b)))
		} else {
			data = append(data, byte(component.a), byte(component.b))
		}
		for _, value := range component.transform {
			data = binary.BigEndian.AppendUint16(data, uint16(value))
		}
	}
	if instructions != nil {
		data = binary.BigEndian.AppendUint16(data, uint16(len(instructions)))
		data = append(data, instructions...)
	}
	return data
}

func buildGlyf(glyphs ...[]byte) ([]byte, []uint32) {
	var glyf []byte
	offsets := make([]uint32, 0, len(glyphs)+1)
	for _, glyph := range glyphs {
		offsets = append(offsets, uint32(len(glyf)))
		glyf = append(glyf, glyph...)
	}
	offsets = append(offsets, uint32(len(glyf)))
	return glyf, offsets
}

func glyphBox(glyf []byte, offsets []uint32, id int) [4]int16 {
	start := int(offsets[id])
	var box [4]int16
	for i := range box {
		box[i] = int16(binary.BigEndian.Uint16(glyf[start+2+2*i:]))
	}
	return box
}

func assertOnlyCompositeBBoxChanged(t *testing.T, before, after []byte, offsets []uint32) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("glyf length changed: %d -> %d", len(before), len(after))
	}
	composite := make([]bool, len(offsets)-1)
	for id := range composite {
		start, end := int(offsets[id]), int(offsets[id+1])
		if start == end {
			continue
		}
		composite[id] = int16(binary.BigEndian.Uint16(before[start:start+2])) < 0
		if !composite[id] {
			if !bytes.Equal(before[start:end], after[start:end]) {
				t.Errorf("simple glyph %d changed", id)
			}
			continue
		}
		if !bytes.Equal(before[start:start+2], after[start:start+2]) {
			t.Errorf("composite glyph %d contour count changed", id)
		}
		if !bytes.Equal(before[start+10:end], after[start+10:end]) {
			t.Errorf("composite glyph %d data after bbox changed", id)
		}
	}
	for position := range before {
		if before[position] == after[position] {
			continue
		}
		allowed := false
		for id := range composite {
			start := int(offsets[id])
			if composite[id] && position >= start+2 && position < start+10 {
				allowed = true
				break
			}
		}
		if !allowed {
			t.Errorf("byte %d changed outside a composite bbox", position)
		}
	}
}

func TestRecalculateCompositeBoundsRoundsOnlyAfterNestedTransforms(t *testing.T) {
	const half = int16(0x2000) // 0.5 in F2Dot14.
	simple := simpleTestGlyph([]glyphTestPoint{{0, 0}, {101, 0}, {0, 101}})
	inner := compositeTestGlyph([]testComponent{{
		glyph:     0,
		xy:        true,
		transform: []int16{half},
	}}, [4]int16{0, 0, 51, 51}, nil)
	// inner's exact maximum is 50.5, while its header bbox is otRound(50.5)=51.
	// Scaling the header independently gives otRound(51*0.5)=26. Scaling the
	// nested coordinates gives otRound(50.5*0.5)=25, the one-unit difference
	// this helper must remove.
	outer := compositeTestGlyph([]testComponent{{
		glyph:     1,
		xy:        true,
		transform: []int16{half},
	}}, [4]int16{0, 0, 26, 26}, nil)
	glyf, offsets := buildGlyf(simple, inner, outer)
	before := append([]byte(nil), glyf...)

	if err := RecalculateCompositeBounds(glyf, offsets); err != nil {
		t.Fatal(err)
	}
	if got, want := glyphBox(glyf, offsets, 1), [4]int16{0, 0, 51, 51}; got != want {
		t.Fatalf("inner bbox = %v, want %v", got, want)
	}
	if got, want := glyphBox(glyf, offsets, 2), [4]int16{0, 0, 25, 25}; got != want {
		t.Fatalf("outer nested bbox = %v, want %v", got, want)
	}
	assertOnlyCompositeBBoxChanged(t, before, glyf, offsets)
}

func TestRecalculateCompositeBoundsPointAttachmentUsesUnsignedPointIndices(t *testing.T) {
	prior := make([]glyphTestPoint, 260)
	for i := range prior {
		prior[i] = glyphTestPoint{0, 0}
	}
	prior[200] = glyphTestPoint{2, 3}
	source0 := simpleTestGlyph(prior)
	source1 := simpleTestGlyph([]glyphTestPoint{{0, 0}, {20, 0}, {0, 20}})
	attached := compositeTestGlyph([]testComponent{
		{glyph: 0, xy: true},
		// 200 is a byte argument. Reading these as signed bytes would turn it
		// into -56 and corrupt the anchor.
		{glyph: 1, a: 200, b: 0},
	}, [4]int16{9, 9, 9, 9}, nil)
	glyf, offsets := buildGlyf(source0, source1, attached)
	before := append([]byte(nil), glyf...)

	if err := RecalculateCompositeBounds(glyf, offsets); err != nil {
		t.Fatal(err)
	}
	if got, want := glyphBox(glyf, offsets, 2), [4]int16{0, 0, 22, 23}; got != want {
		t.Fatalf("attached bbox = %v, want %v", got, want)
	}
	assertOnlyCompositeBBoxChanged(t, before, glyf, offsets)
}

func TestRecalculateCompositeBoundsPointAttachmentWordsAndTransform(t *testing.T) {
	prior := make([]glyphTestPoint, 310)
	for i := range prior {
		prior[i] = glyphTestPoint{0, 0}
	}
	prior[300] = glyphTestPoint{2, 3}
	source0 := simpleTestGlyph(prior)
	source1 := simpleTestGlyph([]glyphTestPoint{{0, 0}, {20, 0}, {0, 20}})
	attached := compositeTestGlyph([]testComponent{
		{glyph: 0, xy: true},
		{
			glyph:     1,
			words:     true,
			a:         300,
			b:         1,
			transform: []int16{int16(0x2000)},
		},
	}, [4]int16{0, 0, 0, 0}, nil)
	glyf, offsets := buildGlyf(source0, source1, attached)
	before := append([]byte(nil), glyf...)

	if err := RecalculateCompositeBounds(glyf, offsets); err != nil {
		t.Fatal(err)
	}
	if got, want := glyphBox(glyf, offsets, 2), [4]int16{-8, 0, 2, 13}; got != want {
		t.Fatalf("word attachment bbox = %v, want %v", got, want)
	}
	assertOnlyCompositeBBoxChanged(t, before, glyf, offsets)
}

func TestRecalculateCompositeBoundsTransformsAndOffsetFlags(t *testing.T) {
	const (
		half    = int16(0x2000)
		quarter = int16(0x1000)
	)
	simple := simpleTestGlyph([]glyphTestPoint{{0, 0}, {10, 0}, {0, 10}})
	tests := []struct {
		name      string
		component testComponent
		want      [4]int16
	}{
		{
			name:      "default offset is unscaled",
			component: testComponent{glyph: 0, xy: true, a: 100, transform: []int16{half}},
			want:      [4]int16{100, 0, 105, 5},
		},
		{
			name:      "scaled component offset",
			component: testComponent{glyph: 0, xy: true, a: 100, transform: []int16{half}, scaledOffset: true},
			want:      [4]int16{50, 0, 55, 5},
		},
		{
			name:      "explicit unscaled component offset",
			component: testComponent{glyph: 0, xy: true, a: 100, transform: []int16{half}, unscaledOffset: true},
			want:      [4]int16{100, 0, 105, 5},
		},
		{
			name:      "x and y scale",
			component: testComponent{glyph: 0, xy: true, transform: []int16{half, quarter}},
			want:      [4]int16{0, 0, 5, 3},
		},
		{
			name:      "two by two matrix",
			component: testComponent{glyph: 0, xy: true, transform: []int16{half, quarter, -quarter, half}},
			want:      [4]int16{-2, 0, 5, 5},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instructions := []byte{0xb0, 0x00, 0x2e}
			composite := compositeTestGlyph([]testComponent{test.component}, [4]int16{1, 1, 1, 1}, instructions)
			glyf, offsets := buildGlyf(simple, composite)
			before := append([]byte(nil), glyf...)
			if err := RecalculateCompositeBounds(glyf, offsets); err != nil {
				t.Fatal(err)
			}
			if got := glyphBox(glyf, offsets, 1); got != test.want {
				t.Fatalf("bbox = %v, want %v", got, test.want)
			}
			assertOnlyCompositeBBoxChanged(t, before, glyf, offsets)
		})
	}
}

func TestRecalculateCompositeBoundsEmptyChildrenProduceZeroBox(t *testing.T) {
	emptyHeader := make([]byte, 10) // numberOfContours = 0
	composite := compositeTestGlyph([]testComponent{
		{glyph: 0, xy: true, a: 10, b: 20},
		{glyph: 1, xy: true, a: 30, b: 40},
	}, [4]int16{7, 8, 9, 10}, nil)
	glyf, offsets := buildGlyf(emptyHeader, nil, composite)
	before := append([]byte(nil), glyf...)

	if err := RecalculateCompositeBounds(glyf, offsets); err != nil {
		t.Fatal(err)
	}
	if got := glyphBox(glyf, offsets, 2); got != [4]int16{} {
		t.Fatalf("empty composite bbox = %v, want zero", got)
	}
	assertOnlyCompositeBBoxChanged(t, before, glyf, offsets)
}

func TestRecalculateCompositeBoundsRejectsMalformedInput(t *testing.T) {
	const half = int16(0x2000)
	simple := simpleTestGlyph([]glyphTestPoint{{0, 0}, {10, 0}, {0, 10}})

	cycleGlyf, cycleOffsets := buildGlyf(
		compositeTestGlyph([]testComponent{{glyph: 1, xy: true}}, [4]int16{}, nil),
		compositeTestGlyph([]testComponent{{glyph: 0, xy: true}}, [4]int16{}, nil),
	)
	outOfRangeComposite := compositeTestGlyph([]testComponent{{glyph: 1, xy: true}}, [4]int16{}, nil)
	pointAttachComposite := compositeTestGlyph([]testComponent{{glyph: 0, a: 0, b: 0}}, [4]int16{}, nil)
	pointAttachGlyf, pointAttachOffsets := buildGlyf(simple, pointAttachComposite)
	bothFlagsComposite := compositeTestGlyph([]testComponent{{
		glyph:          0,
		xy:             true,
		transform:      []int16{half},
		scaledOffset:   true,
		unscaledOffset: true,
	}}, [4]int16{}, nil)
	bothFlagsGlyf, bothFlagsOffsets := buildGlyf(simple, bothFlagsComposite)
	overflowSource := simpleTestGlyph([]glyphTestPoint{{30000, 0}, {0, 0}, {0, 1}})
	overflowInner := compositeTestGlyph([]testComponent{{
		glyph:     0,
		xy:        true,
		transform: []int16{32767},
	}}, [4]int16{}, nil)
	overflowOuter := compositeTestGlyph([]testComponent{{
		glyph:     1,
		xy:        true,
		transform: []int16{32767},
	}}, [4]int16{}, nil)
	overflowGlyf, overflowOffsets := buildGlyf(overflowSource, overflowInner, overflowOuter)

	tests := []struct {
		name    string
		glyf    []byte
		offsets []uint32
		want    string
	}{
		{
			name:    "empty offsets",
			glyf:    nil,
			offsets: nil,
			want:    "offsets are empty",
		},
		{
			name:    "offset beyond glyf",
			glyf:    []byte{0, 0, 0},
			offsets: []uint32{0, 4},
			want:    "exceeds glyf size",
		},
		{
			name:    "decreasing offsets",
			glyf:    make([]byte, 20),
			offsets: []uint32{0, 10, 5},
			want:    "decrease",
		},
		{
			name:    "truncated header",
			glyf:    []byte{0, 0, 0, 0, 0},
			offsets: []uint32{0, 5},
			want:    "truncated glyph header",
		},
		{
			name: "invalid negative contour count",
			glyf: func() []byte {
				data := make([]byte, 10)
				binary.BigEndian.PutUint16(data, 0xfffe)
				return data
			}(),
			offsets: []uint32{0, 10},
			want:    "invalid negative contour count",
		},
		{
			name:    "cycle",
			glyf:    cycleGlyf,
			offsets: cycleOffsets,
			want:    "cyclic composite reference",
		},
		{
			name:    "component out of range",
			glyf:    outOfRangeComposite,
			offsets: []uint32{0, uint32(len(outOfRangeComposite))},
			want:    "out of range",
		},
		{
			name:    "point attachment out of range",
			glyf:    pointAttachGlyf,
			offsets: pointAttachOffsets,
			want:    "point attachment",
		},
		{
			name:    "both offset flags",
			glyf:    bothFlagsGlyf,
			offsets: bothFlagsOffsets,
			want:    "both scaled and unscaled",
		},
		{
			name:    "bbox exceeds int16",
			glyf:    overflowGlyf,
			offsets: overflowOffsets,
			want:    "exceeds int16",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := RecalculateCompositeBounds(test.glyf, test.offsets)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestRecalculateCompositeBoundsFontToolsOracle(t *testing.T) {
	if os.Getenv("HD2_FONTTOOLS_ORACLE") == "" {
		t.Skip("set HD2_FONTTOOLS_ORACLE=1 to compare against FontTools")
	}
	python := os.Getenv("HD2_FONTTOOLS_PYTHON")
	if python == "" {
		python = filepath.Join("..", "..", "..", ".venv", "Scripts", "python.exe")
	}
	if _, err := os.Stat(python); err != nil {
		t.Skipf("FontTools Python not available: %v", err)
	}

	glyf, offsets := fontToolsOracleFixture()
	if err := RecalculateCompositeBounds(glyf, offsets); err != nil {
		t.Fatal(err)
	}
	glyphs := make([]string, len(offsets)-1)
	for id := range glyphs {
		glyphs[id] = hexEncode(glyf[offsets[id]:offsets[id+1]])
	}
	payload, err := json.Marshal(map[string]any{"glyphs": glyphs})
	if err != nil {
		t.Fatal(err)
	}
	const script = `
import json, sys
from fontTools.misc.roundTools import otRound
from fontTools.ttLib.tables._g_l_y_f import Glyph, table__g_l_y_f

payload = json.load(sys.stdin)
glyf = table__g_l_y_f()
glyf.glyphOrder = ["g%d" % i for i in range(len(payload["glyphs"]))]
glyf.glyphs = {
    "g%d" % i: Glyph(data=bytes.fromhex(data))
    for i, data in enumerate(payload["glyphs"])
}
result = []
for i in range(len(glyf.glyphOrder)):
    glyph = glyf["g%d" % i]
    if not glyph.isComposite():
        result.append([0, 0, 0, 0])
        continue
    coords, _, _ = glyph.getCoordinates(glyf, round=otRound)
    result.append(list(coords.calcIntBounds()))
print(json.dumps(result))
`
	cmd := exec.Command(python, "-c", script)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "ModuleNotFoundError") {
			t.Skipf("fontTools is not installed for %s", python)
		}
		t.Fatalf("FontTools oracle failed: %v\n%s", err, stderr.String())
	}
	var want [][]int
	if err := json.Unmarshal(stdout.Bytes(), &want); err != nil {
		t.Fatalf("parse FontTools oracle output: %v\n%s", err, stdout.String())
	}
	if len(want) != len(glyphs) {
		t.Fatalf("FontTools returned %d glyphs, want %d", len(want), len(glyphs))
	}
	for id := range glyphs {
		start, end := int(offsets[id]), int(offsets[id+1])
		if start == end {
			continue
		}
		if int16(binary.BigEndian.Uint16(glyf[start:start+2])) >= 0 {
			continue
		}
		got := glyphBox(glyf, offsets, id)
		for i := range got {
			if int(got[i]) != want[id][i] {
				t.Fatalf("glyph %d bbox[%d] = %d, FontTools %d", id, i, got[i], want[id][i])
			}
		}
	}
}

func hexEncode(data []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(data))
	for _, value := range data {
		out = append(out, digits[value>>4], digits[value&0x0f])
	}
	return string(out)
}

func fontToolsOracleFixture() ([]byte, []uint32) {
	const (
		half    = int16(0x2000)
		quarter = int16(0x1000)
	)
	nestedSource := simpleTestGlyph([]glyphTestPoint{{0, 0}, {101, 0}, {0, 101}})
	nestedInner := compositeTestGlyph([]testComponent{{
		glyph:     0,
		xy:        true,
		transform: []int16{half},
	}}, [4]int16{0, 0, 51, 51}, nil)
	nestedOuter := compositeTestGlyph([]testComponent{{
		glyph:     1,
		xy:        true,
		transform: []int16{half},
	}}, [4]int16{0, 0, 26, 26}, nil)

	prior := make([]glyphTestPoint, 260)
	for i := range prior {
		prior[i] = glyphTestPoint{0, 0}
	}
	prior[200] = glyphTestPoint{2, 3}
	anchorSource := simpleTestGlyph(prior)
	anchorChild := simpleTestGlyph([]glyphTestPoint{{0, 0}, {20, 0}, {0, 20}})
	anchor := compositeTestGlyph([]testComponent{
		{glyph: 3, xy: true},
		{glyph: 4, a: 200, b: 0},
	}, [4]int16{9, 9, 9, 9}, nil)

	matrixSource := simpleTestGlyph([]glyphTestPoint{{0, 0}, {10, 0}, {0, 10}})
	defaultOffset := compositeTestGlyph([]testComponent{{
		glyph:     6,
		xy:        true,
		a:         100,
		transform: []int16{half},
	}}, [4]int16{1, 1, 1, 1}, nil)
	scaledOffset := compositeTestGlyph([]testComponent{{
		glyph:        6,
		xy:           true,
		a:            100,
		transform:    []int16{half},
		scaledOffset: true,
	}}, [4]int16{1, 1, 1, 1}, nil)
	twoByTwo := compositeTestGlyph([]testComponent{{
		glyph:     6,
		xy:        true,
		transform: []int16{half, quarter, -quarter, half},
	}}, [4]int16{1, 1, 1, 1}, nil)
	emptyComposite := compositeTestGlyph([]testComponent{{
		glyph: 10,
		xy:    true,
		a:     10,
		b:     20,
	}}, [4]int16{7, 8, 9, 10}, nil)

	return buildGlyf(
		nestedSource,   // 0
		nestedInner,    // 1
		nestedOuter,    // 2
		anchorSource,   // 3
		anchorChild,    // 4
		anchor,         // 5
		matrixSource,   // 6
		defaultOffset,  // 7
		scaledOffset,   // 8
		twoByTwo,       // 9
		nil,            // 10, empty
		emptyComposite, // 11
	)
}
