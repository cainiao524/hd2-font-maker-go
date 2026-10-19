//go:build windows && amd64

package production

import (
	"context"
	"encoding/binary"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"hd2font/native/internal/fontcore"
	"hd2font/native/internal/harfbuzz"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/sfntio"
	"hd2font/native/mygoapp"
)

func (b *Backend) preview(ctx context.Context, data []byte, req mygoapp.PreviewRequest) (mygoapp.PreviewResult, error) {
	if err := ctx.Err(); err != nil {
		return mygoapp.PreviewResult{}, err
	}
	raw, err := sfntio.Parse(data, req.Font.FaceIndex)
	if err != nil {
		return mygoapp.PreviewResult{}, err
	}
	pixelGrid := false
	if head := raw.Tables["head"]; len(head) >= 20 {
		pixelGrid = binary.BigEndian.Uint16(head[18:20]) < 1000
	}
	axes, err := fontcore.VariableAxes(data, req.Font.FaceIndex)
	if err != nil {
		return mygoapp.PreviewResult{}, err
	}
	coordinates := maps.Clone(req.Font.Axes)
	nativeWeight := false
	if req.MatchWeights {
		for _, axis := range axes {
			if axis.Tag == "wght" {
				if coordinates == nil {
					coordinates = make(map[string]float32)
				}
				value, ok := coordinates["wght"]
				if !ok {
					value = axis.Default
				}
				coordinates["wght"] = max(axis.Min, min(axis.Max, value+300))
				nativeWeight = true
			}
		}
	}
	// Bound only the visual sample; Build receives the untouched full source.
	text := []rune(req.Text)
	if len(text) > 512 {
		text = text[:512]
	}
	req.Text = string(text)
	characters := []rune{'?'}
	for _, cp := range text {
		if cp != '\n' && cp != '\r' && cp != '\t' {
			characters = append(characters, cp)
		}
	}
	slices.Sort(characters)
	characters = slices.Compact(characters)
	hb, err := harfbuzz.Open(b.binDir)
	if err != nil {
		return mygoapp.PreviewResult{}, err
	}
	defer hb.Close()
	instanced, err := hb.Instance(data, uint32(req.Font.FaceIndex), coordinates)
	if err != nil {
		return mygoapp.PreviewResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return mygoapp.PreviewResult{}, err
	}
	metadata, err := inventory.InspectFace(instanced)
	if err != nil {
		return mygoapp.PreviewResult{}, err
	}
	matched := false
	for _, cp := range characters {
		if _, ok := metadata.Chars[cp]; ok {
			matched = true
			break
		}
	}
	// Keep a valid cmap when every sample character is missing. The retained
	// character is not added to the displayed text; missing-glyph reporting stays.
	if !matched {
		characters = append(characters, metadata.Codepoints()[0])
	}
	processor, err := fontcore.NewProcessor(b.binDir, b.workers)
	if err != nil {
		return mygoapp.PreviewResult{}, err
	}
	defer processor.Close()
	subset, err := processor.Subset(ctx, instanced, characters)
	if err != nil {
		return mygoapp.PreviewResult{}, err
	}
	prepared, err := processor.Prepare(ctx, subset, 0, nil, req.ProcessOutlines)
	if err != nil {
		return mygoapp.PreviewResult{}, err
	}
	if req.MatchWeights && !nativeWeight {
		prepared, err = processor.Style(ctx, prepared, 700, pixelGrid, req.ProcessOutlines)
		if err != nil {
			return mygoapp.PreviewResult{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return mygoapp.PreviewResult{}, err
	}
	return b.renderPreviewData(ctx, prepared, req)
}

func (b *Backend) renderPreviewData(ctx context.Context, prepared []byte, req mygoapp.PreviewRequest) (mygoapp.PreviewResult, error) {
	workRoot := filepath.Join(b.userData, "Work")
	if err := os.MkdirAll(workRoot, 0755); err != nil {
		return mygoapp.PreviewResult{}, err
	}
	ext := ".ttf"
	if len(prepared) >= 4 && string(prepared[:4]) == "OTTO" {
		ext = ".otf"
	}
	f, err := os.CreateTemp(workRoot, "preview-*"+ext)
	if err != nil {
		return mygoapp.PreviewResult{}, err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(prepared)
	closeErr := f.Close()
	if err != nil {
		return mygoapp.PreviewResult{}, err
	}
	if closeErr != nil {
		return mygoapp.PreviewResult{}, closeErr
	}
	req.Font = mygoapp.FontSelection{Path: f.Name(), FaceIndex: 0}
	return (mygoapp.FontPreviewBackend{}).Preview(ctx, req)
}
