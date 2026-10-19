package fontcore

import (
	"encoding/binary"
	"math"

	"hd2font/native/internal/geometry"
	"hd2font/native/internal/sfntio"
)

func pathBounds(path geometry.Path) ([4]int16, bool) {
	minx, miny, maxx, maxy := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	current, start := [2]float64{}, [2]float64{}
	ink := false
	add := func(x, y float64) {
		minx = math.Min(minx, x)
		maxx = math.Max(maxx, x)
		miny = math.Min(miny, y)
		maxy = math.Max(maxy, y)
	}
	for _, c := range path {
		switch c.Verb {
		case geometry.Move:
			current = [2]float64{float64(c.Points[0]), float64(c.Points[1])}
			start = current
		case geometry.Line:
			next := [2]float64{float64(c.Points[0]), float64(c.Points[1])}
			add(current[0], current[1])
			add(next[0], next[1])
			current = next
			ink = true
		case geometry.Quad, geometry.Cubic:
			points := [][2]float64{current, {float64(c.Points[0]), float64(c.Points[1])}, {float64(c.Points[2]), float64(c.Points[3])}}
			if c.Verb == geometry.Cubic {
				points = append(points, [2]float64{float64(c.Points[4]), float64(c.Points[5])})
			}
			add(current[0], current[1])
			last := points[len(points)-1]
			add(last[0], last[1])
			evaluate := func(t float64) {
				q := append([][2]float64(nil), points...)
				for n := len(q) - 1; n > 0; n-- {
					for i := 0; i < n; i++ {
						for a := 0; a < 2; a++ {
							q[i][a] = (1-t)*q[i][a] + t*q[i+1][a]
						}
					}
				}
				add(q[0][0], q[0][1])
			}
			for a := 0; a < 2; a++ {
				p0, p1, p2 := points[0][a], points[1][a], points[2][a]
				if len(points) == 3 {
					den := p0 - 2*p1 + p2
					if den != 0 {
						t := (p0 - p1) / den
						if t > 0 && t < 1 {
							evaluate(t)
						}
					}
				} else {
					p3 := points[3][a]
					aa := -p0 + 3*p1 - 3*p2 + p3
					bb := 2 * (p0 - 2*p1 + p2)
					cc := p1 - p0
					if aa == 0 {
						if bb != 0 {
							t := -cc / bb
							if t > 0 && t < 1 {
								evaluate(t)
							}
						}
					} else {
						d := bb*bb - 4*aa*cc
						if d >= 0 {
							root := math.Sqrt(d)
							for _, t := range []float64{(-bb + root) / (2 * aa), (-bb - root) / (2 * aa)} {
								if t > 0 && t < 1 {
									evaluate(t)
								}
							}
						}
					}
				}
			}
			current = last
			ink = true
		case geometry.Close:
			current = start
		}
	}
	if !ink {
		return [4]int16{}, false
	}
	return [4]int16{int16(math.Floor(minx)), int16(math.Floor(miny)), int16(math.Ceil(maxx)), int16(math.Ceil(maxy))}, true
}
func unionBounds(bounds [][4]int16, nonempty []bool) [4]int16 {
	var box [4]int16
	found := false
	for i, b := range bounds {
		if !nonempty[i] {
			continue
		}
		if !found {
			box = b
			found = true
		} else {
			box[0] = min(box[0], b[0])
			box[1] = min(box[1], b[1])
			box[2] = max(box[2], b[2])
			box[3] = max(box[3], b[3])
		}
	}
	return box
}
func setMetrics(f *sfntio.Font, advances []uint16, bearings []int16, bounds [][4]int16, nonempty []bool) {
	be := binary.BigEndian
	hmtx := make([]byte, len(advances)*4)
	maxAdvance := uint16(0)
	minLSB, minRSB, maxExtent := 32767, 32767, -32768
	hasInk := false
	aligned := true
	for i, a := range advances {
		be.PutUint16(hmtx[i*4:], a)
		be.PutUint16(hmtx[i*4+2:], uint16(bearings[i]))
		maxAdvance = max(maxAdvance, a)
		if nonempty[i] {
			b := bounds[i]
			w := int(b[2]) - int(b[0])
			lsb := int(bearings[i])
			minLSB = min(minLSB, lsb)
			minRSB = min(minRSB, int(a)-lsb-w)
			maxExtent = max(maxExtent, lsb+w)
			hasInk = true
			aligned = aligned && bearings[i] == b[0]
		}
	}
	f.Tables["hmtx"] = hmtx
	h := f.Tables["hhea"]
	be.PutUint16(h[10:], maxAdvance)
	be.PutUint16(h[34:], uint16(len(advances)))
	if hasInk {
		be.PutUint16(h[12:], uint16(minLSB))
		be.PutUint16(h[14:], uint16(minRSB))
		be.PutUint16(h[16:], uint16(maxExtent))
	}
	box := unionBounds(bounds, nonempty)
	for i, v := range box {
		be.PutUint16(f.Tables["head"][36+2*i:], uint16(v))
	}
	flags := be.Uint16(f.Tables["head"][16:])
	if aligned {
		flags |= 2
	} else {
		flags &^= 2
	}
	be.PutUint16(f.Tables["head"][16:], flags)
}
func setTTFGlyphs(f *sfntio.Font, glyphs []sfntio.Glyph, advances []uint16, bearings []int16, keepHints bool) error {
	var data []byte
	loca := make([]byte, 4*(len(glyphs)+1))
	bounds := make([][4]int16, len(glyphs))
	nonempty := make([]bool, len(glyphs))
	var previous []byte
	if keepHints {
		previous = f.Tables["maxp"]
	}
	be := binary.BigEndian
	rawGlyphs := make([][]byte, len(glyphs))
	for i, g := range glyphs {
		be.PutUint32(loca[4*i:], uint32(len(data)))
		data = append(data, g.Data...)
		if len(data)%2 != 0 {
			data = append(data, 0)
		}
		bounds[i] = g.Bounds
		nonempty[i] = g.Contours > 0
		rawGlyphs[i] = g.Data
	}
	be.PutUint32(loca[4*len(glyphs):], uint32(len(data)))
	maxp, err := ttfMaximumProfile(previous, rawGlyphs)
	if err != nil {
		return err
	}
	f.Tables["glyf"], f.Tables["loca"], f.Tables["maxp"] = data, loca, maxp
	f.Version = 0x10000
	be.PutUint16(f.Tables["head"][50:], 1)
	for _, tag := range []string{"CFF ", "CFF2", "VORG"} {
		delete(f.Tables, tag)
	}
	if !keepHints {
		for _, tag := range []string{"fpgm", "prep", "cvt "} {
			delete(f.Tables, tag)
		}
	}
	post := make([]byte, 32)
	copy(post, f.Tables["post"])
	be.PutUint32(post, 0x30000)
	f.Tables["post"] = post
	setMetrics(f, advances, bearings, bounds, nonempty)
	return nil
}
