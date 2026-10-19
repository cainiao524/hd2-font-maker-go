package fontcore

import (
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"sync"
	"time"

	"hd2font/native/internal/cffconv"
	"hd2font/native/internal/sfntio"
)

type CFFReport struct {
	Glyphs       int     `json:"glyphs"`
	UnitsPerEm   uint16  `json:"units_per_em"`
	Workers      int     `json:"workers"`
	ParseSeconds float64 `json:"parse_seconds"`
	GlyphSeconds float64 `json:"glyph_seconds"`
	WriteSeconds float64 `json:"write_seconds"`
	TotalSeconds float64 `json:"total_seconds"`
	InputBytes   int     `json:"input_bytes"`
	OutputBytes  int     `json:"output_bytes"`
}

// ToCFF retains the source units and glyph IDs. Font-wide rescaling, mixed
// fallback merging, registration and variable instancing are separate work.
func ToCFF(ctx context.Context, data []byte, workers int) ([]byte, CFFReport, error) {
	start := time.Now()
	r := CFFReport{InputBytes: len(data)}
	f, err := sfntio.Parse(data, 0)
	if err != nil {
		return nil, r, err
	}
	if _, ok := f.Tables["glyf"]; !ok {
		return nil, r, fmt.Errorf("native converter requires static TrueType outlines")
	}
	if _, ok := f.Tables["fvar"]; ok {
		return nil, r, fmt.Errorf("instance variable font before conversion")
	}
	source, err := parseMetadata(f)
	if err != nil {
		return nil, r, err
	}
	r.Glyphs = int(source.NumGlyphs())
	r.UnitsPerEm = source.UnitsPerEm()
	if len(f.Tables["head"]) < 54 || len(f.Tables["post"]) < 32 {
		return nil, r, fmt.Errorf("incomplete font headers")
	}
	if workers <= 0 {
		workers = min(8, runtime.NumCPU())
	}
	workers = max(1, min(workers, r.Glyphs))
	r.Workers = workers
	r.ParseSeconds = time.Since(start).Seconds()
	glyphStart := time.Now()
	strings := make([][]byte, r.Glyphs)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	var once sync.Once
	var glyphErr error
	for w := 0; w < workers; w++ {
		wg.Go(func() {
			for id := range jobs {
				if ctx.Err() != nil {
					return
				}
				p, err := readGlyph(f, source, uint16(id))
				if err == nil {
					strings[id], err = cffconv.Encode(p, source.GlyphAdvance(uint16(id)), true)
				}
				if err != nil {
					once.Do(func() { glyphErr = fmt.Errorf("glyph %d: %w", id, err); cancel() })
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
	if err = ctx.Err(); err != nil {
		return nil, r, err
	}
	r.GlyphSeconds = time.Since(glyphStart).Seconds()
	writeStart := time.Now()
	var bounds [4]int16
	for i := range bounds {
		bounds[i] = int16(binary.BigEndian.Uint16(f.Tables["head"][36+2*i:]))
	}
	cff, err := cffconv.Table("HD2Native", strings, bounds, r.UnitsPerEm)
	if err != nil {
		return nil, r, err
	}
	f.Tables["CFF "] = cff
	f.Version = 0x4f54544f
	for _, tag := range []string{"glyf", "loca", "cvt ", "fpgm", "prep", "gasp", "LTSH", "hdmx", "VDMX", "DSIG"} {
		delete(f.Tables, tag)
	}
	maxp := make([]byte, 6)
	binary.BigEndian.PutUint32(maxp, 0x00005000)
	binary.BigEndian.PutUint16(maxp[4:], uint16(r.Glyphs))
	f.Tables["maxp"] = maxp
	post := append([]byte(nil), f.Tables["post"][:32]...)
	binary.BigEndian.PutUint32(post, 0x00030000)
	f.Tables["post"] = post
	output, err := f.Bytes()
	if err != nil {
		return nil, r, err
	}
	r.WriteSeconds = time.Since(writeStart).Seconds()
	r.TotalSeconds = time.Since(start).Seconds()
	r.OutputBytes = len(output)
	return output, r, nil
}
