package sfntio

import (
	"encoding/binary"
	"fmt"
	"math"

	"hd2font/native/internal/geometry"
)

// Glyph is an unhinted simple TrueType glyph. Data is unpadded; an empty outline
// has nil Data. Bounds include on- and off-curve points in xMin,yMin,xMax,yMax order.
// Callers retain glyph IDs when placing this data into glyf and rebuilding loca.
type Glyph struct {
	Data     []byte
	Bounds   [4]int16
	Points   uint16
	Contours uint16
}

type glyphPoint struct {
	x, y   float32
	ix, iy int16
	on     bool
}

func newPoint(x, y float32, on bool) (glyphPoint, error) {
	px, py := float64(x), float64(y)
	rx, ry := math.Floor(px+0.5), math.Floor(py+0.5)
	if math.IsNaN(rx) || math.IsNaN(ry) || rx < math.MinInt16 || rx > math.MaxInt16 || ry < math.MinInt16 || ry > math.MaxInt16 {
		return glyphPoint{}, fmt.Errorf("glyph point (%g,%g) is not a finite int16 coordinate after OT rounding", x, y)
	}
	return glyphPoint{x: x, y: y, ix: int16(rx), iy: int16(ry), on: on}, nil
}

func nearlyEqual(x, y float32) bool {
	const tolerance float32 = 1.0 / 4096
	return float32(float32(x*x)+float32(y*y)) <= tolerance*tolerance
}

func isImplied(previous, point, next glyphPoint) bool {
	midX := float32(float32(previous.x+next.x) * 0.5)
	midY := float32(float32(previous.y+next.y) * 0.5)
	return point.on && !previous.on && !next.on && nearlyEqual(point.x-midX, point.y-midY)
}

func finishContour(contour []glyphPoint, closed, join bool, points *[]glyphPoint, ends *[]uint16) error {
	if len(contour) <= 1 {
		return nil
	}
	first, last := contour[0], contour[len(contour)-1]
	joinsStart := last.on && ((first.x == last.x && first.y == last.y) || (join && closed && nearlyEqual(first.x-last.x, first.y-last.y)))
	if joinsStart {
		contour = contour[:len(contour)-1]
	}
	start := len(*points)
	allQuadratic := true
	for i, point := range contour {
		if join && i > 0 && i+1 < len(contour) && isImplied(contour[i-1], point, contour[i+1]) {
			continue
		}
		if i > 0 && point.on {
			allQuadratic = false
		}
		*points = append(*points, point)
	}
	// pathops drops the starting point only for a closed contour consisting
	// entirely of one joined quadratic spline, before TTGlyphPen rounds points.
	if join && closed && joinsStart && allQuadratic && len(*points)-start >= 2 && isImplied((*points)[len(*points)-1], first, (*points)[start+1]) {
		copy((*points)[start:], (*points)[start+1:])
		*points = (*points)[:len(*points)-1]
	}
	if len(*points) == start {
		return nil
	}
	if len(*points) > math.MaxUint16 {
		return fmt.Errorf("simple glyph exceeds 65535 stored points")
	}
	if len(*ends) == math.MaxInt16 {
		return fmt.Errorf("simple glyph exceeds 32767 contours")
	}
	*ends = append(*ends, uint16(len(*points)-1))
	return nil
}

func coordinateDelta(value, previous int16, shortFlag, sameFlag byte, data *[]byte) (byte, error) {
	delta := int32(value) - int32(previous)
	if delta < math.MinInt16 || delta > math.MaxInt16 {
		return 0, fmt.Errorf("glyph coordinate delta %d exceeds int16", delta)
	}
	if delta == 0 {
		return sameFlag, nil
	}
	if delta > 0 && delta <= math.MaxUint8 {
		*data = append(*data, byte(delta))
		return shortFlag | sameFlag, nil
	}
	if delta < 0 && delta >= -math.MaxUint8 {
		*data = append(*data, byte(-delta))
		return shortFlag, nil
	}
	*data = binary.BigEndian.AppendUint16(*data, uint16(int16(delta)))
	return 0, nil
}

// EncodeGlyph encodes Move/Line/Quad/Close commands without changing contour
// order. Cubics and unknown verbs are rejected. Open contours close implicitly
// at the next Move or path end; move-only contours and redundant Close are empty.
// Midpoint joining follows pathops.draw -> TTGlyphPen.glyph(default options),
// including its float32 tolerance. Retained coordinates use floor(x+0.5).
func EncodeGlyph(path geometry.Path) (Glyph, error) {
	return encodeGlyph(path, true)
}

// EncodeGlyphExplicit matches separate TTGlyphPen qCurveTo calls. Cleanup
// intentionally serializes newly explicit half-grid endpoints before rereading.
func EncodeGlyphExplicit(path geometry.Path) (Glyph, error) { return encodeGlyph(path, false) }

func encodeGlyph(path geometry.Path, join bool) (Glyph, error) {
	var contour, points []glyphPoint
	var ends []uint16
	for i, command := range path {
		switch command.Verb {
		case geometry.Move:
			if err := finishContour(contour, false, join, &points, &ends); err != nil {
				return Glyph{}, err
			}
			point, err := newPoint(command.Points[0], command.Points[1], true)
			if err != nil {
				return Glyph{}, fmt.Errorf("command %d: %w", i, err)
			}
			contour = append(contour[:0], point)
		case geometry.Line, geometry.Quad:
			if len(contour) == 0 {
				return Glyph{}, fmt.Errorf("command %d draws before Move", i)
			}
			count := 1
			if command.Verb == geometry.Quad {
				count = 2
			}
			for j := range count {
				point, err := newPoint(command.Points[2*j], command.Points[2*j+1], j == count-1)
				if err != nil {
					return Glyph{}, fmt.Errorf("command %d: %w", i, err)
				}
				contour = append(contour, point)
			}
		case geometry.Close:
			if err := finishContour(contour, true, join, &points, &ends); err != nil {
				return Glyph{}, err
			}
			contour = contour[:0]
		case geometry.Cubic:
			return Glyph{}, fmt.Errorf("command %d: cubic curves cannot be encoded as simple TrueType quadratics", i)
		default:
			return Glyph{}, fmt.Errorf("command %d: unknown geometry verb %d", i, command.Verb)
		}
	}
	if err := finishContour(contour, false, join, &points, &ends); err != nil {
		return Glyph{}, err
	}
	return packGlyph(points, ends)
}

// OutlinePoint retains double precision until TrueType's final integer grid.
// OnCurve distinguishes explicit endpoints from quadratic control points.
type OutlinePoint struct {
	X, Y    float64
	OnCurve bool
}

// EncodeContours serializes TTGlyphPen-style point lists. Implied endpoints are
// already absent: rounding a float32 path first can move a half-unit control to
// the other side of the rounding boundary during cubic-to-quadratic conversion.
func EncodeContours(contours [][]OutlinePoint) (Glyph, error) {
	var points []glyphPoint
	var ends []uint16
	for _, contour := range contours {
		if len(contour) > 1 && contour[0].OnCurve && contour[len(contour)-1].OnCurve && contour[0] == contour[len(contour)-1] {
			contour = contour[:len(contour)-1]
		}
		if len(contour) <= 1 {
			continue
		}
		for _, p := range contour {
			x, y := math.Floor(p.X+.5), math.Floor(p.Y+.5)
			if math.IsNaN(x) || math.IsNaN(y) || x < math.MinInt16 || x > math.MaxInt16 || y < math.MinInt16 || y > math.MaxInt16 {
				return Glyph{}, fmt.Errorf("quadratic coordinate exceeds int16")
			}
			points = append(points, glyphPoint{ix: int16(x), iy: int16(y), on: p.OnCurve})
		}
		if len(points) > math.MaxUint16 || len(ends) >= math.MaxInt16 {
			return Glyph{}, fmt.Errorf("quadratic glyph exceeds storage")
		}
		ends = append(ends, uint16(len(points)-1))
	}
	return packGlyph(points, ends)
}

func packGlyph(points []glyphPoint, ends []uint16) (Glyph, error) {
	var glyph Glyph
	if len(points) == 0 {
		return glyph, nil
	}
	glyph.Points, glyph.Contours = uint16(len(points)), uint16(len(ends))
	glyph.Bounds = [4]int16{points[0].ix, points[0].iy, points[0].ix, points[0].iy}
	flags := make([]byte, len(points))
	var xBytes, yBytes []byte
	var previousX, previousY int16
	for i, point := range points {
		if point.on {
			flags[i] = 1
		}
		xFlag, err := coordinateDelta(point.ix, previousX, 2, 16, &xBytes)
		if err != nil {
			return Glyph{}, fmt.Errorf("point %d x: %w", i, err)
		}
		yFlag, err := coordinateDelta(point.iy, previousY, 4, 32, &yBytes)
		if err != nil {
			return Glyph{}, fmt.Errorf("point %d y: %w", i, err)
		}
		flags[i] |= xFlag | yFlag
		previousX, previousY = point.ix, point.iy
		glyph.Bounds[0] = min(glyph.Bounds[0], point.ix)
		glyph.Bounds[1] = min(glyph.Bounds[1], point.iy)
		glyph.Bounds[2] = max(glyph.Bounds[2], point.ix)
		glyph.Bounds[3] = max(glyph.Bounds[3], point.iy)
	}
	data := make([]byte, 10, 12+2*len(ends)+len(flags)+len(xBytes)+len(yBytes))
	binary.BigEndian.PutUint16(data, glyph.Contours)
	for i, bound := range glyph.Bounds {
		binary.BigEndian.PutUint16(data[2+2*i:], uint16(bound))
	}
	for _, end := range ends {
		data = binary.BigEndian.AppendUint16(data, end)
	}
	data = binary.BigEndian.AppendUint16(data, 0) // No glyph instructions.
	for i := 0; i < len(flags); {
		run := 1
		for run < 256 && i+run < len(flags) && flags[i+run] == flags[i] {
			run++
		}
		if run > 1 {
			data = append(data, flags[i]|8, byte(run-1))
		} else {
			data = append(data, flags[i])
		}
		i += run
	}
	data = append(data, xBytes...)
	glyph.Data = append(data, yBytes...)
	return glyph, nil
}
