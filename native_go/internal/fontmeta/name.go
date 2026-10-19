package fontmeta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"unicode/utf16"

	"golang.org/x/text/encoding/charmap"
)

type nameRecord struct {
	platform, encoding, language, id uint16
	value                            []byte
	languageTag                      []byte
}

func readNames(b []byte) ([]nameRecord, error) {
	be := binary.BigEndian
	if len(b) < 6 {
		return nil, fmt.Errorf("missing or truncated name table")
	}
	format, count, storage := int(be.Uint16(b)), int(be.Uint16(b[2:])), int(be.Uint16(b[4:]))
	if format > 1 || 6+12*count > len(b) || storage > len(b) || storage < 6+12*count {
		return nil, fmt.Errorf("invalid name table directory")
	}
	read := func(at int) ([]byte, error) {
		length, offset := int(be.Uint16(b[at:])), int(be.Uint16(b[at+2:]))
		if offset > len(b)-storage || length > len(b)-storage-offset {
			return nil, fmt.Errorf("name string outside table")
		}
		return b[storage+offset : storage+offset+length], nil
	}
	var tags [][]byte
	if format == 1 {
		at := 6 + 12*count
		if at+2 > storage {
			return nil, fmt.Errorf("truncated name language tags")
		}
		n := int(be.Uint16(b[at:]))
		at += 2
		if at+4*n > storage {
			return nil, fmt.Errorf("invalid name language tags")
		}
		for i := 0; i < n; i++ {
			v, err := read(at + 4*i)
			if err != nil {
				return nil, err
			}
			tags = append(tags, v)
		}
	}
	result := make([]nameRecord, 0, count)
	for i := 0; i < count; i++ {
		at := 6 + 12*i
		r := nameRecord{platform: be.Uint16(b[at:]), encoding: be.Uint16(b[at+2:]), language: be.Uint16(b[at+4:]), id: be.Uint16(b[at+6:])}
		var err error
		r.value, err = read(at + 8)
		if err != nil {
			return nil, err
		}
		// Language-tag indexes exist only in format 1. Macintosh CID findfont
		// names use 0xffff for "not language-specific", not a tag index.
		cidLanguage := r.platform == 1 && r.id == 20 && r.language == 0xffff
		if format == 1 && r.language >= 0x8000 && !cidLanguage {
			if int(r.language-0x8000) >= len(tags) {
				return nil, fmt.Errorf("invalid name language reference")
			}
			r.languageTag = tags[r.language-0x8000]
		}
		result = append(result, r)
	}
	return result, nil
}

func writeNames(records []nameRecord) ([]byte, error) {
	if len(records) > 65535 {
		return nil, fmt.Errorf("too many name records")
	}
	var tags [][]byte
	tagIDs := map[string]uint16{}
	for i := range records {
		if records[i].languageTag != nil {
			key := string(records[i].languageTag)
			id, ok := tagIDs[key]
			if !ok {
				if len(tags) >= 32768 {
					return nil, fmt.Errorf("too many name languages")
				}
				id = uint16(len(tags)) + 0x8000
				tagIDs[key] = id
				tags = append(tags, records[i].languageTag)
			}
			records[i].language = id
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		a, b := records[i], records[j]
		if a.platform != b.platform {
			return a.platform < b.platform
		}
		if a.encoding != b.encoding {
			return a.encoding < b.encoding
		}
		if a.language != b.language {
			return a.language < b.language
		}
		if a.id != b.id {
			return a.id < b.id
		}
		return bytes.Compare(a.value, b.value) < 0
	})
	start := 6 + 12*len(records)
	if len(tags) > 0 {
		start += 2 + 4*len(tags)
	}
	if start > 65535 {
		return nil, fmt.Errorf("name storage offset exceeds 16 bits")
	}
	out := make([]byte, start)
	be := binary.BigEndian
	be.PutUint16(out[2:], uint16(len(records)))
	be.PutUint16(out[4:], uint16(start))
	stored := map[string]uint16{}
	put := func(at int, value []byte) error {
		if len(value) > 65535 {
			return fmt.Errorf("name exceeds 16-bit length")
		}
		offset, ok := stored[string(value)]
		if !ok {
			if len(out)-start > 65535 {
				return fmt.Errorf("name storage exceeds 16-bit offsets")
			}
			offset = uint16(len(out) - start)
			stored[string(value)] = offset
			out = append(out, value...)
		}
		be.PutUint16(out[at:], uint16(len(value)))
		be.PutUint16(out[at+2:], offset)
		return nil
	}
	for i, r := range records {
		at := 6 + 12*i
		for j, v := range []uint16{r.platform, r.encoding, r.language, r.id} {
			be.PutUint16(out[at+2*j:], v)
		}
		if err := put(at+8, r.value); err != nil {
			return nil, err
		}
	}
	if len(tags) > 0 {
		be.PutUint16(out, 1)
		at := 6 + 12*len(records)
		be.PutUint16(out[at:], uint16(len(tags)))
		for i, v := range tags {
			if err := put(at+2+4*i, v); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func encodeUnicode(value string) []byte {
	units := utf16.Encode([]rune(value))
	out := make([]byte, 2*len(units))
	for i, u := range units {
		binary.BigEndian.PutUint16(out[2*i:], u)
	}
	return out
}

func debugName(records []nameRecord, id uint16) string {
	var mac, some string
	for _, r := range records {
		if r.id != id {
			continue
		}
		var value string
		if r.platform == 0 || r.platform == 3 && (r.encoding == 0 || r.encoding == 1 || r.encoding == 10) {
			if len(r.value)%2 != 0 {
				continue
			}
			units := make([]uint16, len(r.value)/2)
			valid := true
			for i := range units {
				units[i] = binary.BigEndian.Uint16(r.value[2*i:])
			}
			for i := 0; i < len(units); i++ {
				u := units[i]
				if u >= 0xd800 && u <= 0xdbff {
					if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
						valid = false
						break
					}
					i++
				} else if u >= 0xdc00 && u <= 0xdfff {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
			value = string(utf16.Decode(units))
		} else if r.platform == 1 && r.encoding == 0 {
			decoded, err := charmap.Macintosh.NewDecoder().Bytes(r.value)
			if err != nil {
				continue
			}
			value = string(decoded)
		} else {
			continue
		}
		some = value
		if r.platform == 3 && r.language == 0x409 && value != "" {
			return value
		}
		if r.platform == 1 && r.language == 0 && mac == "" {
			mac = value
		}
	}
	if mac != "" {
		return mac
	}
	return some
}
