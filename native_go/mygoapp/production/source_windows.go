//go:build windows && amd64

package production

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ot "github.com/go-text/typesetting/font/opentype"
	"hd2font/native/internal/fontcore"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/sfntio"
	"hd2font/native/mygoapp"
)

func readSource(ctx context.Context, path string) ([]byte, error) {
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
	return data, nil
}

// Inspection reads table metadata in Go, including CID CFF and CFF2 fonts;
// no native processing, conversion, or glyph truncation occurs during import.
func inspectData(ctx context.Context, data []byte, path string) (mygoapp.FontInfo, error) {
	loaders, err := ot.NewLoaders(bytes.NewReader(data))
	if err != nil {
		return mygoapp.FontInfo{}, err
	}
	info := mygoapp.FontInfo{Path: path}
	for index := range loaders {
		if err := ctx.Err(); err != nil {
			return mygoapp.FontInfo{}, err
		}
		f, err := sfntio.Parse(data, index)
		if err != nil {
			return mygoapp.FontInfo{}, err
		}
		standalone, err := f.Bytes()
		if err != nil {
			return mygoapp.FontInfo{}, err
		}
		metadata, err := inventory.InspectFace(standalone)
		if err != nil {
			return mygoapp.FontInfo{}, err
		}
		axes, err := fontcore.VariableAxes(data, index)
		if err != nil {
			return mygoapp.FontInfo{}, err
		}
		face := mygoapp.FontFace{Index: index, Name: metadata.Name}
		for _, axis := range axes {
			face.Axes = append(face.Axes, mygoapp.AxisInfo{Tag: axis.Tag, Name: axis.Tag, Min: axis.Min, Default: axis.Default, Max: axis.Max})
		}
		info.Faces = append(info.Faces, face)
	}
	return info, nil
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func storeFont(ctx context.Context, userData, path string, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	target := filepath.Join(userData, "Fonts", digest(data), filepath.Base(path))
	existing, err := os.ReadFile(target)
	if err == nil {
		if !bytes.Equal(data, existing) {
			return "", fmt.Errorf("stored source font changed: %s", target)
		}
		return target, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(target), "font-*.pending")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), target); err != nil {
		return "", err
	}
	return target, nil
}

func verifyStoredSource(userData, path string, data []byte) error {
	root, err := filepath.Abs(filepath.Join(userData, "Fonts"))
	if err != nil {
		return err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) == 2 && parts[0] != ".." && parts[0] != digest(data) {
		return errors.New("the imported font has changed; import it again")
	}
	return nil
}

// Resolve existing ancestors so a new ZIP path through a junction is checked
// against the real game root before the pipeline can create output files.
func resolvedPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(abs)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", err
		}
		suffix = append(suffix, filepath.Base(abs))
		abs = parent
	}
}

func outsideGame(game string, paths ...string) error {
	root, err := resolvedPath(game)
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Base(root), "data") {
		root = filepath.Dir(root)
	}
	for _, path := range paths {
		resolved, err := resolvedPath(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return errors.New("keep UserData and output ZIP outside the game folder")
		}
	}
	return nil
}
