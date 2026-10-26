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
	// The preview draws one game weight row: 400 when weights are not matched,
	// else the requested row, defaulting to the bold row shown before rows existed.
	target := 400
	if req.MatchWeights {
		target = 700
		if req.Weight > 0 {
			target = req.Weight
		}
	}
	axes, err := fontcore.VariableAxes(data, req.Font.FaceIndex)
	if err != nil {
		return mygoapp.PreviewResult{}, err
	}
	faceIndex, emphasis := req.Font.FaceIndex, 0
	// fallbacks are the other family faces, nearest weight first, that fill
	// characters the chosen face lacks, as they do in the build.
	var fallbacks []mygoapp.FamilyFace
	coordinates := maps.Clone(req.Font.Axes)
	nativeWeight := false
	for _, axis := range axes {
		if axis.Tag == "wght" && req.MatchWeights {
			if coordinates == nil {
				coordinates = make(map[string]float32)
			}
			value, ok := coordinates["wght"]
			if !ok {
				value = axis.Default
			}
			coordinates["wght"] = max(axis.Min, min(axis.Max, value+float32(target-400)))
			nativeWeight = true
		}
	}
	if req.MatchWeights && !nativeWeight {
		// Choose the face as the pipeline does, so the preview matches the build.
		style, err := inventory.DescribeStyle(data, req.Font.FaceIndex)
		if err != nil {
			return mygoapp.PreviewResult{}, err
		}
		weights := []uint16{style.Weight}
		if len(axes) == 0 {
			for _, face := range req.Font.Family {
				weights = append(weights, uint16(face.Style.Weight))
			}
		}
		var index int
		index, emphasis = inventory.PickFace(weights, req.Font.WeightFaces, target)
		if len(weights) == 1 {
			emphasis = 0
			if target > 400 {
				emphasis = target
			}
		}
		if len(weights) > 1 {
			members := append([]mygoapp.FamilyFace{{Path: req.Font.Path, FaceIndex: req.Font.FaceIndex, Style: mygoapp.FaceStyle{Weight: int(style.Weight)}}}, req.Font.Family...)
			for i, member := range members {
				if i != index {
					fallbacks = append(fallbacks, member)
				}
			}
			slices.SortStableFunc(fallbacks, func(x, y mygoapp.FamilyFace) int {
				return abs(x.Style.Weight-target) - abs(y.Style.Weight-target)
			})
		}
		if index > 0 {
			face := req.Font.Family[index-1]
			if data, err = readSource(ctx, face.Path); err != nil {
				return mygoapp.PreviewResult{}, err
			}
			if err := verifyStoredSource(b.userData, face.Path, data); err != nil {
				return mygoapp.PreviewResult{}, err
			}
			faceIndex, coordinates = face.FaceIndex, nil
		}
	}
	raw, err := sfntio.Parse(data, faceIndex)
	if err != nil {
		return mygoapp.PreviewResult{}, err
	}
	pixelGrid := false
	if head := raw.Tables["head"]; len(head) >= 20 {
		pixelGrid = binary.BigEndian.Uint16(head[18:20]) < 1000
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
	instanced, err := hb.Instance(data, uint32(faceIndex), coordinates)
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
	if emphasis > 0 {
		prepared, err = processor.Style(ctx, prepared, emphasis, pixelGrid, req.ProcessOutlines)
		if err != nil {
			return mygoapp.PreviewResult{}, err
		}
	}
	if len(fallbacks) > 0 {
		var parts [][]byte
		for _, face := range fallbacks {
			part, err := b.previewPart(ctx, hb, processor, face, characters, req.ProcessOutlines)
			if err != nil {
				return mygoapp.PreviewResult{}, err
			}
			if part != nil {
				parts = append(parts, part)
			}
		}
		if prepared, err = processor.Merge(ctx, prepared, parts); err != nil {
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

// previewPart prepares the sample characters of one family face for merging,
// or returns nil when the face draws none of them.
func (b *Backend) previewPart(ctx context.Context, hb *harfbuzz.Engine, processor *fontcore.Processor, face mygoapp.FamilyFace, characters []rune, outlines bool) ([]byte, error) {
	data, err := readSource(ctx, face.Path)
	if err != nil {
		return nil, err
	}
	if err := verifyStoredSource(b.userData, face.Path, data); err != nil {
		return nil, err
	}
	instanced, err := hb.Instance(data, uint32(face.FaceIndex), nil)
	if err != nil {
		return nil, err
	}
	metadata, err := inventory.InspectFace(instanced)
	if err != nil {
		return nil, err
	}
	var drawn []rune
	for _, cp := range characters {
		if _, ok := metadata.Chars[cp]; ok {
			drawn = append(drawn, cp)
		}
	}
	if len(drawn) == 0 {
		return nil, nil
	}
	subset, err := processor.Subset(ctx, instanced, drawn)
	if err != nil {
		return nil, err
	}
	return processor.Prepare(ctx, subset, 0, nil, outlines)
}

func abs(v int) int { return max(v, -v) }
