package sfntio

import (
	"encoding/binary"
	"fmt"

	"hd2font/native/internal/geometry"
)

type pathTransform struct{ xx, xy, yx, yy, x, y float64 }

func (a pathTransform) child(b pathTransform) pathTransform {
	return pathTransform{
		xx: a.xx*b.xx + a.yx*b.xy, xy: a.xy*b.xx + a.yy*b.xy,
		yx: a.xx*b.yx + a.yx*b.yy, yy: a.xy*b.yx + a.yy*b.yy,
		x: a.xx*b.x + a.yx*b.y + a.x, y: a.xy*b.x + a.yy*b.y + a.y,
	}
}

// DecodeCompositeGlyph follows FontTools Glyph.draw's component pen protocol.
// Component transforms retain fractional coordinates until the final Skia path.
// Glyph.draw emits component x/y translations directly, independently of the
// offset flags used by getCoordinates when recalculating stored glyph bounds.
// Like Glyph.draw, this applies no hmtx bearing translation to a composite.
func DecodeCompositeGlyph(glyf []byte, offsets []uint32, id uint16) (geometry.Path, error) {
	for i, off := range offsets {
		if uint64(off) > uint64(len(glyf)) || i > 0 && off < offsets[i-1] {
			return nil, fmt.Errorf("invalid composite path glyph offset")
		}
	}
	visiting := make(map[uint16]bool)
	points := compositeBoundsResolver{glyf: glyf, offsets: offsets,
		cache: make(map[uint16][]boundsPoint), done: make(map[uint16]bool), visiting: make(map[uint16]bool)}
	var result geometry.Path
	var draw func(uint16, pathTransform) error
	draw = func(gid uint16, transform pathTransform) error {
		if int(gid)+1 >= len(offsets) || visiting[gid] {
			return fmt.Errorf("invalid or cyclic composite path glyph %d", gid)
		}
		raw := glyf[offsets[gid]:offsets[int(gid)+1]]
		if len(raw) == 0 {
			return nil
		}
		if len(raw) < 10 {
			return fmt.Errorf("truncated composite path glyph %d", gid)
		}
		if int16(binary.BigEndian.Uint16(raw)) >= 0 {
			path, err := DecodeGlyph(raw)
			if err != nil {
				return err
			}
			for i := range path {
				n := 0
				switch path[i].Verb {
				case geometry.Move, geometry.Line:
					n = 1
				case geometry.Quad:
					n = 2
				case geometry.Cubic:
					n = 3
				}
				for j := 0; j < n; j++ {
					x, y := float64(path[i].Points[2*j]), float64(path[i].Points[2*j+1])
					path[i].Points[2*j] = float32(transform.xx*x + transform.yx*y + transform.x)
					path[i].Points[2*j+1] = float32(transform.xy*x + transform.yy*y + transform.y)
				}
			}
			result = append(result, path...)
			return nil
		}
		visiting[gid] = true
		defer delete(visiting, gid)
		r := glyphReader{data: raw, at: 10}
		var previousPoints []boundsPoint
		for {
			row, err := r.take(4)
			if err != nil {
				return err
			}
			flags, child := binary.BigEndian.Uint16(row), binary.BigEndian.Uint16(row[2:])
			xy := flags&componentArgsAreXY != 0
			var a, b int
			if flags&componentArgsAreWords != 0 {
				args, err := r.take(4)
				if err != nil {
					return err
				}
				a, b = int(binary.BigEndian.Uint16(args)), int(binary.BigEndian.Uint16(args[2:]))
				if xy {
					a, b = int(int16(a)), int(int16(b))
				}
			} else {
				args, err := r.take(2)
				if err != nil {
					return err
				}
				a, b = int(args[0]), int(args[1])
				if xy {
					a, b = int(int8(a)), int(int8(b))
				}
			}
			matrix, _, err := readComponentTransform(&r, flags)
			if err != nil {
				return err
			}
			local := pathTransform{xx: matrix.xx, xy: matrix.xy, yx: matrix.yx, yy: matrix.yy, x: float64(a), y: float64(b)}
			// Coordinates are needed only for point-attached components. Preserve
			// the preceding component points for their unsigned anchor indices.
			childPoints, err := points.resolve(child)
			if err != nil {
				return err
			}
			childPoints = append([]boundsPoint(nil), childPoints...)
			matrix.apply(childPoints)
			if !xy {
				if a >= len(previousPoints) || b >= len(childPoints) {
					return fmt.Errorf("composite path anchor out of range")
				}
				local.x, local.y = previousPoints[a].x-childPoints[b].x, previousPoints[a].y-childPoints[b].y
			}
			translatePoints(childPoints, local.x, local.y)
			previousPoints = append(previousPoints, childPoints...)
			if err := draw(child, transform.child(local)); err != nil {
				return err
			}
			if flags&componentMore == 0 {
				break
			}
		}
		return nil
	}
	err := draw(id, pathTransform{xx: 1, yy: 1})
	return result, err
}
