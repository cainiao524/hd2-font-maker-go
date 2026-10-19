package mygoapp

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"

	textfont "github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"golang.org/x/image/vector"
)

// go-text applies fvar/avar/gvar/CFF2 coordinates and resolves compound glyphs;
// x/image rasterizes its resulting outlines at the requested preview size.
func renderVariablePreview(ctx context.Context, req PreviewRequest) (PreviewResult, error) {
	if err := ctx.Err(); err != nil {
		return PreviewResult{}, err
	}
	f, err := os.Open(req.Font.Path)
	if err != nil {
		return PreviewResult{}, err
	}
	defer f.Close()
	faces, err := textfont.ParseTTC(f)
	if err != nil {
		return PreviewResult{}, err
	}
	if req.Font.FaceIndex < 0 || req.Font.FaceIndex >= len(faces) {
		return PreviewResult{}, fmt.Errorf("invalid face index %d", req.Font.FaceIndex)
	}
	face := faces[req.Font.FaceIndex]
	variations := make([]textfont.Variation, 0, len(req.Font.Axes))
	for tag, value := range req.Font.Axes {
		if len(tag) != 4 {
			return PreviewResult{}, fmt.Errorf("invalid axis tag %q", tag)
		}
		variations = append(variations, textfont.Variation{Tag: ot.MustNewTag(tag), Value: value})
	}
	face.SetVariations(variations)
	w, h := max(320, req.Width), max(160, req.Height)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{246, 247, 249, 255}), image.Point{}, draw.Src)
	scale := float32(40) / float32(face.Upem())
	extents, _ := face.FontHExtents()
	lineHeight := (extents.Ascender-extents.Descender+extents.LineGap)*scale + 14
	x, y := float32(24), 24+extents.Ascender*scale
	ink := image.NewUniform(color.RGBA{28, 33, 39, 255})
	raster := vector.NewRasterizer(w, h)
	missing := 0
	for _, ch := range req.Text {
		if err := ctx.Err(); err != nil {
			return PreviewResult{}, err
		}
		if ch == '\r' {
			continue
		}
		if ch == '\n' {
			x = 24
			y += lineHeight
			continue
		}
		glyph, ok := face.NominalGlyph(ch)
		if !ok {
			missing++
		}
		advance := face.HorizontalAdvance(glyph) * scale
		if x+advance > float32(w-24) {
			x = 24
			y += lineHeight
		}
		if y-extents.Descender*scale > float32(h-16) {
			break
		}
		outline, ok := face.GlyphDataOutline(glyph)
		if ok {
			raster.Reset(w, h)
			open := false
			for _, segment := range outline.Segments {
				p := segment.Args
				for i := range p {
					p[i].X = x + p[i].X*scale
					p[i].Y = y - p[i].Y*scale
				}
				switch segment.Op {
				case ot.SegmentOpMoveTo:
					if open {
						raster.ClosePath()
					}
					raster.MoveTo(p[0].X, p[0].Y)
					open = true
				case ot.SegmentOpLineTo:
					raster.LineTo(p[0].X, p[0].Y)
				case ot.SegmentOpQuadTo:
					raster.QuadTo(p[0].X, p[0].Y, p[1].X, p[1].Y)
				case ot.SegmentOpCubeTo:
					raster.CubeTo(p[0].X, p[0].Y, p[1].X, p[1].Y, p[2].X, p[2].Y)
				}
			}
			if open {
				raster.ClosePath()
			}
			raster.Draw(img, img.Bounds(), ink, image.Point{})
		}
		x += advance
	}
	return PreviewResult{Image: img, MissingCharacters: missing}, nil
}
