//go:build windows && amd64

package production

import (
	"context"
	"fmt"

	"hd2font/native/internal/inventory"
	"hd2font/native/mygoapp"
)

func faceStyle(s inventory.Style) mygoapp.FaceStyle {
	return mygoapp.FaceStyle{Family: s.Family, Subfamily: s.Subfamily, Weight: int(s.Weight), Width: int(s.Width), Italic: s.Italic}
}

// uniqueFamily also handles saved configurations made before import deduplication.
// Read actual content rather than trusting filenames or stored style labels.
func (b *Backend) uniqueFamily(ctx context.Context, data []byte, selected mygoapp.FontSelection) (mygoapp.FontSelection, error) {
	keys := []string{fmt.Sprintf("%s#%d", digest(data), selected.FaceIndex)}
	for _, face := range selected.Family {
		raw, err := readSource(ctx, face.Path)
		if err != nil {
			return selected, err
		}
		if err := verifyStoredSource(b.userData, face.Path, raw); err != nil {
			return selected, err
		}
		keys = append(keys, fmt.Sprintf("%s#%d", digest(raw), face.FaceIndex))
	}
	keep, pins := inventory.UniqueFaces(keys, selected.WeightFaces)
	family := make([]mygoapp.FamilyFace, 0, len(keep)-1)
	for _, index := range keep[1:] {
		family = append(family, selected.Family[index-1])
	}
	selected.Family, selected.WeightFaces = family, pins
	return selected, nil
}
