//go:build windows && amd64

package production

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	ot "github.com/go-text/typesetting/font/opentype"
	"hd2font/native/internal/inventory"
	"hd2font/native/mygoapp"
)

// Sibling scanning stays cheap and bounded: a folder holding a whole font
// library is read only up to these limits.
const (
	maxSiblingFiles = 64
	maxSiblingBytes = 256 << 20
)

func faceStyle(s inventory.Style) mygoapp.FaceStyle {
	return mygoapp.FaceStyle{Family: s.Family, Subfamily: s.Subfamily, Weight: int(s.Weight), Width: int(s.Width), Italic: s.Italic}
}

func inventoryStyle(s mygoapp.FaceStyle) inventory.Style {
	return inventory.Style{Family: s.Family, Subfamily: s.Subfamily, Weight: uint16(s.Weight), Width: uint16(s.Width), Italic: s.Italic}
}

// familySiblings returns the faces of other font files beside path whose
// family matches one of faces, after storing each such file in UserData the
// way the selected font is stored.
func familySiblings(ctx context.Context, userData, path string, faces []mygoapp.FontFace) ([]mygoapp.FamilyFace, error) {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return nil, nil // a folder that cannot be listed simply has no siblings
	}
	var out []mygoapp.FamilyFace
	scanned := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if entry.IsDir() || strings.EqualFold(name, filepath.Base(path)) || !slices.Contains([]string{".ttf", ".otf", ".ttc", ".otc"}, strings.ToLower(filepath.Ext(name))) {
			continue
		}
		if scanned++; scanned > maxSiblingFiles {
			break
		}
		if info, err := entry.Info(); err != nil || info.Size() > maxSiblingBytes {
			continue
		}
		sibling := filepath.Join(filepath.Dir(path), name)
		data, err := readSource(ctx, sibling)
		if err != nil {
			continue
		}
		matched := siblingFaces(data, faces)
		if len(matched) == 0 {
			continue
		}
		stored, err := storeFont(ctx, userData, sibling, data)
		if err != nil {
			return nil, err
		}
		for i := range matched {
			matched[i].Path = stored
		}
		out = append(out, matched...)
	}
	return out, nil
}

// siblingFaces lists the faces of data that share a family with any of faces.
func siblingFaces(data []byte, faces []mygoapp.FontFace) []mygoapp.FamilyFace {
	loaders, err := ot.NewLoaders(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	var out []mygoapp.FamilyFace
	for index := range loaders {
		style, err := inventory.DescribeStyle(data, index)
		if err != nil {
			continue
		}
		for _, face := range faces {
			if inventory.SameFamily(inventoryStyle(face.Style), style) {
				name := strings.TrimSpace(style.Family + " " + style.Subfamily)
				out = append(out, mygoapp.FamilyFace{FaceIndex: index, Name: name, Style: faceStyle(style)})
				break
			}
		}
	}
	return out
}
