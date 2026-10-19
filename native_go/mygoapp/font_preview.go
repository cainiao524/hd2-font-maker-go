package mygoapp

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

var ErrBackendUnavailable = errors.New("native generation backend is not connected")

// FontPreviewBackend provides real Go font reading and original-face previews
// for the standalone UI. Generation requires an injected production Backend.
type FontPreviewBackend struct{}

func fontCollection(ctx context.Context, path string) (*sfnt.Collection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ttf", ".otf", ".ttc", ".otc":
	default:
		return nil, errors.New("choose a TTF, OTF, TTC or OTC font")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return sfnt.ParseCollection(data)
}

func (FontPreviewBackend) InspectFont(ctx context.Context, path string) (FontInfo, error) {
	collection, err := fontCollection(ctx, path)
	if err != nil {
		return FontInfo{}, err
	}
	info := FontInfo{Path: path}
	axes, err := readFontAxes(path)
	if err != nil {
		return FontInfo{}, err
	}
	for index := 0; index < collection.NumFonts(); index++ {
		if err := ctx.Err(); err != nil {
			return FontInfo{}, err
		}
		f, err := collection.Font(index)
		if err != nil {
			return FontInfo{}, err
		}
		name, _ := f.Name(nil, sfnt.NameIDFull)
		if name == "" {
			name, _ = f.Name(nil, sfnt.NameIDFamily)
		}
		if name == "" {
			name = fmt.Sprintf("%s #%d", filepath.Base(path), index+1)
		}
		info.Faces = append(info.Faces, FontFace{Index: index, Name: name, Axes: axes[index]})
	}
	return info, nil
}

func (FontPreviewBackend) Preview(ctx context.Context, req PreviewRequest) (PreviewResult, error) {
	if len(req.Font.Axes) > 0 {
		return renderVariablePreview(ctx, req)
	}
	collection, err := fontCollection(ctx, req.Font.Path)
	if err != nil {
		return PreviewResult{}, err
	}
	f, err := collection.Font(req.Font.FaceIndex)
	if err != nil {
		return PreviewResult{}, err
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: 30, DPI: 96, Hinting: font.HintingFull})
	if err != nil {
		return PreviewResult{}, err
	}
	defer face.Close()
	w, h := max(320, req.Width), max(160, req.Height)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{246, 247, 249, 255}), image.Point{}, draw.Src)
	d := font.Drawer{Dst: img, Src: image.NewUniform(color.RGBA{28, 33, 39, 255}), Face: face}
	lineHeight := face.Metrics().Height.Ceil() + 14
	x, y := 24, 24+face.Metrics().Ascent.Ceil()
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
		idx, _ := f.GlyphIndex(nil, ch)
		if idx == 0 {
			missing++
		}
		advance, ok := face.GlyphAdvance(ch)
		if !ok {
			advance = fixed.I(24)
		}
		if x+advance.Ceil() > w-24 {
			x = 24
			y += lineHeight
		}
		if y+face.Metrics().Descent.Ceil() > h-16 {
			break
		}
		d.Dot = fixed.P(x, y)
		d.DrawString(string(ch))
		x += advance.Ceil()
	}
	return PreviewResult{Image: img, MissingCharacters: missing}, nil
}

func (FontPreviewBackend) Generate(context.Context, BuildRequest, func(Progress)) (BuildResult, error) {
	return BuildResult{}, ErrBackendUnavailable
}
