// Package inventory scans font resources and persists self-contained snapshots.
package inventory

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"

	"golang.org/x/text/encoding/charmap"
	"hd2font/native/internal/archiveio"
	"hd2font/native/internal/sfntio"
)

// Face owns the original, trimmed SFNT bytes. Chars and Cmap describe the single
// preferred Unicode cmap, using the same preference order as fontTools.
type Face struct {
	Name       string
	Group      string
	Data       []byte
	Chars      map[rune]struct{}
	Digest     string
	UnitsPerEm uint16
	UPM        uint16
	Weight     uint16
	Format     string
	Cmap       map[rune]uint16
	Advances   map[rune]uint16
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func GroupFor(name string) string {
	s := strings.ToLower(name)
	for _, item := range []struct {
		group   string
		needles []string
	}{
		{"sc", []string{"xianghe hei sc", "xiangheheisc", "ying hei prc", "yingheiprc"}},
		{"tc", []string{"xianghe hei tc", "xiangheheitc", "mjngaihk"}},
		{"jp", []string{"kakumin", "folkpro", "folk pro"}},
		{"ko", []string{"yoon", "core sans", "coresans"}},
		{"arabic", []string{"arabic"}}, {"bengali", []string{"bengali"}},
		{"devanagari", []string{"devanagari"}}, {"thai", []string{"thai"}},
		{"emoji", []string{"emoji"}}, {"special", []string{"steelworks", "illuminate", "cyborg"}},
		{"latin", []string{"sinclair", "supermolot", "nimbus", "noto sans"}},
	} {
		for _, needle := range item.needles {
			if strings.Contains(s, needle) {
				return item.group
			}
		}
	}
	return "other"
}

func InspectFace(raw []byte) (*Face, error) {
	size, err := archiveio.SFNTSize(raw)
	if err != nil {
		return nil, err
	}
	data := append([]byte(nil), raw[:size]...)
	f, err := sfntio.Parse(data, 0)
	if err != nil {
		return nil, err
	}
	name, err := fontName(f.Tables["name"])
	if err != nil {
		return nil, err
	}
	cmap, err := bestCmap(f.Tables["cmap"])
	if err != nil {
		return nil, fmt.Errorf("font %q: %w", name, err)
	}
	if len(cmap) == 0 {
		return nil, fmt.Errorf("font %q has no Unicode glyph mappings", name)
	}
	face := &Face{Name: name, Group: GroupFor(name), Data: data, Digest: Digest(data), Chars: make(map[rune]struct{}, len(cmap)), Cmap: cmap, Advances: make(map[rune]uint16), Weight: 400}
	face.Format = "ttf"
	if _, ok := f.Tables["CFF "]; ok || f.Version == 0x4f54544f {
		face.Format = "cff"
	}
	for cp := range cmap {
		face.Chars[cp] = struct{}{}
	}
	if head := f.Tables["head"]; len(head) >= 20 {
		face.UnitsPerEm = binary.BigEndian.Uint16(head[18:])
		face.UPM = face.UnitsPerEm
	}
	if os2 := f.Tables["OS/2"]; len(os2) >= 6 {
		face.Weight = binary.BigEndian.Uint16(os2[4:])
	}
	if hhea, hmtx := f.Tables["hhea"], f.Tables["hmtx"]; len(hhea) >= 36 {
		count := int(binary.BigEndian.Uint16(hhea[34:]))
		if count == 0 || len(hmtx) < 4*count {
			return nil, fmt.Errorf("font %q has incomplete horizontal metrics", name)
		}
		for cp, gid := range cmap {
			at := 4 * min(int(gid), count-1)
			face.Advances[cp] = binary.BigEndian.Uint16(hmtx[at:])
		}
	}
	return face, nil
}

func (f *Face) Codepoints() []rune {
	chars := make([]rune, 0, len(f.Chars))
	for cp := range f.Chars {
		chars = append(chars, cp)
	}
	sort.Slice(chars, func(i, j int) bool { return chars[i] < chars[j] })
	return chars
}

func fontName(data []byte) (string, error) { return nameRecord(data, 4, 1) }

// nameRecord returns the first name of ids found, preferring English Windows
// and then Mac Roman records, as fontName always has.
func nameRecord(data []byte, ids ...uint16) (string, error) {
	if len(data) < 6 {
		return "", fmt.Errorf("missing or truncated font name table")
	}
	count, storage := int(binary.BigEndian.Uint16(data[2:])), int(binary.BigEndian.Uint16(data[4:]))
	if 6+12*count > len(data) || storage > len(data) {
		return "", fmt.Errorf("truncated font name records")
	}
	for _, id := range ids {
		var last, macEnglish string
		for i := 0; i < count; i++ {
			r := data[6+12*i:]
			platform, encoding, language := binary.BigEndian.Uint16(r), binary.BigEndian.Uint16(r[2:]), binary.BigEndian.Uint16(r[4:])
			if binary.BigEndian.Uint16(r[6:]) != id {
				continue
			}
			size, at := int(binary.BigEndian.Uint16(r[8:])), storage+int(binary.BigEndian.Uint16(r[10:]))
			if at > len(data) || size > len(data)-at {
				return "", fmt.Errorf("font name string exceeds table")
			}
			raw := data[at : at+size]
			var name string
			if platform == 0 || (platform == 3 && (encoding == 0 || encoding == 1 || encoding == 10)) {
				if len(raw)%2 != 0 {
					continue
				}
				units := make([]uint16, len(raw)/2)
				for j := range units {
					units[j] = binary.BigEndian.Uint16(raw[2*j:])
				}
				name = string(utf16.Decode(units))
			} else if platform == 1 && encoding == 0 {
				decoded, err := charmap.Macintosh.NewDecoder().Bytes(raw)
				if err != nil {
					continue
				}
				name = string(decoded)
			} else {
				continue
			}
			last = name
			if platform == 3 && language == 0x409 && name != "" {
				return name, nil
			}
			if platform == 1 && language == 0 && macEnglish == "" {
				macEnglish = name
			}
		}
		if macEnglish != "" {
			return macEnglish, nil
		}
		if last != "" {
			return last, nil
		}
	}
	return "Unnamed font", nil
}
