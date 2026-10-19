// Package fontmeta edits font registration metadata without changing outlines.
package fontmeta

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"hd2font/native/internal/sfntio"
)

type Axis struct {
	Tag               string
	Min, Default, Max float32
}

// Axes reads ordered fvar axes from a standalone font or selected TTC face.
// A static font returns an empty slice. Values are decoded signed 16.16 units.
func Axes(data []byte, index int) ([]Axis, error) {
	f, err := sfntio.Parse(data, index)
	if err != nil {
		return nil, err
	}
	b, exists := f.Tables["fvar"]
	if !exists {
		return nil, nil
	}
	if len(b) < 16 || binary.BigEndian.Uint32(b) != 0x10000 {
		return nil, fmt.Errorf("invalid fvar header")
	}
	be := binary.BigEndian
	offset, count, size := int(be.Uint16(b[4:])), int(be.Uint16(b[8:])), int(be.Uint16(b[10:]))
	if offset < 16 || size < 20 || offset > len(b) || count > (len(b)-offset)/size {
		return nil, fmt.Errorf("invalid fvar axis range")
	}
	result := make([]Axis, 0, count)
	seen := make(map[string]bool, count)
	for i := 0; i < count; i++ {
		record := b[offset+i*size:]
		a := Axis{Tag: string(record[:4]), Min: float32(int32(be.Uint32(record[4:]))) / 65536,
			Default: float32(int32(be.Uint32(record[8:]))) / 65536, Max: float32(int32(be.Uint32(record[12:]))) / 65536}
		if a.Min > a.Default || a.Default > a.Max || seen[a.Tag] {
			return nil, fmt.Errorf("invalid fvar axis %q", a.Tag)
		}
		seen[a.Tag] = true
		result = append(result, a)
	}
	return result, nil
}

// Register matches fonts.py register_font: template identity names and selected
// style fields replace the source's registration metadata. All input bytes are
// immutable. CFF output must be a standalone name-keyed, no-subroutine CFF1 table
// as emitted by cffconv.Table. Template CFF may be name-keyed or CID-keyed.
func Register(data, template []byte) ([]byte, error) {
	f, err := sfntio.Parse(data, 0)
	if err != nil {
		return nil, err
	}
	ref, err := sfntio.Parse(template, 0)
	if err != nil {
		return nil, fmt.Errorf("registration template: %w", err)
	}
	names, err := readNames(f.Tables["name"])
	if err != nil {
		return nil, err
	}
	refNames, err := readNames(ref.Tables["name"])
	if err != nil {
		return nil, fmt.Errorf("template names: %w", err)
	}
	identity := func(id uint16) bool {
		switch id {
		case 1, 2, 4, 6, 16, 17, 21, 22:
			return true
		}
		return false
	}
	var merged []nameRecord
	for _, r := range names {
		if !identity(r.id) && r.id != 3 {
			merged = append(merged, r)
		}
	}
	for _, r := range refNames {
		if identity(r.id) {
			merged = append(merged, r)
		}
	}
	sourceHash, templateHash := sha256.Sum256(data), sha256.Sum256(template)
	unique := fmt.Sprintf("HD2Custom-%x-%x", sourceHash[:8], templateHash[:6])
	merged = append(merged, nameRecord{platform: 3, encoding: 1, language: 0x409, id: 3, value: encodeUnicode(unique)})
	f.Tables["name"], err = writeNames(merged)
	if err != nil {
		return nil, err
	}
	if len(f.Tables["OS/2"]) < 64 || len(ref.Tables["OS/2"]) < 64 || len(f.Tables["head"]) < 46 || len(ref.Tables["head"]) < 46 {
		return nil, fmt.Errorf("registration requires complete OS/2 and head style fields")
	}
	copy(f.Tables["OS/2"][4:8], ref.Tables["OS/2"][4:8])
	be := binary.BigEndian
	for _, field := range []struct {
		tag    string
		offset int
		mask   uint16
	}{{"OS/2", 62, 0x263}, {"head", 44, 3}} {
		a, b := f.Tables[field.tag][field.offset:], ref.Tables[field.tag][field.offset:]
		be.PutUint16(a, be.Uint16(a)&^field.mask|be.Uint16(b)&field.mask)
	}
	if cff, ok := f.Tables["CFF "]; ok {
		f.Tables["CFF "], err = registerCFF(cff, ref.Tables["CFF "], refNames)
		if err != nil {
			return nil, fmt.Errorf("CFF registration: %w", err)
		}
	}
	return f.Bytes()
}
