//go:build windows && amd64

package production

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"hd2font/native/mygoapp"
)

func (b *Backend) GameFonts(ctx context.Context, folder string) ([]mygoapp.GameFont, error) {
	ctx, done, err := b.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if strings.TrimSpace(folder) == "" {
		return nil, errors.New("choose the game folder first")
	}
	folder, err = filepath.Abs(folder)
	if err != nil {
		return nil, err
	}
	inv, err := b.deps.scan(ctx, folder, func(string) {})
	if err != nil {
		return nil, err
	}
	fonts := make(map[string][]byte)
	var list []mygoapp.GameFont
	for _, face := range inv.OrderedFaces() {
		if face.Group != "latin" && face.Name != "Steelworks for HELLDIVERS 2" {
			continue
		}
		fonts[face.Digest] = bytes.Clone(face.Data)
		list = append(list, mygoapp.GameFont{ID: face.Digest, Name: face.Name})
	}
	if len(list) == 0 {
		return nil, errors.New("no original English fonts found in the game")
	}
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].Name == "Steelworks for HELLDIVERS 2" && list[j].Name != "Steelworks for HELLDIVERS 2"
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.previewGameFolder, b.previewGameFonts = folder, fonts
	b.mu.Unlock()
	return list, nil
}

func (b *Backend) originalGamePreview(ctx context.Context, req mygoapp.PreviewRequest) (mygoapp.PreviewResult, error) {
	folder, err := filepath.Abs(req.GameFolder)
	if err != nil {
		return mygoapp.PreviewResult{}, err
	}
	b.mu.Lock()
	data := b.previewGameFonts[req.GameFontID]
	validFolder := strings.EqualFold(folder, b.previewGameFolder)
	b.mu.Unlock()
	if !validFolder || len(data) == 0 {
		return mygoapp.PreviewResult{}, errors.New("reload the original game fonts for this folder")
	}
	// Originals are shown as authored, without synthetic weights or outline cleanup.
	return b.renderPreviewData(ctx, data, req)
}
