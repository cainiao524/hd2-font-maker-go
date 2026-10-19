package archiveio

import (
	"encoding/binary"
	"fmt"
	"math"
)

// RuntimePart stores ascent, descent, line height and unmodified SFNT bytes.
type RuntimePart struct {
	Metrics [3]float32
	Data    []byte
}

// SFNTSize follows fonts.sfnt_size, excluding unrelated trailing resource bytes.
func SFNTSize(data []byte) (int, error) {
	if len(data) < 12 {
		return 0, fmt.Errorf("incomplete SFNT header")
	}
	if magic := string(data[:4]); magic != "\x00\x01\x00\x00" && magic != "OTTO" && magic != "true" {
		return 0, fmt.Errorf("unsupported SFNT signature")
	}
	count := int(binary.BigEndian.Uint16(data[4:6]))
	end := uint64(12 + count*16)
	if end > uint64(len(data)) {
		return 0, fmt.Errorf("incomplete SFNT table directory")
	}
	for i := range count {
		record := data[12+i*16:]
		offset := uint64(binary.BigEndian.Uint32(record[8:12]))
		size := uint64(binary.BigEndian.Uint32(record[12:16]))
		end = max(end, offset+size)
	}
	if end > uint64(len(data)) {
		return 0, fmt.Errorf("SFNT table exceeds resource bounds")
	}
	return int(end), nil
}

// RuntimeParts decodes fonts.runtime_parts, returning owned copies of each font.
func RuntimeParts(payload []byte) ([]RuntimePart, error) {
	if len(payload) < 40 {
		return nil, fmt.Errorf("incomplete runtime font header")
	}
	count := uint64(binary.LittleEndian.Uint32(payload[32:36]))
	at := uint64(binary.LittleEndian.Uint32(payload[36:40]))
	tableEnd := at + 16*count
	if count == 0 || at < 40 || tableEnd > uint64(len(payload)) {
		return nil, fmt.Errorf("unsupported runtime font structure")
	}
	parts := make([]RuntimePart, int(count))
	for i := range parts {
		record := payload[int(at)+16*i:]
		start := uint64(binary.LittleEndian.Uint32(record[12:16]))
		if start < tableEnd || start > uint64(len(payload)) {
			return nil, fmt.Errorf("runtime font %d has invalid offset", i)
		}
		size, err := SFNTSize(payload[int(start):])
		if err != nil {
			return nil, fmt.Errorf("runtime font %d: %w", i, err)
		}
		for j := range parts[i].Metrics {
			parts[i].Metrics[j] = math.Float32frombits(binary.LittleEndian.Uint32(record[j*4:]))
		}
		parts[i].Data = append([]byte(nil), payload[int(start):int(start)+size]...)
	}
	return parts, nil
}

// MakeRuntime preserves template[:32], rebuilds records at offset 40, aligns each
// font to 16 bytes, and appends each Data slice verbatim, without final padding.
// Like fonts.make_runtime it can encode zero parts; RuntimeParts rejects those.
func MakeRuntime(template []byte, parts []RuntimePart) ([]byte, error) {
	if len(template) < 40 {
		return nil, fmt.Errorf("incomplete runtime font template")
	}
	if uint64(len(parts)) > math.MaxUint32 {
		return nil, fmt.Errorf("too many runtime font parts")
	}
	size := uint64(40) + uint64(len(parts))*16
	for _, part := range parts {
		size = align(size, 16)
		if size > math.MaxUint32 {
			return nil, fmt.Errorf("runtime font offset exceeds uint32")
		}
		size += uint64(len(part.Data))
		if size > uint64(math.MaxInt) {
			return nil, fmt.Errorf("runtime container cannot fit in memory")
		}
	}
	if size > uint64(math.MaxInt) {
		return nil, fmt.Errorf("runtime table cannot fit in memory")
	}
	result := make([]byte, int(size))
	copy(result[:40], template[:40])
	binary.LittleEndian.PutUint32(result[32:36], uint32(len(parts)))
	binary.LittleEndian.PutUint32(result[36:40], 40)
	position := uint64(40) + uint64(len(parts))*16
	for i, part := range parts {
		position = align(position, 16)
		record := result[40+16*i:]
		for j, metric := range part.Metrics {
			binary.LittleEndian.PutUint32(record[j*4:], math.Float32bits(metric))
		}
		binary.LittleEndian.PutUint32(record[12:16], uint32(position))
		copy(result[int(position):], part.Data)
		position += uint64(len(part.Data))
	}
	return result, nil
}
