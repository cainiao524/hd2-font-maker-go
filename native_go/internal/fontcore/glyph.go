package fontcore

import (
	"encoding/binary"
	"fmt"

	"github.com/tdewolff/font"
	"hd2font/native/internal/geometry"
	"hd2font/native/internal/sfntio"
)

func parseMetadata(f *sfntio.Font) (*font.SFNT, error) {
	// Some authored fonts omit the final alignment bytes. Rebuilding the
	// container preserves every table while satisfying the parser's padding.
	aligned, err := f.Bytes()
	if err != nil {
		return nil, err
	}
	return font.ParseSFNT(aligned, 0)
}

func readGlyph(f *sfntio.Font, source *font.SFNT, id uint16) (geometry.Path, error) {
	start, ok := source.Loca.Get(id)
	if !ok {
		return nil, fmt.Errorf("missing glyph offset")
	}
	end, ok := source.Loca.Get(id + 1)
	if !ok || end < start || uint64(end) > uint64(len(f.Tables["glyf"])) {
		return nil, fmt.Errorf("invalid glyph range")
	}
	// The simple decoder preserves half-grid points without upstream int16
	// averaging or composite-transform rounding. Composite support is explicit
	// future work, not a silently lower-quality fallback through another parser.
	raw := f.Tables["glyf"][start:end]
	p, err := sfntio.DecodeGlyph(raw)
	if err != nil || len(raw) < 10 {
		return p, err
	}
	// Match fontTools' top-level GlyphSet drawing, including authored metrics
	// whose side bearing differs from the stored contour bounding box.
	dx := float32(int32(source.Hmtx.LeftSideBearing(id)) - int32(int16(binary.BigEndian.Uint16(raw[2:]))))
	for i := range p {
		count := 0
		switch p[i].Verb {
		case geometry.Move, geometry.Line:
			count = 1
		case geometry.Quad:
			count = 2
		case geometry.Cubic:
			count = 3
		}
		for j := 0; j < count; j++ {
			p[i].Points[2*j] += dx
		}
	}
	return p, nil
}
