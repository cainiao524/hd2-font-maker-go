//go:build windows && amd64

package production

import (
	"context"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
	"hd2font/native/internal/inventory"
	"hd2font/native/mygoapp"
)

func TestGameFontsReadAndOriginalPreview(t *testing.T) {
	b := testBackend(t)
	face, err := inventory.InspectFace(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face.Group = "latin"
	inv := &inventory.Inventory{Faces: map[string]*inventory.Face{face.Digest: face}, FaceOrder: []string{face.Digest}}
	scans := 0
	b.deps.scan = func(ctx context.Context, path string, progress func(string)) (*inventory.Inventory, error) {
		scans++
		return inv, ctx.Err()
	}
	folder := filepath.Join(t.TempDir(), "game")
	fonts, err := b.GameFonts(context.Background(), folder)
	if err != nil || len(fonts) != 1 {
		t.Fatalf("fonts: %v %v", fonts, err)
	}
	req := mygoapp.PreviewRequest{GameFolder: folder, GameFontID: fonts[0].ID, Text: "HELLDIVERS 2\nFOR SUPER EARTH! 0123456789", Width: 1000, Height: 480, MatchWeights: true, ProcessOutlines: true}
	original, err := b.Preview(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.MatchWeights, req.ProcessOutlines = false, false
	plain, err := b.Preview(context.Background(), req)
	if err != nil || !reflect.DeepEqual(original.Image, plain.Image) {
		t.Fatal("original game preset was processed")
	}
	if scans != 1 {
		t.Fatal("editing preview rescanned the installation")
	}
	if _, err := os.Stat(filepath.Join(b.userData, "Fonts")); !os.IsNotExist(err) {
		t.Fatal("original game font imported as a user font")
	}
	files, err := os.ReadDir(filepath.Join(b.userData, "Work"))
	if err != nil || len(files) != 0 {
		t.Fatal("original font temporary file leaked")
	}
	req.GameFolder += "-changed"
	if _, err := b.Preview(context.Background(), req); err == nil {
		t.Fatal("stale game font accepted for another folder")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.GameFonts(ctx, folder); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestLocalOriginalGameFonts(t *testing.T) {
	folder := os.Getenv("HD2_LOCAL_GAME")
	if folder == "" {
		t.Skip("set HD2_LOCAL_GAME to inspect the local game read-only")
	}
	b := testBackend(t)
	fonts, err := b.GameFonts(context.Background(), folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(fonts) < 2 || fonts[0].Name != "Steelworks for HELLDIVERS 2" {
		t.Fatalf("missing original preset: %+v", fonts)
	}
	for i, face := range fonts {
		result, err := b.Preview(context.Background(), mygoapp.PreviewRequest{GameFolder: folder, GameFontID: face.ID, Text: "HELLDIVERS 2\nFOR SUPER EARTH! 0123456789", Width: 1000, Height: 480})
		if err != nil {
			t.Fatalf("%s: %v", face.Name, err)
		}
		ink := 0
		for y := 0; y < 480; y++ {
			for x := 0; x < 1000; x++ {
				r, _, _, _ := result.Image.At(x, y).RGBA()
				if r < 32768 {
					ink++
				}
			}
		}
		if ink < 100 {
			t.Fatalf("%s blank preview", face.Name)
		}
		t.Logf("%s: %d ink pixels, missing=%d", face.Name, ink, result.MissingCharacters)
		if i == 0 && os.Getenv("HD2_GAME_PREVIEW_PNG") != "" {
			path := os.Getenv("HD2_GAME_PREVIEW_PNG")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(f, result.Image)
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				t.Fatalf("png: %v %v", err, closeErr)
			}
		}
	}
}
