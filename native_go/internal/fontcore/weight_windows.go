//go:build windows && amd64

package fontcore

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/tdewolff/font"
	"hd2font/native/internal/geometry"
	"hd2font/native/internal/outlinecleanup"
	"hd2font/native/internal/sfntio"
)

type WeightOptions struct {
	Weight          int
	PixelGrid       bool
	Workers         int
	ProcessOutlines bool
}

type WeightReport struct {
	ProcessOutlines bool    `json:"process_outlines"`
	Glyphs          int     `json:"glyphs"`
	Weight          int     `json:"weight"`
	Radius          float64 `json:"radius"`
	Workers         int     `json:"workers"`
	ParseSeconds    float64 `json:"parse_seconds"`
	GlyphSeconds    float64 `json:"glyph_seconds"`
	WriteSeconds    float64 `json:"write_seconds"`
	TotalSeconds    float64 `json:"total_seconds"`
	InputBytes      int     `json:"input_bytes"`
	OutputBytes     int     `json:"output_bytes"`
}

// Weight changes outlines in-place by glyph ID. Layout, cmap and names retain
// their original bytes; this is deliberately not a substitute for font merge.
func Weight(ctx context.Context, engine *geometry.Engine, data []byte, opts WeightOptions) ([]byte, WeightReport, error) {
	start := time.Now()
	r := WeightReport{Weight: opts.Weight, InputBytes: len(data), ProcessOutlines: opts.ProcessOutlines}
	tables, err := sfntio.Parse(data, 0)
	if err != nil {
		return nil, r, err
	}
	if opts.Weight < 1 || opts.Weight > 1000 {
		return nil, r, fmt.Errorf("weight must be 1..1000")
	}
	if _, variable := tables.Tables["fvar"]; variable {
		return nil, r, fmt.Errorf("variable font requires native static instancing before synthesis")
	}
	if _, ok := tables.Tables["glyf"]; !ok {
		return nil, r, fmt.Errorf("native weight prototype currently requires static TrueType outlines")
	}
	source, err := parseMetadata(tables)
	if err != nil {
		return nil, r, err
	}
	r.Glyphs = int(source.NumGlyphs())
	radius := math.RoundToEven(float64(source.UnitsPerEm()) * math.Min(.0225, float64(opts.Weight-400)*.000075))
	if opts.Weight <= 400 || radius <= 0 {
		radius = 0
		if !opts.ProcessOutlines {
			r.OutputBytes = len(data)
			r.TotalSeconds = time.Since(start).Seconds()
			return append([]byte(nil), data...), r, nil
		}
	}
	if opts.PixelGrid && radius > 0 {
		radius = math.Max(1, math.RoundToEven(radius/4)) * 4
	}
	r.Radius = radius
	workers := opts.Workers
	if workers <= 0 {
		workers = min(12, runtime.NumCPU())
	}
	workers = max(1, min(workers, r.Glyphs))
	r.Workers = workers
	if len(tables.Tables["head"]) < 54 || len(tables.Tables["maxp"]) < 32 || len(tables.Tables["hhea"]) < 36 || len(tables.Tables["OS/2"]) < 6 {
		return nil, r, fmt.Errorf("truncated font metrics")
	}
	r.ParseSeconds = time.Since(start).Seconds()
	glyphStart := time.Now()
	results := make([]sfntio.Glyph, r.Glyphs)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	var once sync.Once
	var glyphErr error
	fail := func(err error) { once.Do(func() { glyphErr = err; cancel() }) }
	for worker := 0; worker < workers; worker++ {
		wg.Go(func() {
			for id := range jobs {
				if ctx.Err() != nil {
					return
				}
				p, err := readGlyph(tables, source, uint16(id))
				if err != nil {
					fail(fmt.Errorf("glyph %d: %w", id, err))
					return
				}
				out, err := engine.Process(p, geometry.Options{Radius: float32(radius), PixelGrid: opts.PixelGrid, Clockwise: true})
				if err == nil {
					results[id], err = sfntio.EncodeGlyph(out)
				}
				if err == nil && opts.ProcessOutlines {
					results[id], err = cleanEncoded(results[id])
				}
				if err != nil {
					fail(fmt.Errorf("glyph %d: %w", id, err))
					return
				}
			}
		})
	}
feed:
	for i := 0; i < r.Glyphs; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if glyphErr != nil {
		return nil, r, glyphErr
	}
	if err := ctx.Err(); err != nil {
		return nil, r, err
	}
	r.GlyphSeconds = time.Since(glyphStart).Seconds()
	writeStart := time.Now()
	weight := opts.Weight
	if radius == 0 {
		weight = int(binary.BigEndian.Uint16(tables.Tables["OS/2"][4:]))
	}
	if err := replaceGlyphs(tables, source, results, weight); err != nil {
		return nil, r, err
	}
	output, err := tables.Bytes()
	if err != nil {
		return nil, r, err
	}
	r.WriteSeconds = time.Since(writeStart).Seconds()
	r.OutputBytes = len(output)
	r.TotalSeconds = time.Since(start).Seconds()
	return output, r, nil
}

func cleanEncoded(g sfntio.Glyph) (sfntio.Glyph, error) {
	p, err := sfntio.DecodeGlyph(g.Data)
	if err != nil {
		return g, err
	}
	clean, err := outlinecleanup.Clean(p)
	if err != nil {
		return g, err
	}
	if slices.Equal(clean, geometry.ClosedSegments(p)) {
		return g, nil
	}
	for attempt := 0; attempt < 8; attempt++ {
		g, err = sfntio.EncodeGlyphExplicit(clean)
		if err != nil {
			return g, err
		}
		p, err = sfntio.DecodeGlyph(g.Data)
		if err != nil {
			return g, err
		}
		clean, err = outlinecleanup.Clean(p)
		if err != nil {
			return g, err
		}
		if slices.Equal(clean, geometry.ClosedSegments(p)) {
			return g, nil
		}
	}
	return g, fmt.Errorf("quantized outline did not stabilize after eight passes")
}

func replaceGlyphs(f *sfntio.Font, source *font.SFNT, glyphs []sfntio.Glyph, weight int) error {
	be := binary.BigEndian
	var data []byte
	loca := make([]byte, 4*(len(glyphs)+1))
	metrics := make([]byte, 4*len(glyphs))
	var bounds [4]int16
	hasBounds := false
	maxPoints, maxContours := uint16(0), uint16(0)
	minLSB, minRSB, maxExtent := 32767, 32767, -32768
	nmetrics := int(be.Uint16(f.Tables["hhea"][34:]))
	oldMetrics := f.Tables["hmtx"]
	if nmetrics < 1 || nmetrics > len(glyphs) || len(oldMetrics) < 4*nmetrics+2*(len(glyphs)-nmetrics) {
		return fmt.Errorf("invalid hmtx metrics")
	}
	for i, g := range glyphs {
		be.PutUint32(loca[4*i:], uint32(len(data)))
		data = append(data, g.Data...)
		for len(data)%4 != 0 {
			data = append(data, 0)
		}
		advance := source.GlyphAdvance(uint16(i))
		bearingOffset := 4*i + 2
		if i >= nmetrics {
			bearingOffset = 4*nmetrics + 2*(i-nmetrics)
		}
		bearing := int16(be.Uint16(oldMetrics[bearingOffset:]))
		if g.Contours > 0 {
			bearing = g.Bounds[0]
			if !hasBounds {
				bounds = g.Bounds
				hasBounds = true
			} else {
				bounds[0] = min(bounds[0], g.Bounds[0])
				bounds[1] = min(bounds[1], g.Bounds[1])
				bounds[2] = max(bounds[2], g.Bounds[2])
				bounds[3] = max(bounds[3], g.Bounds[3])
			}
			minLSB = min(minLSB, int(bearing))
			minRSB = min(minRSB, int(advance)-int(bearing)-int(g.Bounds[2])+int(g.Bounds[0]))
			maxExtent = max(maxExtent, int(bearing)+int(g.Bounds[2])-int(g.Bounds[0]))
		}
		be.PutUint16(metrics[4*i:], advance)
		be.PutUint16(metrics[4*i+2:], uint16(bearing))
		maxPoints = max(maxPoints, g.Points)
		maxContours = max(maxContours, g.Contours)
	}
	be.PutUint32(loca[4*len(glyphs):], uint32(len(data)))
	f.Tables["glyf"] = data
	f.Tables["loca"] = loca
	f.Tables["hmtx"] = metrics
	head := f.Tables["head"]
	be.PutUint16(head[50:], 1)
	for i, v := range bounds {
		be.PutUint16(head[36+i*2:], uint16(v))
	}
	maxp := f.Tables["maxp"]
	clear(maxp[6:32])
	be.PutUint16(maxp[6:], maxPoints)
	be.PutUint16(maxp[8:], maxContours)
	be.PutUint16(maxp[14:], 1)
	hhea := f.Tables["hhea"]
	be.PutUint16(hhea[34:], uint16(len(glyphs)))
	if hasBounds {
		be.PutUint16(hhea[12:], uint16(minLSB))
		be.PutUint16(hhea[14:], uint16(minRSB))
		be.PutUint16(hhea[16:], uint16(maxExtent))
	}
	be.PutUint16(f.Tables["OS/2"][4:], uint16(weight))
	for _, tag := range []string{"fpgm", "prep", "cvt ", "LTSH", "hdmx", "VDMX", "DSIG"} {
		delete(f.Tables, tag)
	}
	return nil
}
