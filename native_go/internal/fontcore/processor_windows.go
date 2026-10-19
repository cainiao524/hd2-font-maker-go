//go:build windows && amd64

package fontcore

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"sync"

	"hd2font/native/internal/geometry"
	"hd2font/native/internal/harfbuzz"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/sfntio"
)

type memoEntry struct {
	done chan struct{}
	data []byte
	err  error
}
type Processor struct {
	Geometry *geometry.Engine
	HB       *harfbuzz.Engine
	Workers  int
	mu       sync.Mutex
	memo     map[string]*memoEntry
}

func NewProcessor(binDir string, workers int) (*Processor, error) {
	g, err := geometry.Open(binDir)
	if err != nil {
		return nil, err
	}
	hb, err := harfbuzz.Open(binDir)
	if err != nil {
		g.Close()
		return nil, err
	}
	if workers <= 0 {
		workers = min(12, runtime.NumCPU())
	}
	return &Processor{Geometry: g, HB: hb, Workers: workers, memo: map[string]*memoEntry{}}, nil
}
func (p *Processor) Close() error {
	e := p.HB.Close()
	g := p.Geometry.Close()
	p.memo = nil
	if e != nil {
		return e
	}
	return g
}
func (p *Processor) shared(ctx context.Context, key string, work func() ([]byte, error)) ([]byte, error) {
	p.mu.Lock()
	e := p.memo[key]
	if e != nil {
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-e.done:
			return e.data, e.err
		}
	}
	e = &memoEntry{done: make(chan struct{})}
	p.memo[key] = e
	p.mu.Unlock()
	e.data, e.err = work()
	p.mu.Lock()
	if e.err != nil {
		delete(p.memo, key)
	}
	close(e.done)
	p.mu.Unlock()
	return e.data, e.err
}
func glyphJobs(ctx context.Context, n, workers int, job func(int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	jobs := make(chan int)
	for w := 0; w < min(n, max(1, workers)); w++ {
		wg.Go(func() {
			for id := range jobs {
				if ctx.Err() != nil {
					return
				}
				if err := job(id); err != nil {
					once.Do(func() { first = fmt.Errorf("glyph %d: %w", id, err); cancel() })
					return
				}
			}
		})
	}
feed:
	for i := 0; i < n; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}
func canonicalFont(data []byte, index int) (*sfntio.Font, []byte, error) {
	f, err := sfntio.Parse(data, index)
	if err != nil {
		return nil, nil, err
	}
	raw, err := f.Bytes()
	if err != nil {
		return nil, nil, err
	}
	face, err := inventory.InspectFace(raw)
	if err != nil {
		return nil, nil, err
	}
	f.Tables["cmap"] = sfntio.UnicodeCmap(face.Cmap)
	delete(f.Tables, "DSIG")
	raw, err = f.Bytes()
	return f, raw, err
}

func (p *Processor) Prepare(ctx context.Context, data []byte, index uint32, axes map[string]float32, outlines bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, raw, err := canonicalFont(data, int(index))
	if err != nil {
		return nil, err
	}
	if f.Tables["fvar"] != nil || f.Tables["CFF2"] != nil {
		raw, err = p.HB.Instance(raw, 0, axes)
		if err != nil {
			return nil, err
		}
		f, raw, err = canonicalFont(raw, 0)
		if err != nil {
			return nil, err
		}
	} else if len(axes) > 0 {
		return nil, fmt.Errorf("static font has no variable axes")
	}
	m, err := fontMetrics(f)
	if err != nil {
		return nil, err
	}
	if outlines {
		raw, err = p.transform(ctx, raw, transformOptions{ttf: m.ttf, upm: m.upm, simplify: true, clean: m.upm >= 1000, fractional: !m.ttf})
		if err != nil {
			return nil, err
		}
	}
	if m.upm < 1000 {
		raw, err = p.transform(ctx, raw, transformOptions{ttf: m.ttf, upm: 1024, clean: outlines})
		if err != nil {
			return nil, err
		}
	}
	return raw, nil
}
func (p *Processor) Style(ctx context.Context, data []byte, weight int, pixelGrid, outlines bool) ([]byte, error) {
	if weight <= 400 {
		return data, ctx.Err()
	}
	f, err := sfntio.Parse(data, 0)
	if err != nil {
		return nil, err
	}
	m, err := fontMetrics(f)
	if err != nil {
		return nil, err
	}
	radius := math.RoundToEven(float64(m.upm) * math.Min(.0225, float64(weight-400)*.000075))
	if pixelGrid {
		radius = math.Max(1, math.RoundToEven(radius/4)) * 4
	}
	if radius == 0 {
		return data, nil
	}
	result, err := p.transform(ctx, data, transformOptions{ttf: m.ttf, upm: m.upm, radius: float32(radius), pixel: pixelGrid, clean: outlines})
	if err != nil {
		return nil, err
	}
	f, err = sfntio.Parse(result, 0)
	if err != nil {
		return nil, err
	}
	if len(f.Tables["OS/2"]) >= 6 {
		binary.BigEndian.PutUint16(f.Tables["OS/2"][4:], uint16(weight))
	}

	return f.Bytes()
}

// PrepareAtlasFallback follows atlas.py's original-font path. A donor keeps
// its own UPM: scaling a small donor to the custom font's grid would introduce
// a quantization step absent from 1.3's independent FreeType atlas sources.
func (p *Processor) PrepareAtlasFallback(ctx context.Context, data []byte, outlines bool) ([]byte, error) {
	if !outlines {
		return data, ctx.Err()
	}
	f, raw, err := canonicalFont(data, 0)
	if err != nil {
		return nil, err
	}
	m, err := fontMetrics(f)
	if err != nil {
		return nil, err
	}
	return p.transform(ctx, raw, transformOptions{ttf: m.ttf, upm: m.upm, simplify: true, clean: true, fractional: !m.ttf})
}

func (p *Processor) Conform(ctx context.Context, data []byte, targetTTF bool, targetUPM uint16, layout []byte, outlines bool) ([]byte, [3]float32, error) {
	var metrics [3]float32
	if outlines {
		key := "v13-normalize:" + inventory.Digest(data)
		var err error
		data, err = p.shared(ctx, key, func() ([]byte, error) {
			f, err := sfntio.Parse(data, 0)
			if err != nil {
				return nil, err
			}
			m, err := fontMetrics(f)
			if err != nil {
				return nil, err
			}
			return p.transform(ctx, data, transformOptions{ttf: m.ttf, upm: m.upm, simplify: true})
		})
		if err != nil {
			return nil, metrics, err
		}
	}
	raw, err := p.transform(ctx, data, transformOptions{ttf: targetTTF, upm: targetUPM, clean: outlines})
	if err != nil {
		return nil, metrics, err
	}
	f, err := sfntio.Parse(raw, 0)
	if err != nil {
		return nil, metrics, err
	}
	if len(layout) > 0 {
		base, err := sfntio.Parse(layout, 0)
		if err != nil {
			return nil, metrics, err
		}
		if len(base.Tables["head"]) < 20 {
			return nil, metrics, fmt.Errorf("layout font header")
		}
		r := float64(targetUPM) / float64(binary.BigEndian.Uint16(base.Tables["head"][18:]))
		for tag, offsets := range map[string][]int{"hhea": {4, 6, 8}, "OS/2": {68, 70, 72}} {
			for _, o := range offsets {
				if len(f.Tables[tag]) >= o+2 && len(base.Tables[tag]) >= o+2 {
					v := int(int16(binary.BigEndian.Uint16(base.Tables[tag][o:])))
					binary.BigEndian.PutUint16(f.Tables[tag][o:], uint16(scaledInt(v, r)))
				}
			}
		}
	}
	h := f.Tables["hhea"]
	asc, desc, gap := int16(binary.BigEndian.Uint16(h[4:])), int16(binary.BigEndian.Uint16(h[6:])), int16(binary.BigEndian.Uint16(h[8:]))
	metrics = [3]float32{float32(asc), float32(desc), float32(int(asc) - int(desc) + int(gap))}
	raw, err = f.Bytes()
	return raw, metrics, err
}

func simplePath(f *sfntio.Font, m *metrics, id int) (geometry.Path, bool, error) {
	if !m.ttf {
		return nil, false, nil
	}
	raw := f.Tables["glyf"][m.offsets[id]:m.offsets[id+1]]
	if len(raw) > 0 && len(raw) < 10 {
		return nil, true, fmt.Errorf("truncated glyph header")
	}
	if len(raw) > 0 && int16(binary.BigEndian.Uint16(raw)) < 0 {
		return nil, false, nil
	}
	path, err := sfntio.DecodeGlyph(raw)
	if err != nil {
		return nil, true, err
	}
	if len(raw) < 10 {
		return path, true, nil
	}
	dx := float32(int(m.bearings[id]) - int(int16(binary.BigEndian.Uint16(raw[2:]))))
	translate(path, dx)
	return path, true, nil
}
func translate(path geometry.Path, dx float32) {
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
			path[i].Points[2*j] += dx
		}
	}
}
