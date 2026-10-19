package geometry

import (
	"math"
	"slices"
	"testing"
)

func TestSplitTouchingContoursSimpleExplicitShapeUnchanged(t *testing.T) {
	var path Path
	path.MoveTo(0, 0)
	path.LineTo(8, 0)
	path.QuadTo(10, 2, 10, 5)
	path.CubeTo(10, 8, 8, 10, 5, 10)
	path.LineTo(0, 10)
	path.LineTo(0, 0)
	path.Close()
	want := slices.Clone(path)

	got, err := SplitTouchingContours(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("simple shape changed\ngot  %v\nwant %v", got, want)
	}
	if !slices.Equal(path, want) {
		t.Fatal("input was mutated")
	}
}

func TestSplitTouchingContoursImplicitCloseAndZeroLengthLine(t *testing.T) {
	var path Path
	path.MoveTo(0, 0)
	path.LineTo(2, 2)
	path.LineTo(2, 2)
	path.LineTo(0, 4)
	path.Close()

	got, err := SplitTouchingContours(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Path{
		{Verb: Move, Points: [6]float32{0, 0}},
		{Verb: Line, Points: [6]float32{2, 2}},
		{Verb: Line, Points: [6]float32{0, 4}},
		{Verb: Line, Points: [6]float32{0, 0}},
		{Verb: Close},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("implicit close mismatch\ngot  %v\nwant %v", got, want)
	}
}

func TestSplitTouchingContoursKeepsCurveWithIdenticalEndpoints(t *testing.T) {
	var path Path
	path.MoveTo(4, 4)
	path.QuadTo(5, 7, 4, 4)
	path.Close()

	got, err := SplitTouchingContours(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1] != path[1] {
		t.Fatalf("nontrivial zero-endpoint curve was changed or dropped: %v", got)
	}
}

func TestSplitTouchingContoursFigureEight(t *testing.T) {
	var path Path
	path.MoveTo(0, 0)
	path.LineTo(2, 2)
	path.LineTo(0, 4)
	path.LineTo(-2, 2)
	path.LineTo(0, 0)
	path.LineTo(2, -2)
	path.LineTo(0, -4)
	path.LineTo(-2, -2)
	path.LineTo(0, 0)
	path.Close()

	got, err := SplitTouchingContours(path)
	if err != nil {
		t.Fatal(err)
	}
	if countSplitContours(got) != 2 {
		t.Fatalf("got %d contours, want 2: %v", countSplitContours(got), got)
	}
	want := Path{
		{Verb: Move, Points: [6]float32{0, 0}},
		{Verb: Line, Points: [6]float32{2, 2}},
		{Verb: Line, Points: [6]float32{0, 4}},
		{Verb: Line, Points: [6]float32{-2, 2}},
		{Verb: Line, Points: [6]float32{0, 0}},
		{Verb: Close},
		{Verb: Move, Points: [6]float32{0, 0}},
		{Verb: Line, Points: [6]float32{2, -2}},
		{Verb: Line, Points: [6]float32{0, -4}},
		{Verb: Line, Points: [6]float32{-2, -2}},
		{Verb: Line, Points: [6]float32{0, 0}},
		{Verb: Close},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("figure-eight direction/order changed\ngot  %v\nwant %v", got, want)
	}
	assertDirectedEdgesEqual(t, path, got)
	assertWindingEqual(t, path, got)
}

func TestSplitTouchingContoursSelfTouchingHole(t *testing.T) {
	var path Path
	// Counter-clockwise outer boundary.
	path.MoveTo(0, 0)
	path.LineTo(10, 0)
	path.LineTo(10, 10)
	path.LineTo(0, 10)
	path.Close()
	// Two clockwise holes meeting at (4,4) inside one input contour.
	path.MoveTo(4, 4)
	path.LineTo(3, 6)
	path.LineTo(5, 6)
	path.LineTo(4, 4)
	path.LineTo(5, 2)
	path.LineTo(3, 2)
	path.LineTo(4, 4)
	path.Close()

	got, err := SplitTouchingContours(path)
	if err != nil {
		t.Fatal(err)
	}
	if countSplitContours(got) != 3 {
		t.Fatalf("got %d contours, want outer plus two touching holes: %v", countSplitContours(got), got)
	}
	assertDirectedEdgesEqual(t, path, got)
	assertWindingEqual(t, path, got)
	if windingAt(testFlatten(path), 4, 7) != 1 {
		t.Fatal("test fixture should fill the outer band")
	}
	if windingAt(testFlatten(path), 4, 3) != 0 {
		t.Fatal("test fixture should make the clockwise touching hole empty")
	}
}

func TestSplitTouchingContoursPreservesCurvesAndDirection(t *testing.T) {
	var path Path
	path.MoveTo(0, 0)
	path.QuadTo(1, 2, 2, 0)
	path.CubeTo(3, -2, 1, -2, 0, 0)
	path.QuadTo(1, 1, 2, 1)
	path.CubeTo(3, 1, 3, -1, 2, -1)
	path.QuadTo(1, -1, 0, 0)
	path.Close()

	got, err := SplitTouchingContours(path)
	if err != nil {
		t.Fatal(err)
	}
	if countSplitContours(got) != 2 {
		t.Fatalf("got %d contours, want 2: %v", countSplitContours(got), got)
	}
	assertDirectedEdgesEqual(t, path, got)
	assertWindingEqual(t, path, got)
	if countVerb(path, Quad) != countVerb(got, Quad) || countVerb(path, Cubic) != countVerb(got, Cubic) {
		t.Fatal("curve command count changed")
	}
}

func TestSplitTouchingContoursRejectsMalformedPaths(t *testing.T) {
	move := Command{Verb: Move}
	line := Command{Verb: Line, Points: [6]float32{1, 1}}
	close := Command{Verb: Close}
	for name, path := range map[string]Path{
		"line without move":  {line},
		"nested move":        {move, line, move, close},
		"unclosed contour":   {move, line},
		"unknown verb":       {move, {Verb: 99}},
		"non-finite command": {move, {Verb: Line, Points: [6]float32{float32(math.Inf(1)), 0}}, close},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := SplitTouchingContours(path)
			if err == nil || got != nil {
				t.Fatalf("expected error with no partial path, got %v, %v", got, err)
			}
		})
	}
}

func countSplitContours(path Path) int {
	count := 0
	for _, command := range path {
		if command.Verb == Close {
			count++
		}
	}
	return count
}

func countVerb(path Path, verb uint32) int {
	count := 0
	for _, command := range path {
		if command.Verb == verb {
			count++
		}
	}
	return count
}

type splitEdgeKey struct {
	verb   uint32
	from   [2]float32
	points [6]float32
}

func splitDirectedEdges(path Path) []splitEdgeKey {
	var edges []splitEdgeKey
	var start, current [2]float32
	active := false
	for _, command := range path {
		switch command.Verb {
		case Move:
			start = [2]float32{command.Points[0], command.Points[1]}
			current, active = start, true
		case Line, Quad, Cubic:
			next := [2]float32{command.Points[0], command.Points[1]}
			if command.Verb == Quad {
				next = [2]float32{command.Points[2], command.Points[3]}
			} else if command.Verb == Cubic {
				next = [2]float32{command.Points[4], command.Points[5]}
			}
			if command.Verb != Line || current != next {
				edges = append(edges, splitEdgeKey{verb: command.Verb, from: current, points: command.Points})
			}
			current = next
		case Close:
			if current != start {
				edges = append(edges, splitEdgeKey{verb: Line, from: current, points: [6]float32{start[0], start[1]}})
			}
			active = false
		}
	}
	if active {
		panic("splitDirectedEdges requires closed contours")
	}
	return edges
}

func assertDirectedEdgesEqual(t *testing.T, before, after Path) {
	t.Helper()
	left, right := splitDirectedEdges(before), splitDirectedEdges(after)
	leftCounts := map[splitEdgeKey]int{}
	rightCounts := map[splitEdgeKey]int{}
	for _, edge := range left {
		leftCounts[edge]++
	}
	for _, edge := range right {
		rightCounts[edge]++
	}
	if len(leftCounts) != len(rightCounts) {
		t.Fatalf("directed edge multiset changed\nbefore %v\nafter  %v", leftCounts, rightCounts)
	}
	for edge, count := range leftCounts {
		if rightCounts[edge] != count {
			t.Fatalf("directed edge multiset changed\nbefore %v\nafter  %v", leftCounts, rightCounts)
		}
	}
	if len(left) != len(right) {
		t.Fatalf("directed edge multiset changed\nbefore %v\nafter  %v", left, right)
	}
}

type splitTestPoint struct{ x, y float64 }

func testFlatten(path Path) [][]splitTestPoint {
	const steps = 16
	var contours [][]splitTestPoint
	var points []splitTestPoint
	var start, current splitTestPoint
	active := false
	for _, command := range path {
		switch command.Verb {
		case Move:
			start = splitTestPoint{float64(command.Points[0]), float64(command.Points[1])}
			current, active = start, true
			points = []splitTestPoint{start}
		case Line:
			current = splitTestPoint{float64(command.Points[0]), float64(command.Points[1])}
			points = append(points, current)
		case Quad:
			p0 := current
			p1 := splitTestPoint{float64(command.Points[0]), float64(command.Points[1])}
			p2 := splitTestPoint{float64(command.Points[2]), float64(command.Points[3])}
			for i := 1; i <= steps; i++ {
				t := float64(i) / steps
				u := 1 - t
				points = append(points, splitTestPoint{
					u*u*p0.x + 2*u*t*p1.x + t*t*p2.x,
					u*u*p0.y + 2*u*t*p1.y + t*t*p2.y,
				})
			}
			current = p2
		case Cubic:
			p0 := current
			p1 := splitTestPoint{float64(command.Points[0]), float64(command.Points[1])}
			p2 := splitTestPoint{float64(command.Points[2]), float64(command.Points[3])}
			p3 := splitTestPoint{float64(command.Points[4]), float64(command.Points[5])}
			for i := 1; i <= steps; i++ {
				t := float64(i) / steps
				u := 1 - t
				points = append(points, splitTestPoint{
					u*u*u*p0.x + 3*u*u*t*p1.x + 3*u*t*t*p2.x + t*t*t*p3.x,
					u*u*u*p0.y + 3*u*u*t*p1.y + 3*u*t*t*p2.y + t*t*t*p3.y,
				})
			}
			current = p3
		case Close:
			if current != start {
				points = append(points, start)
			}
			contours = append(contours, points)
			active = false
		}
	}
	if active {
		panic("testFlatten requires closed contours")
	}
	return contours
}

func windingAt(contours [][]splitTestPoint, x, y float64) int {
	winding := 0
	for _, points := range contours {
		for i := 0; i+1 < len(points); i++ {
			a, b := points[i], points[i+1]
			cross := (b.x-a.x)*(y-a.y) - (b.y-a.y)*(x-a.x)
			if a.y <= y && b.y > y && cross > 0 {
				winding++
			} else if a.y > y && b.y <= y && cross < 0 {
				winding--
			}
		}
	}
	return winding
}

func assertWindingEqual(t *testing.T, before, after Path) {
	t.Helper()
	left, right := testFlatten(before), testFlatten(after)
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, command := range append(slices.Clone(before), after...) {
		n := 0
		switch command.Verb {
		case Move, Line:
			n = 2
		case Quad:
			n = 4
		case Cubic:
			n = 6
		}
		for i := 0; i < n; i += 2 {
			x, y := float64(command.Points[i]), float64(command.Points[i+1])
			minX, minY = math.Min(minX, x), math.Min(minY, y)
			maxX, maxY = math.Max(maxX, x), math.Max(maxY, y)
		}
	}
	const columns, rows = 43, 37
	for row := 0; row < rows; row++ {
		y := minY - 1 + (float64(row)+0.5)*(maxY-minY+2)/rows
		for column := 0; column < columns; column++ {
			x := minX - 1 + (float64(column)+0.5)*(maxX-minX+2)/columns
			want, got := windingAt(left, x, y), windingAt(right, x, y)
			if got != want {
				t.Fatalf("winding changed at (%.6g, %.6g): got %d, want %d", x, y, got, want)
			}
		}
	}
}
