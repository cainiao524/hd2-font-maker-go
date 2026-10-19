// Package sfntio preserves raw SFNT tables and encodes static TrueType outlines.
package sfntio

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"sort"
)

// Font owns unpadded table bytes. Glyph IDs and table contents are not interpreted
// or reordered. Bytes changes only head.checkSumAdjustment within table data.
type Font struct {
	Version uint32
	Tables  map[string][]byte
}

func supportedVersion(version uint32) bool {
	return version == 0x00010000 || version == 0x4f54544f || version == 0x74727565
}

func checkedRange(data []byte, offset, size uint64) ([]byte, error) {
	if offset > uint64(len(data)) || size > uint64(len(data))-offset {
		return nil, fmt.Errorf("SFNT range [%d,+%d) exceeds %d bytes", offset, size, len(data))
	}
	return data[int(offset):int(offset+size)], nil
}

// Parse extracts owned table copies from TTF/OTF or a selected TTC v1/v2 face.
// A standalone font accepts only index 0. Checksums and table semantics are not
// validated here; Bytes rebuilds checksums, including after extracting a TTC face.
func Parse(data []byte, index int) (*Font, error) {
	if index < 0 {
		return nil, fmt.Errorf("font index must be nonnegative")
	}
	header, err := checkedRange(data, 0, 12)
	if err != nil {
		return nil, err
	}
	var directoryOffset, collectionHeaderSize uint64
	if string(header[:4]) == "ttcf" {
		version := binary.BigEndian.Uint32(header[4:8])
		if version != 0x00010000 && version != 0x00020000 {
			return nil, fmt.Errorf("unsupported TTC version %#x", version)
		}
		count := uint64(binary.BigEndian.Uint32(header[8:12]))
		if uint64(index) >= count {
			return nil, fmt.Errorf("font index %d exceeds TTC face count %d", index, count)
		}
		collectionHeaderSize = 12 + 4*count
		if version == 0x00020000 {
			collectionHeaderSize += 12
		}
		collection, err := checkedRange(data, 0, collectionHeaderSize)
		if err != nil {
			return nil, fmt.Errorf("TTC header: %w", err)
		}
		directoryOffset = uint64(binary.BigEndian.Uint32(collection[12+4*index:]))
		if directoryOffset < collectionHeaderSize {
			return nil, fmt.Errorf("TTC font directory overlaps collection header")
		}
		header, err = checkedRange(data, directoryOffset, 12)
		if err != nil {
			return nil, fmt.Errorf("TTC font directory: %w", err)
		}
	} else if index != 0 {
		return nil, fmt.Errorf("standalone font requires index 0")
	}
	version := binary.BigEndian.Uint32(header[:4])
	if !supportedVersion(version) {
		return nil, fmt.Errorf("unsupported SFNT version %#x", version)
	}
	count := int(binary.BigEndian.Uint16(header[4:6]))
	directorySize := uint64(12 + count*16)
	directory, err := checkedRange(data, directoryOffset, directorySize)
	if err != nil {
		return nil, fmt.Errorf("SFNT table directory: %w", err)
	}
	font := &Font{Version: version, Tables: make(map[string][]byte, count)}
	for i := range count {
		record := directory[12+16*i:]
		tag := string(record[:4])
		if _, exists := font.Tables[tag]; exists {
			return nil, fmt.Errorf("duplicate SFNT table %q", tag)
		}
		// TTC offsets are absolute within the collection, not face-relative.
		offset := uint64(binary.BigEndian.Uint32(record[8:12]))
		size := uint64(binary.BigEndian.Uint32(record[12:16]))
		payload, err := checkedRange(data, offset, size)
		if err != nil {
			return nil, fmt.Errorf("SFNT table %q: %w", tag, err)
		}
		if size != 0 && (offset < collectionHeaderSize || (offset < directoryOffset+directorySize && offset+size > directoryOffset)) {
			return nil, fmt.Errorf("SFNT table %q overlaps font directory", tag)
		}
		font.Tables[tag] = append([]byte{}, payload...)
	}
	return font, nil
}

// checksum treats an incomplete final uint32 as zero-padded, as required for
// unpadded table lengths in a directory. Addition intentionally wraps at 32 bits.
func checksum(data []byte) uint32 {
	var sum uint32
	for len(data) >= 4 {
		sum += binary.BigEndian.Uint32(data[:4])
		data = data[4:]
	}
	for i, value := range data {
		sum += uint32(value) << (24 - 8*i)
	}
	return sum
}

// Bytes writes a standalone SFNT with sorted records, four-byte alignment, table
// checksums, and a whole-font sum of 0xB1B0AFBA. Tables and their backing slices
// remain unchanged. A head table of at least 12 bytes is required for adjustment.
func (font *Font) Bytes() ([]byte, error) {
	if font == nil || !supportedVersion(font.Version) {
		return nil, fmt.Errorf("unsupported or nil SFNT font")
	}
	count := len(font.Tables)
	// searchRange and rangeShift are uint16 values measured in 16-byte records.
	if count == 0 || count > math.MaxUint16/16 {
		return nil, fmt.Errorf("SFNT table count %d cannot be encoded", count)
	}
	if len(font.Tables["head"]) < 12 {
		return nil, fmt.Errorf("SFNT head table is missing or shorter than 12 bytes")
	}
	tags := make([]string, 0, count)
	size := uint64(12 + 16*count)
	for tag, data := range font.Tables {
		if len(tag) != 4 {
			return nil, fmt.Errorf("SFNT table tag %q must contain exactly four bytes", tag)
		}
		tags = append(tags, tag)
		size += (uint64(len(data)) + 3) &^ uint64(3)
		if size > math.MaxUint32 || size > uint64(math.MaxInt) {
			return nil, fmt.Errorf("SFNT exceeds supported offset or memory size")
		}
	}
	sort.Strings(tags)
	result := make([]byte, int(size))
	binary.BigEndian.PutUint32(result, font.Version)
	binary.BigEndian.PutUint16(result[4:], uint16(count))
	selector := bits.Len(uint(count)) - 1
	searchRange := (1 << selector) * 16
	binary.BigEndian.PutUint16(result[6:], uint16(searchRange))
	binary.BigEndian.PutUint16(result[8:], uint16(selector))
	binary.BigEndian.PutUint16(result[10:], uint16(count*16-searchRange))
	offset := 12 + 16*count
	var adjustmentOffset int
	for i, tag := range tags {
		data := font.Tables[tag]
		record := result[12+16*i:]
		copy(record[:4], tag)
		binary.BigEndian.PutUint32(record[8:], uint32(offset))
		binary.BigEndian.PutUint32(record[12:], uint32(len(data)))
		payload := result[offset : offset+len(data)]
		copy(payload, data)
		if tag == "head" {
			clear(payload[8:12])
			adjustmentOffset = offset + 8
		}
		binary.BigEndian.PutUint32(record[4:], checksum(payload))
		offset += (len(data) + 3) &^ 3
	}
	binary.BigEndian.PutUint32(result[adjustmentOffset:], 0xb1b0afba-checksum(result))
	return result, nil
}
