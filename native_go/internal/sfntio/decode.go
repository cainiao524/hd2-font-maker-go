package sfntio

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"hd2font/native/internal/geometry"
)

type glyphReader struct {
	data []byte
	at   int
}

func (r *glyphReader) take(size int) ([]byte, error) {
	if size < 0 || size > len(r.data)-r.at {
		return nil, io.ErrUnexpectedEOF
	}
	data := r.data[r.at : r.at+size]
	r.at += size
	return data, nil
}

func (r *glyphReader) coordinate(flag, short, same byte, previous int32) (int16, error) {
	var delta int32
	if flag&short != 0 {
		data, err := r.take(1)
		if err != nil {
			return 0, err
		}
		delta = int32(data[0])
		if flag&same == 0 {
			delta = -delta
		}
	} else if flag&same == 0 {
		data, err := r.take(2)
		if err != nil {
			return 0, err
		}
		delta = int32(int16(binary.BigEndian.Uint16(data)))
	}
	value := previous + delta
	if value < math.MinInt16 || value > math.MaxInt16 {
		return 0, fmt.Errorf("glyph coordinate %d exceeds int16", value)
	}
	return int16(value), nil
}

type decodedPoint struct {
	x, y int16
	on   bool
}

// DecodeGlyph reads empty or simple TrueType glyph bytes without parsing a font.
// Composite glyphs are rejected. Instructions are skipped, trailing glyph padding
// is allowed, and implied midpoints use int32 sums before conversion to float32.
// Contours start at their first explicit on-curve point, matching fontTools'
// Glyph.draw. An entirely off-curve contour starts at the first/last midpoint.
func DecodeGlyph(data []byte) (geometry.Path, error) {
	return DecodeGlyphScaled(data, 1)
}

// DecodeGlyphScaled rounds stored coordinates before creating implied points,
// matching scale_upem's TrueType grid behavior.
func DecodeGlyphScaled(data []byte, ratio float64) (geometry.Path, error) {
	if ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return nil, fmt.Errorf("invalid glyph scale")
	}
	if len(data) == 0 {
		return nil, nil
	}
	r := glyphReader{data: data}
	header, err := r.take(10)
	if err != nil {
		return nil, fmt.Errorf("glyph header: %w", err)
	}
	contours := int(int16(binary.BigEndian.Uint16(header)))
	if contours < 0 {
		return nil, fmt.Errorf("composite glyph decoding is not supported")
	}
	if contours == 0 && len(data) == 10 {
		return nil, nil
	}
	endData, err := r.take(2 * contours)
	if err != nil {
		return nil, fmt.Errorf("glyph contour endpoints: %w", err)
	}
	ends := make([]int, contours)
	previousEnd := -1
	for i := range ends {
		ends[i] = int(binary.BigEndian.Uint16(endData[2*i:]))
		if ends[i] <= previousEnd {
			return nil, fmt.Errorf("glyph contour endpoints are not increasing")
		}
		previousEnd = ends[i]
	}
	length, err := r.take(2)
	if err != nil {
		return nil, fmt.Errorf("glyph instruction length: %w", err)
	}
	if _, err = r.take(int(binary.BigEndian.Uint16(length))); err != nil {
		return nil, fmt.Errorf("glyph instructions: %w", err)
	}
	if contours == 0 {
		return nil, nil
	}
	flags := make([]byte, previousEnd+1)
	for i := 0; i < len(flags); {
		value, err := r.take(1)
		if err != nil {
			return nil, fmt.Errorf("glyph flags: %w", err)
		}
		flag, repeat := value[0], 1
		if flag&0x80 != 0 {
			return nil, fmt.Errorf("unsupported glyph flag %#x", flag)
		}
		if flag&8 != 0 {
			value, err = r.take(1)
			if err != nil {
				return nil, fmt.Errorf("glyph repeated flags: %w", err)
			}
			repeat += int(value[0])
		}
		if repeat > len(flags)-i {
			return nil, fmt.Errorf("glyph repeated flags exceed point count")
		}
		for j := range repeat {
			flags[i+j] = flag
		}
		i += repeat
	}
	points := make([]decodedPoint, len(flags))
	var previous int32
	for i, flag := range flags {
		points[i].x, err = r.coordinate(flag, 2, 16, previous)
		if err != nil {
			return nil, fmt.Errorf("glyph point %d x: %w", i, err)
		}
		points[i].on = flag&1 != 0
		previous = int32(points[i].x)
	}
	previous = 0
	for i, flag := range flags {
		points[i].y, err = r.coordinate(flag, 4, 32, previous)
		if err != nil {
			return nil, fmt.Errorf("glyph point %d y: %w", i, err)
		}
		previous = int32(points[i].y)
	}
	path := make(geometry.Path, 0, len(points)+2*contours)
	if ratio != 1 {
		for i := range points {
			x, y := math.Floor(float64(points[i].x)*ratio+.5), math.Floor(float64(points[i].y)*ratio+.5)
			if x < math.MinInt16 || x > math.MaxInt16 || y < math.MinInt16 || y > math.MaxInt16 {
				return nil, fmt.Errorf("scaled glyph coordinate exceeds int16")
			}
			points[i].x, points[i].y = int16(x), int16(y)
		}
	}
	start := 0
	for _, end := range ends {
		path = appendContour(path, points[start:end+1])
		start = end + 1
	}
	return path, nil
}

func appendContour(path geometry.Path, points []decodedPoint) geometry.Path {
	first, last := points[0], points[len(points)-1]
	var startX, startY float32
	firstOn := -1
	for i, p := range points {
		if p.on {
			firstOn = i
			break
		}
	}
	if firstOn >= 0 {
		startX, startY = float32(points[firstOn].x), float32(points[firstOn].y)
	} else {
		startX = float32(int32(first.x)+int32(last.x)) * 0.5
		startY = float32(int32(first.y)+int32(last.y)) * 0.5
	}
	path = append(path, geometry.Command{Verb: geometry.Move, Points: [6]float32{startX, startY}})
	var control decodedPoint
	pending := false
	for i := 0; i < len(points); i++ {
		index := i
		if firstOn >= 0 {
			index = (firstOn + 1 + i) % len(points)
		}
		point := points[index]
		if point.on {
			if pending {
				path = append(path, geometry.Command{Verb: geometry.Quad, Points: [6]float32{float32(control.x), float32(control.y), float32(point.x), float32(point.y)}})
			} else if firstOn < 0 || i+1 < len(points) {
				path = append(path, geometry.Command{Verb: geometry.Line, Points: [6]float32{float32(point.x), float32(point.y)}})
			}
			pending = false
		} else {
			if pending {
				midX := float32(int32(control.x)+int32(point.x)) * 0.5
				midY := float32(int32(control.y)+int32(point.y)) * 0.5
				path = append(path, geometry.Command{Verb: geometry.Quad, Points: [6]float32{float32(control.x), float32(control.y), midX, midY}})
			}
			control, pending = point, true
		}
	}
	if pending {
		path = append(path, geometry.Command{Verb: geometry.Quad, Points: [6]float32{float32(control.x), float32(control.y), startX, startY}})
	}
	return append(path, geometry.Command{Verb: geometry.Close})
}
