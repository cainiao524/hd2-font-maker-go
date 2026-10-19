//go:build windows && amd64

package fontcore

// Transform semantics are pinned to Python frontend 1.3.0 / engine 1.3.7.
// Preserve glyph records when normalization and cleanup leave their outlines unchanged.
import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hd2font/native/internal/cffconv"
	"hd2font/native/internal/geometry"
	"hd2font/native/internal/outlinecleanup"
	"hd2font/native/internal/quadratic"
	"hd2font/native/internal/sfntio"
	"slices"
)

type transformOptions struct {
	ttf                    bool
	upm                    uint16
	radius                 float32
	pixel, simplify, clean bool
	fractional             bool
}

func rawScale(f *sfntio.Font, m *metrics, upm uint16) error {
	if upm == m.upm {
		return nil
	}
	ratio := float64(upm) / float64(m.upm)
	var data []byte
	loca := make([]byte, (m.count+1)*4)
	offsets := make([]uint32, m.count+1)
	for i := 0; i < m.count; i++ {
		offsets[i] = uint32(len(data))
		binary.BigEndian.PutUint32(loca[4*i:], uint32(len(data)))
		raw := f.Tables["glyf"][m.offsets[i]:m.offsets[i+1]]
		scaled, err := sfntio.ScaleGlyphData(raw, ratio)
		if err != nil {
			return fmt.Errorf("scale glyph %d: %w", i, err)
		}
		data = append(data, scaled...)
		if len(data)%2 != 0 {
			data = append(data, 0)
		}
	}
	offsets[m.count] = uint32(len(data))
	binary.BigEndian.PutUint32(loca[4*m.count:], uint32(len(data)))
	if err := sfntio.RecalculateCompositeBounds(data, offsets); err != nil {
		return fmt.Errorf("scaled composite bounds: %w", err)
	}
	f.Tables["glyf"], f.Tables["loca"] = data, loca
	binary.BigEndian.PutUint16(f.Tables["head"][50:], 1)
	return scaleMetadata(f, upm)
}
func (p *Processor) transform(ctx context.Context, data []byte, o transformOptions) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := sfntio.Parse(data, 0)
	if err != nil {
		return nil, err
	}
	m, err := fontMetrics(f)
	if err != nil {
		return nil, err
	}
	if o.upm == 0 {
		o.upm = m.upm
	}
	if !m.ttf && o.ttf && o.upm != m.upm {
		quad, err := p.transform(ctx, data, transformOptions{ttf: true, upm: m.upm, simplify: o.simplify})
		if err != nil {
			return nil, err
		}
		o.simplify = false
		return p.transform(ctx, quad, o)
	}
	if m.ttf && o.upm != m.upm {
		if err = rawScale(f, m, o.upm); err != nil {
			return nil, err
		}
		m, err = fontMetrics(f)
		if err != nil {
			return nil, err
		}
		data, err = f.Bytes()
		if err != nil {
			return nil, err
		}
	}
	if m.ttf && o.ttf && o.radius == 0 && !o.simplify && !o.clean {
		rawGlyphs := make([][]byte, m.count)
		for id := range rawGlyphs {
			rawGlyphs[id] = f.Tables["glyf"][m.offsets[id]:m.offsets[id+1]]
		}
		profile, err := ttfMaximumProfile(f.Tables["maxp"], rawGlyphs)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(profile, f.Tables["maxp"]) {
			f.Tables["maxp"] = profile
			return f.Bytes()
		}
		return data, nil
	}
	face, err := p.HB.NewFace(data, 0)
	if err != nil {
		return nil, err
	}
	defer face.Close()
	ratio := float64(o.upm) / float64(m.upm)
	advances := append([]uint16(nil), m.advances...)
	bearings := append([]int16(nil), m.bearings...)
	if ratio != 1 {
		for i := range advances {
			a, b := scaledInt(int(advances[i]), ratio), scaledInt(int(bearings[i]), ratio)
			if a < 0 || a > 65535 || b < -32768 || b > 32767 {
				return nil, fmt.Errorf("scaled glyph metrics overflow")
			}
			advances[i], bearings[i] = uint16(a), int16(b)
		}
		if err = scaleMetadata(f, o.upm); err != nil {
			return nil, err
		}
	}
	glyphs := make([]sfntio.Glyph, m.count)
	strings := make([][]byte, m.count)
	bounds := make([][4]int16, m.count)
	nonempty := make([]bool, m.count)
	preserved := make([]bool, m.count)
	err = glyphJobs(ctx, m.count, p.Workers, func(id int) error {
		path, simple, err := simplePath(f, m, id)
		if err != nil {
			return err
		}
		if !simple {
			if m.ttf {
				path, err = sfntio.DecodeCompositeGlyph(f.Tables["glyf"], m.offsets, uint16(id))
			} else {
				path, err = face.Draw(uint32(id), 0)
			}
			if err != nil {
				return err
			}
		}
		if ratio != 1 {
			for i := range path {
				n := 0
				switch path[i].Verb {
				case geometry.Move, geometry.Line:
					n = 2
				case geometry.Quad:
					n = 4
				case geometry.Cubic:
					n = 6
				}
				for j := 0; j < n; j++ {
					path[i].Points[j] = float32(float64(path[i].Points[j]) * ratio)
				}
			}
		}
		before := path
		if o.radius > 0 || o.simplify {
			path, err = p.Geometry.Process(path, geometry.Options{Radius: o.radius, PixelGrid: o.pixel, Clockwise: m.ttf, Simplify: o.simplify})
			if err != nil {
				return err
			}
		}
		changed := o.radius > 0 || (o.simplify && !geometry.EqualContours(before, path))
		if !changed {
			path = before
		}
		if o.ttf {
			var g sfntio.Glyph
			kept := m.ttf && !changed
			if !m.ttf {
				g, err = quadratic.Glyph(path, float64(o.upm)/1000, true)
				if err != nil {
					return err
				}
			} else if kept {
				raw := f.Tables["glyf"][m.offsets[id]:m.offsets[id+1]]
				raw, err = sfntio.ScaleGlyphData(raw, 1)
				if err != nil {
					return err
				}
				g = storedGlyph(raw)
			} else {
				g, err = sfntio.EncodeGlyph(path)
				if err != nil {
					return err
				}
				if m.ttf && changed && g.Contours != 0 {
					bearings[id] = g.Bounds[0]
				}
			}
			if o.clean {
				encoded := path
				if int16(g.Contours) >= 0 {
					encoded, err = sfntio.DecodeGlyph(g.Data)
					if err != nil {
						return err
					}
					translate(encoded, float32(int(bearings[id])-int(g.Bounds[0])))
				}
				clean, err := outlinecleanup.Clean(encoded)
				if err != nil {
					return err
				}
				if !slices.Equal(clean, geometry.ClosedSegments(encoded)) {
					g, err = sfntio.EncodeGlyphExplicit(clean)
					if err != nil {
						return err
					}
					g, err = cleanEncoded(g)
					if err != nil {
						return err
					}
					kept = false
					if g.Contours != 0 {
						bearings[id] = g.Bounds[0]
					}
				}
			}
			glyphs[id] = g
			bounds[id] = g.Bounds
			nonempty[id] = g.Contours > 0
			preserved[id] = kept
			return nil
		} else {
			var cs []byte
			if o.fractional {
				cs, err = cffconv.EncodeFractional(path, advances[id])
			} else {
				cs, err = cffconv.Encode(path, advances[id], m.ttf)
			}
			if err != nil {
				return err
			}
			if o.clean {
				for attempt := 0; attempt < 8; attempt++ {
					encoded, err := cffconv.DecodeLiteral(cs)
					if err != nil {
						return err
					}
					clean, err := outlinecleanup.Clean(encoded)
					if err != nil {
						return err
					}
					if slices.Equal(clean, geometry.ClosedSegments(encoded)) {
						break
					}
					next, err := cffconv.Encode(clean, advances[id], false)
					if err != nil {
						return err
					}
					if bytes.Equal(cs, next) {
						break
					}
					cs = next
					if attempt == 7 {
						return fmt.Errorf("CFF outline did not stabilize after eight passes")
					}
				}
			}
			strings[id] = cs
			encoded, err := cffconv.DecodeLiteral(cs)
			if err != nil {
				return err
			}
			bounds[id], nonempty[id] = pathBounds(encoded)
		}
		if o.radius > 0 && len(before) > 0 {
			bearings[id] -= int16(o.radius)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if o.ttf {
		keepHints := false
		for _, kept := range preserved {
			keepHints = keepHints || kept
		}
		if err := setTTFGlyphs(f, glyphs, advances, bearings, keepHints); err != nil {
			return nil, err
		}
	} else {
		box := unionBounds(bounds, nonempty)
		cff, err := cffconv.Table("HD2Native", strings, box, o.upm)
		if err != nil {
			return nil, err
		}
		f.Tables["CFF "] = cff
		f.Version = 0x4f54544f
		for _, tag := range []string{"glyf", "loca", "cvt ", "prep", "fpgm", "gasp"} {
			delete(f.Tables, tag)
		}
		f.Tables["maxp"] = []byte{0, 0, 0x50, 0, byte(m.count >> 8), byte(m.count)}
		post := make([]byte, 32)
		copy(post, f.Tables["post"])
		binary.BigEndian.PutUint32(post, 0x30000)
		f.Tables["post"] = post
		setMetrics(f, advances, bearings, bounds, nonempty)
	}
	for _, tag := range []string{"DSIG", "LTSH", "VDMX", "hdmx"} {
		delete(f.Tables, tag)
	}
	return f.Bytes()
}

func storedGlyph(raw []byte) sfntio.Glyph {
	g := sfntio.Glyph{Data: raw}
	if len(raw) >= 10 {
		g.Contours = binary.BigEndian.Uint16(raw)
		for j := 0; j < 4; j++ {
			g.Bounds[j] = int16(binary.BigEndian.Uint16(raw[2+2*j:]))
		}
		if int16(g.Contours) > 0 {
			g.Points = binary.BigEndian.Uint16(raw[8+2*int(g.Contours):]) + 1
		}
	}
	return g
}
