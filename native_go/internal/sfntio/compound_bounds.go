package sfntio

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Composite component flag bits used by glyf. The names follow the OpenType
// specification and FontTools' _g_l_y_f.py.
const (
	componentArgsAreWords   = 1 << 0
	componentArgsAreXY      = 1 << 1
	componentHaveScale      = 1 << 3
	componentMore           = 1 << 5
	componentHaveXYScale    = 1 << 6
	componentHave2x2        = 1 << 7
	componentHaveInstr      = 1 << 8
	componentScaledOffset   = 1 << 11
	componentUnscaledOffset = 1 << 12
)

type boundsPoint struct {
	x, y float64
}

type componentMatrix struct {
	xx, xy, yx, yy float64
}

func (m componentMatrix) apply(points []boundsPoint) {
	for i := range points {
		x, y := points[i].x, points[i].y
		points[i].x = x*m.xx + y*m.yx
		points[i].y = x*m.xy + y*m.yy
	}
}

func translatePoints(points []boundsPoint, x, y float64) {
	if x == 0 && y == 0 {
		return
	}
	for i := range points {
		points[i].x += x
		points[i].y += y
	}
}

// RecalculateCompositeBounds rewrites the four bounding-box fields in the
// ten-byte headers of composite glyphs. offsets contains the loca values for
// the glyphs in glyf (len(offsets) == numGlyphs+1). Simple glyphs, component
// records, transforms, flags, instructions and metrics are left untouched.
//
// The calculation follows FontTools' Glyph.getCoordinates followed by
// GlyphCoordinates.calcIntBounds(round=otRound). Simple points are therefore
// read as their stored integer coordinates; nested composite points remain
// floating point through every transform, and only the final min/max values are
// rounded. In particular, this deliberately uses the coordinate path rather
// than scaling a child's already-rounded composite bbox.
//
// Coordinates of glyphs reached from a composite are cached. Simple glyphs that
// are not referenced by a composite are never decoded, so an unrelated large
// simple outline does not consume resolver memory. Recalculation is validated
// for every composite before any header bytes are changed.
func RecalculateCompositeBounds(glyf []byte, offsets []uint32) error {
	if len(offsets) == 0 {
		return fmt.Errorf("glyph offsets are empty")
	}
	for i, offset := range offsets {
		if uint64(offset) > uint64(len(glyf)) {
			return fmt.Errorf("glyph offset %d (%d) exceeds glyf size %d", i, offset, len(glyf))
		}
		if i > 0 && offset < offsets[i-1] {
			return fmt.Errorf("glyph offsets decrease at entry %d", i)
		}
	}
	numGlyphs := len(offsets) - 1
	if numGlyphs > math.MaxUint16 {
		return fmt.Errorf("glyph count %d exceeds uint16 glyph ids", numGlyphs)
	}

	resolver := compositeBoundsResolver{
		glyf:     glyf,
		offsets:  offsets,
		cache:    make(map[uint16][]boundsPoint),
		done:     make(map[uint16]bool),
		visiting: make(map[uint16]bool),
	}
	boxes := make(map[uint16][4]int16)
	for id := 0; id < numGlyphs; id++ {
		start, end := int(offsets[id]), int(offsets[id+1])
		if start == end {
			continue
		}
		if end-start < 10 {
			return fmt.Errorf("glyph %d: truncated glyph header", id)
		}
		contours := int16(binary.BigEndian.Uint16(glyf[start : start+2]))
		if contours >= 0 {
			continue
		}
		if contours < -1 {
			return fmt.Errorf("glyph %d: invalid negative contour count %d", id, contours)
		}
		points, err := resolver.resolve(uint16(id))
		if err != nil {
			return fmt.Errorf("glyph %d: %w", id, err)
		}
		box, err := boundsFromPoints(points)
		if err != nil {
			return fmt.Errorf("glyph %d: %w", id, err)
		}
		boxes[uint16(id)] = box
	}

	// Do not leave a partially updated glyf behind when a later composite is
	// malformed. Only the four header bbox fields are written.
	for id, box := range boxes {
		start := int(offsets[id])
		for i, value := range box {
			binary.BigEndian.PutUint16(glyf[start+2+2*i:], uint16(value))
		}
	}
	return nil
}

type compositeBoundsResolver struct {
	glyf     []byte
	offsets  []uint32
	cache    map[uint16][]boundsPoint
	done     map[uint16]bool
	visiting map[uint16]bool
}

func (r *compositeBoundsResolver) resolve(id uint16) ([]boundsPoint, error) {
	if r.done[id] {
		return r.cache[id], nil
	}
	if r.visiting[id] {
		return nil, fmt.Errorf("cyclic composite reference at glyph %d", id)
	}
	if int(id)+1 >= len(r.offsets) {
		return nil, fmt.Errorf("component glyph %d is out of range", id)
	}
	data := r.glyf[int(r.offsets[id]):int(r.offsets[id+1])]
	if len(data) == 0 {
		r.done[id] = true
		return nil, nil
	}
	if len(data) < 10 {
		return nil, fmt.Errorf("truncated glyph header")
	}
	contours := int16(binary.BigEndian.Uint16(data[:2]))
	switch {
	case contours == 0:
		// Zero-contour glyphs have no outline points. Instructions, if present,
		// do not contribute coordinates.
		r.done[id] = true
		return nil, nil
	case contours > 0:
		points, err := simpleGlyphPoints(data, int(contours))
		if err != nil {
			return nil, err
		}
		r.cache[id] = points
		r.done[id] = true
		return points, nil
	case contours < -1:
		return nil, fmt.Errorf("invalid negative contour count %d", contours)
	}

	r.visiting[id] = true
	points, err := r.compositeGlyphPoints(data)
	delete(r.visiting, id)
	if err != nil {
		return nil, err
	}
	r.cache[id] = points
	r.done[id] = true
	return points, nil
}

func simpleGlyphPoints(data []byte, contours int) ([]boundsPoint, error) {
	r := glyphReader{data: data}
	if _, err := r.take(10); err != nil {
		return nil, fmt.Errorf("truncated glyph header")
	}
	endData, err := r.take(2 * contours)
	if err != nil {
		return nil, fmt.Errorf("truncated glyph contour endpoints")
	}
	previousEnd := -1
	for i := 0; i < contours; i++ {
		end := int(binary.BigEndian.Uint16(endData[2*i:]))
		if end <= previousEnd {
			return nil, fmt.Errorf("glyph contour endpoints are not increasing")
		}
		previousEnd = end
	}
	pointCount := previousEnd + 1
	lengthData, err := r.take(2)
	if err != nil {
		return nil, fmt.Errorf("truncated glyph instruction length")
	}
	if _, err := r.take(int(binary.BigEndian.Uint16(lengthData))); err != nil {
		return nil, fmt.Errorf("truncated glyph instructions")
	}

	flags := make([]byte, pointCount)
	for i := 0; i < pointCount; {
		flagData, err := r.take(1)
		if err != nil {
			return nil, fmt.Errorf("truncated glyph flags")
		}
		flag := flagData[0]
		repeat := 1
		if flag&8 != 0 {
			repeatData, err := r.take(1)
			if err != nil {
				return nil, fmt.Errorf("truncated repeated glyph flags")
			}
			repeat += int(repeatData[0])
		}
		if repeat > pointCount-i {
			return nil, fmt.Errorf("repeated glyph flags exceed point count")
		}
		for j := 0; j < repeat; j++ {
			flags[i+j] = flag
		}
		i += repeat
	}

	points := make([]boundsPoint, pointCount)
	var previous int32
	for i, flag := range flags {
		x, err := r.coordinate(flag, 2, 16, previous)
		if err != nil {
			return nil, fmt.Errorf("glyph point %d x: %w", i, err)
		}
		points[i].x = float64(x)
		previous = int32(x)
	}
	previous = 0
	for i, flag := range flags {
		y, err := r.coordinate(flag, 4, 32, previous)
		if err != nil {
			return nil, fmt.Errorf("glyph point %d y: %w", i, err)
		}
		points[i].y = float64(y)
		previous = int32(y)
	}
	return points, nil
}

func (r *compositeBoundsResolver) compositeGlyphPoints(data []byte) ([]boundsPoint, error) {
	reader := glyphReader{data: data}
	if _, err := reader.take(10); err != nil {
		return nil, fmt.Errorf("truncated glyph header")
	}
	var all []boundsPoint
	haveInstructions := false
	for {
		row, err := reader.take(4)
		if err != nil {
			return nil, fmt.Errorf("truncated composite component")
		}
		flags := binary.BigEndian.Uint16(row)
		glyphID := binary.BigEndian.Uint16(row[2:])
		if flags&componentHaveInstr != 0 {
			haveInstructions = true
		}
		words := flags&componentArgsAreWords != 0
		xy := flags&componentArgsAreXY != 0

		var arg1, arg2 int32
		if words {
			args, err := reader.take(4)
			if err != nil {
				return nil, fmt.Errorf("truncated composite arguments")
			}
			if xy {
				arg1 = int32(int16(binary.BigEndian.Uint16(args)))
				arg2 = int32(int16(binary.BigEndian.Uint16(args[2:])))
			} else {
				arg1 = int32(binary.BigEndian.Uint16(args))
				arg2 = int32(binary.BigEndian.Uint16(args[2:]))
			}
		} else {
			args, err := reader.take(2)
			if err != nil {
				return nil, fmt.Errorf("truncated composite arguments")
			}
			if xy {
				arg1 = int32(int8(args[0]))
				arg2 = int32(int8(args[1]))
			} else {
				arg1 = int32(args[0])
				arg2 = int32(args[1])
			}
		}

		matrix, hasTransform, err := readComponentTransform(&reader, flags)
		if err != nil {
			return nil, err
		}
		child, err := r.resolve(glyphID)
		if err != nil {
			return nil, fmt.Errorf("component glyph %d: %w", glyphID, err)
		}
		points := append([]boundsPoint(nil), child...)
		if !xy {
			// Point attachment applies the component transform before comparing
			// the two anchor points. firstPt indexes all points accumulated from
			// previous components; secondPt indexes the transformed child.
			if hasTransform {
				matrix.apply(points)
			}
			if arg1 < 0 || arg2 < 0 || int(arg1) >= len(all) || int(arg2) >= len(points) {
				return nil, fmt.Errorf("point attachment (%d,%d) is out of range", arg1, arg2)
			}
			moveX := all[int(arg1)].x - points[int(arg2)].x
			moveY := all[int(arg1)].y - points[int(arg2)].y
			translatePoints(points, moveX, moveY)
		} else if !hasTransform {
			translatePoints(points, float64(arg1), float64(arg2))
		} else {
			apple := flags&componentScaledOffset != 0
			ms := flags&componentUnscaledOffset != 0
			if apple && ms {
				return nil, fmt.Errorf("composite component sets both scaled and unscaled offset flags")
			}
			if apple {
				// Apple: move first, then transform, which scales the offset.
				translatePoints(points, float64(arg1), float64(arg2))
				matrix.apply(points)
			} else {
				// Microsoft, including the default when neither flag is set:
				// transform first, then move without scaling the offset.
				matrix.apply(points)
				translatePoints(points, float64(arg1), float64(arg2))
			}
		}
		all = append(all, points...)
		if flags&componentMore == 0 {
			break
		}
	}
	if haveInstructions {
		length, err := reader.take(2)
		if err != nil {
			return nil, fmt.Errorf("truncated composite instruction length")
		}
		if _, err := reader.take(int(binary.BigEndian.Uint16(length))); err != nil {
			return nil, fmt.Errorf("truncated composite instructions")
		}
	}
	return all, nil
}

func readComponentTransform(reader *glyphReader, flags uint16) (componentMatrix, bool, error) {
	switch {
	case flags&componentHaveScale != 0:
		data, err := reader.take(2)
		if err != nil {
			return componentMatrix{}, false, fmt.Errorf("truncated component scale")
		}
		scale := fixed214(data)
		return componentMatrix{xx: scale, yy: scale}, true, nil
	case flags&componentHaveXYScale != 0:
		data, err := reader.take(4)
		if err != nil {
			return componentMatrix{}, false, fmt.Errorf("truncated component x/y scale")
		}
		return componentMatrix{
			xx: fixed214(data[:2]),
			yy: fixed214(data[2:]),
		}, true, nil
	case flags&componentHave2x2 != 0:
		data, err := reader.take(8)
		if err != nil {
			return componentMatrix{}, false, fmt.Errorf("truncated component two-by-two transform")
		}
		return componentMatrix{
			xx: fixed214(data[:2]),
			xy: fixed214(data[2:4]),
			yx: fixed214(data[4:6]),
			yy: fixed214(data[6:]),
		}, true, nil
	default:
		return componentMatrix{xx: 1, yy: 1}, false, nil
	}
}

func fixed214(data []byte) float64 {
	return float64(int16(binary.BigEndian.Uint16(data))) / 16384
}

func boundsFromPoints(points []boundsPoint) ([4]int16, error) {
	if len(points) == 0 {
		return [4]int16{}, nil
	}
	minX, maxX := points[0].x, points[0].x
	minY, maxY := points[0].y, points[0].y
	for _, point := range points[1:] {
		minX = math.Min(minX, point.x)
		maxX = math.Max(maxX, point.x)
		minY = math.Min(minY, point.y)
		maxY = math.Max(maxY, point.y)
	}
	values := [4]float64{minX, minY, maxX, maxY}
	var box [4]int16
	for i, value := range values {
		rounded := math.Floor(value + 0.5)
		if math.IsNaN(rounded) || math.IsInf(rounded, 0) || rounded < math.MinInt16 || rounded > math.MaxInt16 {
			return [4]int16{}, fmt.Errorf("recalculated composite bounding box exceeds int16")
		}
		box[i] = int16(rounded)
	}
	return box, nil
}
