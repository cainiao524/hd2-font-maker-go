package atlas

import (
	"fmt"
	"math"
)

// FontMetrics contains only the data needed to verify an atlas association.
// The caller obtains these values from the original font's cmap/head/hmtx.
type FontMetrics struct {
	Name       string
	UnitsPerEm uint16
	Advances   map[rune]uint16
}

type Binding struct {
	FontName  string
	TextureID uint64
}

var bindings = map[uint64]Binding{
	0xe01b1c60bb279a7c: {"M XiangHe Hei SC Pro Bold", 0x1a87fa21dafbd90e},
	0xfbb35bee675f2295: {"M Ying Hei PRC W5", 0xf14daed0a7d38271},
	0x3da89f2c15b53d65: {"M XiangHe Hei TC Bold", 0x86667f650dca00f9},
	0x7c63fea1f706eec4: {"MJNgaiHK-Medium", 0x3454c1d97f395663},
	0xb18e66ab16af6cd4: {"MOT-Folk Pro B", 0x5e4f7e7101016c99},
	0xd5a757787d7dcb0f: {"MOT-Kakumin Pro R", 0x9c753f055238d29a},
	0xe007454455e2d2bb: {"Yoon Gothic 730", 0x8d346dcdd08459d5},
	0xfca7631255290a2c: {"Core Sans", 0x9ae590aec7c63b1c},
	0x7a40659072a8be3c: {"NimbusSanExt Bold", 0xa99cdca7ecee679e},
	0xe6944ade8a140216: {"TT Supermolot Neue Medium", 0x529a0ff67a1c8a0b},
	0x28a718e0d452eff0: {"NimbusSanExt Bold", 0x8455f23e0a9bae87},
	0x5e83c5f2a9b02110: {"FSSinclair", 0xe732ccb97e7e201a},
	0xb56d2abac5d17df2: {"FSSinclair-Medium", 0xd1ebb991c79f934b},
}

// LookupBinding returns the verified association, without exposing mutable state.
func LookupBinding(bakedID uint64) (Binding, bool) { b, ok := bindings[bakedID]; return b, ok }

// ResolveBinding returns indexes into fonts and textures, resolving by identity
// rather than resource order. Known associations require at least eight ASCII
// advance matches within 0.001px. An unknown one-font/one-texture pair is allowed,
// as in atlas_styles.py; ambiguous or changed associations are errors.
func ResolveBinding(bakedID uint64, payload []byte, textures []Texture, fonts []FontMetrics) (fontIndex, textureIndex int, err error) {
	binding, known := LookupBinding(bakedID)
	if !known {
		if len(textures) == 1 && len(fonts) == 1 {
			return 0, 0, nil
		}
		return -1, -1, fmt.Errorf("unrecognized atlas style binding %016x", bakedID)
	}
	fi, ti, fontCount, textureCount := -1, -1, 0, 0
	for i, font := range fonts {
		if font.Name == binding.FontName {
			fi = i
			fontCount++
		}
	}
	for i, texture := range textures {
		if texture.ID == binding.TextureID {
			ti = i
			textureCount++
		}
	}
	if fontCount != 1 || textureCount != 1 {
		return -1, -1, fmt.Errorf("font or texture binding for %016x has changed", bakedID)
	}
	keys, err := GlyphKeys(payload)
	if err != nil {
		return -1, -1, err
	}
	font := fonts[fi]
	size := readFloat(payload, 8)
	if font.UnitsPerEm == 0 || size <= 0 || math.IsNaN(size) || math.IsInf(size, 0) {
		return -1, -1, fmt.Errorf("invalid font metrics for atlas binding")
	}
	matches := 0
	for i, cp := range keys {
		if cp < 33 || cp >= 127 {
			continue
		}
		advance, exists := font.Advances[rune(cp)]
		if !exists {
			continue
		}
		stored := readFloat(payload, GlyphHeaderSize+4*len(keys)+28*i+24)
		expected := float64(advance) * size / float64(font.UnitsPerEm)
		if math.IsNaN(stored) || math.Abs(stored-expected) > 0.001 {
			return -1, -1, fmt.Errorf("atlas advance for U+%04X does not match font %q", cp, font.Name)
		}
		matches++
	}
	if matches < 8 {
		return -1, -1, fmt.Errorf("atlas binding has only %d comparable ASCII advances; need 8", matches)
	}
	return fi, ti, nil
}
