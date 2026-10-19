// Package outlinecleanup removes degenerate edges on the encoded font grid.
package outlinecleanup

import (
	"fmt"
	"math"
	"slices"

	"hd2font/native/internal/geometry"
)

const (
	flattenTolerance   = 0.2
	maxFlattenDepth    = 64
	maxFlattenSegments = 65536
)

type point struct{ x, y float64 }
type segment struct {
	p [4]point
	n int
}

func line(a, b point) segment     { return segment{p: [4]point{a, b}, n: 2} }
func (s segment) end() point      { return s.p[s.n-1] }
func sub(a, b point) point        { return point{a.x - b.x, a.y - b.y} }
func dot(a, b point) float64      { return a.x*b.x + a.y*b.y }
func distance(a, b point) float64 { return math.Hypot(a.x-b.x, a.y-b.y) }

func distanceToLine(p, a, b point) float64 {
	v := sub(b, a)
	length := math.Hypot(v.x, v.y)
	if length == 0 {
		return distance(p, a)
	}
	u := sub(p, a)
	return math.Abs(u.x*v.y-u.y*v.x) / length
}

func straight(s segment) bool {
	if s.n == 2 {
		return true
	}
	a := s.p[0]
	var v point
	for _, p := range s.p[1:s.n] {
		if p != a {
			v = sub(p, a)
			break
		}
	}
	for _, p := range s.p[1:s.n] {
		if !(math.Abs(v.x*(p.y-a.y)-v.y*(p.x-a.x)) < 1e-10) {
			return false
		}
	}
	return true
}

func direction(s segment, end bool) point {
	if s.n == 2 {
		return sub(s.p[1], s.p[0])
	}
	if end {
		a := s.end()
		for i := s.n - 2; i >= 0; i-- {
			if s.p[i] != a {
				return sub(a, s.p[i])
			}
		}
	} else {
		for _, p := range s.p[1:s.n] {
			if p != s.p[0] {
				return sub(p, s.p[0])
			}
		}
	}
	return point{}
}

func convergent(a, b segment) bool {
	x, y := direction(a, true), direction(b, false)
	norm := math.Hypot(x.x, x.y) * math.Hypot(y.x, y.y)
	return norm != 0 && dot(x, y)/norm < -0.9999
}

func split(s segment) (segment, segment) {
	left, right := segment{n: s.n}, segment{n: s.n}
	row := s.p
	for size := s.n; size > 0; size-- {
		left.p[s.n-size] = row[0]
		right.p[size-1] = row[size-1]
		for i := 0; i < size-1; i++ {
			row[i] = point{(row[i].x + row[i+1].x) / 2, (row[i].y + row[i+1].y) / 2}
		}
	}
	return left, right
}

func flatEnough(s segment) bool {
	a, b := s.p[0], s.end()
	v := sub(b, a)
	norm := dot(v, v)
	length := math.Sqrt(norm)
	chordOK, small := norm != 0, true
	for _, p := range s.p[:s.n] {
		if distanceToLine(p, a, b) > flattenTolerance {
			chordOK = false
		}
		if norm != 0 {
			projection := dot(sub(p, a), v) / length
			if projection < -flattenTolerance || length+flattenTolerance < projection {
				chordOK = false
			}
		}
		if distance(a, p) > flattenTolerance {
			small = false
		}
	}
	return chordOK || small
}

func flatten(s segment) ([]point, error) {
	type item struct {
		s     segment
		depth int
	}
	stack := []item{{s, 0}}
	points := []point{s.p[0]}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if flatEnough(i.s) {
			if len(points) > maxFlattenSegments {
				return nil, fmt.Errorf("curve exceeds %d flattened segments", maxFlattenSegments)
			}
			points = append(points, i.s.end())
			continue
		}
		if i.depth >= maxFlattenDepth {
			return nil, fmt.Errorf("curve cannot reach 0.2-unit tolerance within %d subdivisions", maxFlattenDepth)
		}
		left, right := split(i.s)
		// LIFO order matches recursive left-then-right subdivision without recursion.
		stack = append(stack, item{right, i.depth + 1}, item{left, i.depth + 1})
	}
	return points, nil
}

func cleanOnce(contour []segment) ([]segment, error) {
	clean := make([]segment, 0, len(contour))
	for _, s := range contour {
		if straight(s) {
			if s.p[0] != s.end() {
				clean = append(clean, line(s.p[0], s.end()))
			}
		} else {
			clean = append(clean, s)
		}
	}
	if len(clean) == 0 {
		return clean, nil
	}
	affected := make([]bool, len(clean))
	for i, s := range clean {
		if s.n > 2 && (s.p[0] == s.p[1] || s.end() == s.p[s.n-2]) {
			affected[i] = true
		}
		j := (i + 1) % len(clean)
		if convergent(s, clean[j]) {
			affected[i], affected[j] = true, true
		}
	}
	expanded := make([]segment, 0, len(clean))
	for i, s := range clean {
		if !affected[i] || s.n == 2 {
			expanded = append(expanded, s)
			continue
		}
		points, err := flatten(s)
		if err != nil {
			return nil, err
		}
		for j, p := range points {
			points[j] = point{math.Floor(p.x + 0.5), math.Floor(p.y + 0.5)}
		}
		// Only new interior vertices are rounded; implied TTF endpoints stay exact.
		points[0], points[len(points)-1] = s.p[0], s.end()
		for j := 1; j < len(points); j++ {
			if points[j-1] != points[j] {
				expanded = append(expanded, line(points[j-1], points[j]))
			}
		}
	}
	for len(expanded) > 1 {
		changed := false
		for i, a := range expanded {
			j := (i + 1) % len(expanded)
			b := expanded[j]
			if a.n != 2 || b.n != 2 {
				continue
			}
			collinear := straight(segment{p: [4]point{a.p[0], a.end(), b.end()}, n: 3})
			if dot(sub(a.end(), a.p[0]), sub(b.end(), b.p[0])) >= 0 ||
				!(collinear || (convergent(a, b) && math.Min(distanceToLine(b.end(), a.p[0], a.end()), distanceToLine(a.p[0], b.p[0], b.end())) <= 1)) {
				continue
			}
			var replacement []segment
			if a.p[0] != b.end() {
				replacement = []segment{line(a.p[0], b.end())}
			}
			if j == 0 {
				expanded = append(replacement, expanded[1:i]...)
			} else {
				expanded = slices.Replace(expanded, i, j+1, replacement...)
			}
			changed = true
			break
		}
		if !changed {
			break
		}
	}
	return expanded, nil
}

func cleanContour(contour []segment) ([]segment, error) {
	for {
		cleaned, err := cleanOnce(contour)
		if err != nil {
			return nil, err
		}
		if slices.Equal(cleaned, contour) {
			return cleaned, nil
		}
		// Every changing pass reduces curves, or reduces edges without adding curves.
		contour = cleaned
	}
}

// Clean reaches the contour-cleanup fixed point without changing its input.
// Paths must contain finite coordinates and explicitly closed contours.
// Font encoding, rereading, and the caller's eight-pass grid loop are separate.
func Clean(path geometry.Path) (geometry.Path, error) {
	var output geometry.Path
	var contour []segment
	var start, current point
	active := false
	for i, cmd := range path {
		count := 0
		switch cmd.Verb {
		case geometry.Move, geometry.Line:
			count = 2
		case geometry.Quad:
			count = 4
		case geometry.Cubic:
			count = 6
		case geometry.Close:
		default:
			return nil, fmt.Errorf("command %d: unknown verb %d", i, cmd.Verb)
		}
		for _, v := range cmd.Points[:count] {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return nil, fmt.Errorf("command %d: non-finite coordinate", i)
			}
		}
		if cmd.Verb == geometry.Move {
			if active {
				return nil, fmt.Errorf("command %d: move before closing contour", i)
			}
			start = point{float64(cmd.Points[0]), float64(cmd.Points[1])}
			current, active = start, true
			contour = contour[:0]
			continue
		}
		if !active {
			return nil, fmt.Errorf("command %d: verb %d without an open contour", i, cmd.Verb)
		}
		if cmd.Verb != geometry.Close {
			s := segment{n: count/2 + 1}
			s.p[0] = current
			for j := 0; j < count/2; j++ {
				s.p[j+1] = point{float64(cmd.Points[j*2]), float64(cmd.Points[j*2+1])}
			}
			contour = append(contour, s)
			current = s.end()
			continue
		}
		if current != start {
			contour = append(contour, line(current, start))
		}
		cleaned, err := cleanContour(contour)
		if err != nil {
			return nil, fmt.Errorf("contour ending at command %d: %w", i, err)
		}
		if len(cleaned) > 0 {
			output.MoveTo(cleaned[0].p[0].x, cleaned[0].p[0].y)
			for _, s := range cleaned {
				cmd := geometry.Command{Verb: uint32(s.n - 1)}
				for j, p := range s.p[1:s.n] {
					cmd.Points[2*j], cmd.Points[2*j+1] = float32(p.x), float32(p.y)
				}
				output = append(output, cmd)
			}
			output.Close()
		}
		active = false
	}
	if active {
		return nil, fmt.Errorf("path ends before closing contour")
	}
	return output, nil
}
