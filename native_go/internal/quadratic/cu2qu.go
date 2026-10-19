// Package quadratic ports the fontTools cu2qu approximation algorithm.
// Copyright 2015 Google Inc. Licensed under Apache-2.0; see LICENSES.
package quadratic

import (
	"fmt"
	"math"
	"math/cmplx"

	"hd2font/native/internal/geometry"
)

type cubic [4]complex128

func div(z complex128, d float64) complex128 { return complex(real(z)/d, imag(z)/d) }
func split2(c cubic) (cubic, cubic) {
	p0, p1, p2, p3 := c[0], c[1], c[2], c[3]
	m := (p0 + 3*(p1+p2) + p3) * .125
	d := (p3 + p2 - p1 - p0) * .125
	return cubic{p0, (p0 + p1) * .5, m - d, m}, cubic{m, m + d, (p2 + p3) * .5, p3}
}
func split3(c cubic) []cubic {
	a, b, c2, d := c[0], c[1], c[2], c[3]
	m1 := (8*a + 12*b + 6*c2 + d) * complex(1./27, 0)
	d1 := (d + 3*c2 - 4*a) * complex(1./27, 0)
	m2 := (a + 6*b + 12*c2 + 8*d) * complex(1./27, 0)
	d2 := (4*d - 3*b - a) * complex(1./27, 0)
	return []cubic{{a, div(2*a+b, 3), m1 - d1, m1}, {m1, m1 + d1, m2 - d2, m2}, {m2, m2 + d2, div(c2+2*d, 3), d}}
}
func split(c cubic, n int) []cubic {
	if n == 2 {
		a, b := split2(c)
		return []cubic{a, b}
	}
	if n == 3 {
		return split3(c)
	}
	if n == 4 {
		a, b := split2(c)
		aa, ab := split2(a)
		ba, bb := split2(b)
		return []cubic{aa, ab, ba, bb}
	}
	if n == 6 {
		a, b := split2(c)
		return append(split3(a), split3(b)...)
	}
	pc := (c[1] - c[0]) * 3
	pb := (c[2]-c[1])*3 - pc
	pd := c[0]
	pa := c[3] - pd - pc - pb
	dt := 1 / float64(n)
	d2, d3 := dt*dt, dt*dt*dt
	result := make([]cubic, n)
	for i := 0; i < n; i++ {
		t := float64(i) * dt
		t2 := t * t
		a := pa * complex(d3, 0)
		b := (3*pa*complex(t, 0) + pb) * complex(d2, 0)
		cc := (2*pb*complex(t, 0) + pc + 3*pa*complex(t2, 0)) * complex(dt, 0)
		// Preserve FontTools' left-to-right products: a*t*t2 is observably
		// different from a*(t*t2) at half-integer rounding boundaries.
		d := pa*complex(t, 0)*complex(t2, 0) + pb*complex(t2, 0) + pc*complex(t, 0) + pd
		p1 := div(cc, 3) + d
		p2 := div(b+cc, 3) + p1
		result[i] = cubic{d, p1, p2, a + d + cc + b}
	}
	return result
}
func fits(c cubic, tol float64, depth int) bool {
	if cmplx.Abs(c[2]) <= tol && cmplx.Abs(c[1]) <= tol {
		return true
	}
	if depth >= 64 {
		return false
	}
	a, b := split2(c)
	if cmplx.Abs(a[3]) > tol {
		return false
	}
	return fits(a, tol, depth+1) && fits(b, tol, depth+1)
}
func dot(a, b complex128) float64 {
	v := real(a * cmplx.Conj(b))
	if math.Abs(v) < 1e-15 {
		return 0
	}
	return v
}
func control(c cubic, t float64) complex128 {
	a := c[0] + (c[1]-c[0])*1.5
	b := c[3] + (c[2]-c[3])*1.5
	return a + (b-a)*complex(t, 0)
}
func approximate(c cubic, n int, tol float64) []complex128 {
	if n == 1 {
		a, b, cc, d := c[0], c[1], c[2], c[3]
		ab, cd := b-a, d-cc
		p := ab * 1i
		den := dot(p, cd)
		var q complex128
		if den == 0 {
			if b == cc && (a == b || cc == d) {
				q = b
			} else {
				return nil
			}
		} else {
			q = cc + cd*complex(dot(p, a-cc)/den, 0)
		}
		if !fits(cubic{0, a + (q-a)*complex(2./3, 0) - b, d + (q-d)*complex(2./3, 0) - cc, 0}, tol, 0) {
			return nil
		}
		return []complex128{a, q, d}
	}
	cs := split(c, n)
	nextQ := control(cs[0], 0)
	q2 := c[0]
	d1 := complex(0, 0)
	spline := []complex128{c[0], nextQ}
	for i := 1; i <= n; i++ {
		cc := cs[i-1]
		q0, q1 := q2, nextQ
		if i < n {
			nextQ = control(cs[i], float64(i)/float64(n-1))
			spline = append(spline, nextQ)
			q2 = (q1 + nextQ) * .5
		} else {
			q2 = cc[3]
		}
		d0 := d1
		d1 = q2 - cc[3]
		if cmplx.Abs(d1) > tol || !fits(cubic{d0, q0 + (q1-q0)*complex(2./3, 0) - cc[1], q2 + (q1-q2)*complex(2./3, 0) - cc[2], d1}, tol, 0) {
			return nil
		}
	}
	return append(spline, c[3])
}

func Curve(points [4][2]float64, tolerance float64) ([][2]float64, error) {
	if tolerance <= 0 || math.IsNaN(tolerance) || math.IsInf(tolerance, 0) {
		return nil, fmt.Errorf("invalid quadratic tolerance")
	}
	var c cubic
	for i, p := range points {
		if math.IsNaN(p[0]) || math.IsNaN(p[1]) || math.IsInf(p[0], 0) || math.IsInf(p[1], 0) {
			return nil, fmt.Errorf("non-finite cubic")
		}
		c[i] = complex(p[0], p[1])
	}
	for n := 1; n <= 100; n++ {
		if spline := approximate(c, n, tolerance); spline != nil {
			result := make([][2]float64, len(spline))
			for i, p := range spline {
				result[i] = [2]float64{real(p), imag(p)}
			}
			return result, nil
		}
	}
	return nil, fmt.Errorf("no quadratic approximation within tolerance")
}

func Path(path geometry.Path, tolerance float64, reverse bool) (geometry.Path, error) {
	var result geometry.Path
	var current [2]float64
	for _, c := range path {
		switch c.Verb {
		case geometry.Move, geometry.Line:
			current = [2]float64{float64(c.Points[0]), float64(c.Points[1])}
			result = append(result, c)
		case geometry.Quad:
			current = [2]float64{float64(c.Points[2]), float64(c.Points[3])}
			result = append(result, c)
		case geometry.Cubic:
			pts := [4][2]float64{current, {float64(c.Points[0]), float64(c.Points[1])}, {float64(c.Points[2]), float64(c.Points[3])}, {float64(c.Points[4]), float64(c.Points[5])}}
			spline, err := Curve(pts, tolerance)
			if err != nil {
				return nil, err
			}
			for i := 1; i < len(spline)-1; i++ {
				end := spline[i+1]
				if i+2 < len(spline) {
					end = [2]float64{(spline[i][0] + end[0]) * .5, (spline[i][1] + end[1]) * .5}
				}
				result.QuadTo(spline[i][0], spline[i][1], end[0], end[1])
			}
			current = pts[3]
		case geometry.Close:
			result = append(result, c)
		default:
			return nil, fmt.Errorf("invalid outline verb")
		}
	}
	if reverse {
		return geometry.Reverse(result)
	}
	return result, nil
}
