// Package atlas generates HD2 MTSDF atlases and patches their binary resources.
// Fonts must already be prepared by the caller; this package uses only the Go
// standard library and the native msdf-atlas-gen executable.
package atlas

import (
	"encoding/binary"
	"fmt"
	"math"
	"slices"
)

const (
	EmSize          = 32
	PixelRange      = 8
	GlyphHeaderSize = 88
)

// Bounds uses the generator's top-origin coordinates.
type Bounds struct {
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Right  float64 `json:"right"`
	Bottom float64 `json:"bottom"`
}

type Glyph struct {
	Unicode     rune    `json:"unicode"`
	Advance     float64 `json:"advance"`
	AtlasBounds *Bounds `json:"atlasBounds,omitempty"`
	PlaneBounds *Bounds `json:"planeBounds,omitempty"`
}

// Metrics returns x, y, width, height, offsetX, offsetY, and advance in pixels.
func Metrics(g Glyph) ([7]float64, error) {
	m := [7]float64{0, 0, 0, 0, 0, 0, g.Advance * EmSize}
	if g.AtlasBounds != nil {
		if g.PlaneBounds == nil {
			return m, fmt.Errorf("glyph U+%04X has atlas bounds without plane bounds", g.Unicode)
		}
		a, p := g.AtlasBounds, g.PlaneBounds
		// HD2 includes another distance range above the baseline in this offset.
		m = [7]float64{a.Left, a.Top, a.Right - a.Left, a.Bottom - a.Top,
			p.Left * EmSize, p.Top*EmSize - PixelRange, g.Advance * EmSize}
	}
	for _, v := range m {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > math.MaxFloat32 {
			return m, fmt.Errorf("invalid metrics for glyph U+%04X", g.Unicode)
		}
	}
	return m, nil
}

// GlyphKeys validates the packed key/metric/kerning layout, preserving key order.
// Keys are uint32 so engine sentinels survive without Unicode conversions.
func GlyphKeys(payload []byte) ([]uint32, error) {
	if len(payload) < GlyphHeaderSize {
		return nil, fmt.Errorf("glyph table is shorter than its 88-byte header")
	}
	count := uint64(binary.LittleEndian.Uint32(payload[68:]))
	offset := uint64(binary.LittleEndian.Uint32(payload[72:]))
	kerns := uint64(binary.LittleEndian.Uint32(payload[76:]))
	koffset := uint64(binary.LittleEndian.Uint32(payload[80:]))
	if offset != GlyphHeaderSize || koffset != GlyphHeaderSize+32*count || uint64(len(payload)) != koffset+12*kerns {
		return nil, fmt.Errorf("unsupported baked glyph table layout")
	}
	keys := make([]uint32, int(count))
	for i := range keys {
		keys[i] = binary.LittleEndian.Uint32(payload[GlyphHeaderSize+4*i:])
	}
	return keys, nil
}

// ReplaceGlyphTable merges extra keys, rescales the original line metrics, and
// writes sorted keys and 28-byte metrics with no kerning. Missing keys (including
// sentinel zero) use U+25A1 if present, otherwise '?', matching atlas.py.
func ReplaceGlyphTable(original []byte, width, height int, glyphs map[rune]Glyph, extraKeys ...uint32) ([]byte, error) {
	keys, err := GlyphKeys(original)
	if err != nil {
		return nil, err
	}
	if _, err := mipByteCount(width, height); err != nil {
		return nil, err
	}
	def, ok := glyphs[0x25a1]
	if !ok {
		def, ok = glyphs['?']
	}
	if !ok {
		return nil, fmt.Errorf("missing default '?' glyph")
	}
	defaultMetrics, err := Metrics(def)
	if err != nil {
		return nil, err
	}
	keys = append(keys, extraKeys...)
	slices.Sort(keys)
	keys = slices.Compact(keys)
	if uint64(len(keys)) > (math.MaxUint32-GlyphHeaderSize)/32 {
		return nil, fmt.Errorf("glyph table is too large")
	}
	out := make([]byte, GlyphHeaderSize+32*len(keys))
	copy(out, original[:GlyphHeaderSize])
	oldSize, oldHeight, oldBase := readFloat(original, 8), readFloat(original, 12), readFloat(original, 16)
	if oldSize <= 0 || math.IsNaN(oldSize) || math.IsInf(oldSize, 0) {
		return nil, fmt.Errorf("invalid original glyph size")
	}
	ratio := EmSize / oldSize
	for i, v := range []float64{EmSize, oldHeight * ratio, oldBase * ratio, 1 / float64(width), 1 / float64(height)} {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > math.MaxFloat32 {
			return nil, fmt.Errorf("invalid original line metrics")
		}
		putFloat(out, 8+4*i, v)
	}
	putFloat(out, 36, PixelRange)
	putMetrics(out, 40, defaultMetrics)
	for i, v := range []uint32{uint32(len(keys)), GlyphHeaderSize, 0, uint32(len(out)), 0} {
		binary.LittleEndian.PutUint32(out[68+4*i:], v)
	}
	for i, cp := range keys {
		binary.LittleEndian.PutUint32(out[GlyphHeaderSize+4*i:], cp)
		m := defaultMetrics
		if cp <= math.MaxInt32 {
			if g, found := glyphs[rune(cp)]; found {
				m, err = Metrics(g)
				if err != nil {
					return nil, err
				}
			}
		}
		putMetrics(out, GlyphHeaderSize+4*len(keys)+28*i, m)
	}
	return out, nil
}

func readFloat(b []byte, offset int) float64 {
	return float64(math.Float32frombits(binary.LittleEndian.Uint32(b[offset:])))
}
func putFloat(b []byte, offset int, value float64) {
	binary.LittleEndian.PutUint32(b[offset:], math.Float32bits(float32(value)))
}
func putMetrics(b []byte, offset int, values [7]float64) {
	for i, v := range values {
		putFloat(b, offset+4*i, v)
	}
}
