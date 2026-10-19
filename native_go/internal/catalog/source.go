// Package catalog provides read-only access to normal and prod_slim HD2 archives.
package catalog

import (
	"fmt"
	"io"
	"math"
	"os"
)

// Source reads exactly the requested logical range without keeping file handles
// open. Returned bytes belong to the caller. Implementations support concurrent reads.
type Source interface {
	Size() uint64
	Read(offset, size uint64) ([]byte, error)
}

func checkRange(total, offset, size uint64) error {
	if offset > total || size > total-offset {
		return fmt.Errorf("range [%d,+%d) exceeds %d bytes", offset, size, total)
	}
	if size > uint64(math.MaxInt) {
		return fmt.Errorf("range cannot fit in memory")
	}
	return nil
}

type Plain struct {
	Path string
	size uint64
}

func OpenPlain(path string) (*Plain, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 {
		return nil, fmt.Errorf("not a regular resource file: %s", path)
	}
	return &Plain{Path: path, size: uint64(info.Size())}, nil
}

func (p *Plain) Size() uint64 { return p.size }

func (p *Plain) Read(offset, size uint64) ([]byte, error) {
	if err := checkRange(p.size, offset, size); err != nil {
		return nil, fmt.Errorf("%s: %w", p.Path, err)
	}
	data := make([]byte, int(size))
	if size == 0 {
		return data, nil
	}
	f, err := os.Open(p.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err = f.ReadAt(data, int64(offset)); err != nil {
		return nil, fmt.Errorf("%s: %w", p.Path, err)
	}
	return data, nil
}

// readerAt bridges logical compressed ranges to archiveio's index reader.
type readerAt struct{ Source }

func (r readerAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative read offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if uint64(off) >= r.Size() {
		return 0, io.EOF
	}
	size := min(uint64(len(p)), r.Size()-uint64(off))
	data, err := r.Read(uint64(off), size)
	n := copy(p, data)
	if err == nil && n < len(p) {
		err = io.EOF
	}
	return n, err
}
