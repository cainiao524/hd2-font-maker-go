// Package archiveio reads HD2 archive metadata and writes extracted resources
// without interpreting or changing their payloads.
package archiveio

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

const (
	Magic      uint32 = 0xf0000011
	HeaderSize        = 72
	TypeSize          = 32
	EntrySize         = 80
)

// Header is Python archives.HEADER: <III20sQQ24s.
type Header struct {
	Magic      uint32
	TypeCount  uint32
	EntryCount uint32
	Reserved0  [20]byte
	MainSize   uint64
	GPUSize    uint64
	Reserved1  [24]byte
}

// TypeRecord is Python archives.TYPE: <IIQIIII. Unknown fields are preserved.
type TypeRecord struct {
	Unknown0 uint32
	Unknown1 uint32
	TypeID   uint64
	Count    uint32
	Unknown2 uint32
	Unknown3 uint32
	Unknown4 uint32
}

// Entry is Python archives.ENTRY: <7Q6I, in exactly the same field order.
type Entry struct {
	NameID        uint64
	TypeID        uint64
	MainOffset    uint64
	StreamOffset  uint64
	GPUOffset     uint64
	Unknown0      uint64
	Unknown1      uint64
	MainSize      uint32
	StreamSize    uint32
	GPUSize       uint32
	MainAlignment uint32
	GPUAlignment  uint32
	Index         uint32
}

func encodeRecord(value any, size int) ([]byte, error) {
	data := make([]byte, size)
	_, err := binary.Encode(data, binary.LittleEndian, value)
	return data, err
}

func decodeRecord(data []byte, size int, value any) error {
	if len(data) < size {
		return io.ErrUnexpectedEOF
	}
	_, err := binary.Decode(data[:size], binary.LittleEndian, value)
	return err
}

func (h Header) MarshalBinary() ([]byte, error)     { return encodeRecord(h, HeaderSize) }
func (t TypeRecord) MarshalBinary() ([]byte, error) { return encodeRecord(t, TypeSize) }
func (e Entry) MarshalBinary() ([]byte, error)      { return encodeRecord(e, EntrySize) }

func ParseHeader(data []byte) (Header, error) {
	var h Header
	if err := decodeRecord(data, HeaderSize, &h); err != nil {
		return h, fmt.Errorf("archive header: %w", err)
	}
	if h.Magic != Magic {
		return h, fmt.Errorf("unsupported archive magic %#x", h.Magic)
	}
	return h, nil
}

func ParseType(data []byte) (TypeRecord, error) {
	var t TypeRecord
	err := decodeRecord(data, TypeSize, &t)
	return t, err
}

func ParseEntry(data []byte) (Entry, error) {
	var e Entry
	err := decodeRecord(data, EntrySize, &e)
	return e, err
}

// ResourceHash is the original seed-zero MurmurHash64A over UTF-8 bytes.
func ResourceHash(text string) uint64 {
	const mix uint64 = 0xc6a4a7935bd1e995
	raw := []byte(text)
	value := uint64(len(raw)) * mix
	for len(raw) >= 8 {
		word := binary.LittleEndian.Uint64(raw)
		word *= mix
		word ^= word >> 47
		value = (value ^ (word * mix)) * mix
		raw = raw[8:]
	}
	if len(raw) != 0 {
		var tail uint64
		for i, b := range raw {
			tail |= uint64(b) << (8 * i)
		}
		value = (value ^ tail) * mix
	}
	value ^= value >> 47
	value *= mix
	return value ^ (value >> 47)
}

// Reader restricts random reads to the declared source size. It does not own r.
// Use an immutable ReaderAt, such as bytes.Reader or a read-only os.File.
type Reader struct {
	r    io.ReaderAt
	size uint64
}

func NewReader(r io.ReaderAt, size uint64) (*Reader, error) {
	if r == nil || size > math.MaxInt64 {
		return nil, fmt.Errorf("invalid archive reader or size")
	}
	return &Reader{r: r, size: size}, nil
}

func (r *Reader) Size() uint64 { return r.size }

// Section offers bounded streaming access without allocating the whole range.
func (r *Reader) Section(offset, size uint64) (*io.SectionReader, error) {
	if offset > r.size || size > r.size-offset {
		return nil, fmt.Errorf("archive range [%d,+%d) exceeds %d bytes", offset, size, r.size)
	}
	return io.NewSectionReader(r.r, int64(offset), int64(size)), nil
}

// Read returns exactly size bytes, or an error, including for truncated sources.
func (r *Reader) Read(offset, size uint64) ([]byte, error) {
	section, err := r.Section(offset, size)
	if err != nil {
		return nil, err
	}
	if size > uint64(math.MaxInt) {
		return nil, fmt.Errorf("archive range cannot fit in memory")
	}
	data := make([]byte, int(size))
	if _, err = io.ReadFull(section, data); err != nil {
		return nil, fmt.Errorf("archive range: %w", err)
	}
	return data, nil
}

// Index retains on-disk record order and all unknown metadata fields.
type Index struct {
	Header  Header
	Types   []TypeRecord
	Entries []Entry
}

// ReadIndex reads only the archive header and tables, never resource payloads.
func ReadIndex(r *Reader) (Index, error) {
	var index Index
	data, err := r.Read(0, HeaderSize)
	if err != nil {
		return index, err
	}
	index.Header, err = ParseHeader(data)
	if err != nil {
		return index, err
	}
	typeBytes := uint64(index.Header.TypeCount) * TypeSize
	entryBytes := uint64(index.Header.EntryCount) * EntrySize
	data, err = r.Read(HeaderSize, typeBytes+entryBytes)
	if err != nil {
		return index, fmt.Errorf("archive index: %w", err)
	}
	index.Types = make([]TypeRecord, int(index.Header.TypeCount))
	for i := range index.Types {
		index.Types[i], err = ParseType(data[i*TypeSize:])
		if err != nil {
			return index, err
		}
	}
	data = data[int(typeBytes):]
	index.Entries = make([]Entry, int(index.Header.EntryCount))
	for i := range index.Entries {
		index.Entries[i], err = ParseEntry(data[i*EntrySize:])
		if err != nil {
			return index, err
		}
	}
	return index, nil
}

// ValidateBounds checks resource offsets against main, stream, GPU file sizes.
func (index Index) ValidateBounds(sizes [3]uint64) error {
	tableSize := uint64(HeaderSize) + uint64(len(index.Types))*TypeSize + uint64(len(index.Entries))*EntrySize
	if index.Header.Magic != Magic || uint64(index.Header.TypeCount) != uint64(len(index.Types)) || uint64(index.Header.EntryCount) != uint64(len(index.Entries)) || tableSize > sizes[0] {
		return fmt.Errorf("invalid archive index dimensions")
	}
	for _, e := range index.Entries {
		if e.MainSize != 0 && e.MainOffset < tableSize {
			return fmt.Errorf("resource %#x overlaps archive index", e.NameID)
		}
		offsets := [3]uint64{e.MainOffset, e.StreamOffset, e.GPUOffset}
		lengths := [3]uint64{uint64(e.MainSize), uint64(e.StreamSize), uint64(e.GPUSize)}
		for channel, offset := range offsets {
			if offset > sizes[channel] || lengths[channel] > sizes[channel]-offset {
				return fmt.Errorf("resource %#x channel %d exceeds companion bounds", e.NameID, channel)
			}
		}
	}
	return nil
}
