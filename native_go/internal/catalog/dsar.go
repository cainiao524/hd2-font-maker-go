package catalog

import (
	"container/list"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/pierrec/lz4/v4"
)

// MaxChunkSize bounds a single DSAR decompression allocation. Files may have
// arbitrarily many chunks; the shared cache also has a 64 MiB byte budget.
const MaxChunkSize = 64 << 20

type chunk struct {
	logical, physical uint64
	size, compressed  uint32
	kind              byte
}

type DSAR struct {
	Path   string
	source *Plain
	size   uint64
	chunks []chunk
}

func OpenDSAR(path string) (*DSAR, error) {
	source, err := OpenPlain(path)
	if err != nil {
		return nil, err
	}
	header, err := source.Read(0, 32)
	if err != nil {
		return nil, err
	}
	if string(header[:4]) != "DSAR" {
		return nil, fmt.Errorf("%s: invalid DSAR magic", path)
	}
	d := &DSAR{Path: path, source: source, size: binary.LittleEndian.Uint64(header[16:])}
	if d.size > math.MaxInt64 {
		return nil, fmt.Errorf("DSAR logical size exceeds int64")
	}
	count := uint64(binary.LittleEndian.Uint32(header[8:]))
	table, err := source.Read(32, count*32)
	if err != nil {
		return nil, fmt.Errorf("DSAR chunk table: %w", err)
	}
	d.chunks = make([]chunk, int(count))
	var previousEnd uint64
	for i := range d.chunks {
		row := table[i*32:]
		c := chunk{binary.LittleEndian.Uint64(row), binary.LittleEndian.Uint64(row[8:]), binary.LittleEndian.Uint32(row[16:]), binary.LittleEndian.Uint32(row[20:]), row[24]}
		if c.size == 0 || c.logical < previousEnd {
			return nil, fmt.Errorf("DSAR chunk %d is empty, overlapping or unordered", i)
		}
		if err := checkRange(d.size, c.logical, uint64(c.size)); err != nil {
			return nil, fmt.Errorf("DSAR chunk %d: %w", i, err)
		}
		if err := checkRange(source.Size(), c.physical, uint64(c.compressed)); err != nil {
			return nil, fmt.Errorf("DSAR compressed chunk %d: %w", i, err)
		}
		if c.physical < 32+count*32 {
			return nil, fmt.Errorf("DSAR chunk %d overlaps its index", i)
		}
		d.chunks[i] = c
		previousEnd = c.logical + uint64(c.size)
	}
	return d, nil
}

func (d *DSAR) Size() uint64 { return d.size }

type cacheKey struct {
	source *DSAR
	index  int
}
type cachedChunk struct {
	key  cacheKey
	data []byte
}

// A process-wide cache avoids retaining a decompressed chunk for every archive
// visited during a full index scan, matching Python's shared 32-entry LRU.
var chunkCache = struct {
	sync.Mutex
	items map[cacheKey]*list.Element
	order list.List
	bytes int
}{items: make(map[cacheKey]*list.Element)}

func (d *DSAR) chunk(index int) ([]byte, error) {
	key := cacheKey{d, index}
	chunkCache.Lock()
	if e := chunkCache.items[key]; e != nil {
		chunkCache.order.MoveToFront(e)
		data := e.Value.(cachedChunk).data
		chunkCache.Unlock()
		return data, nil
	}
	chunkCache.Unlock()
	c := d.chunks[index]
	if c.size > MaxChunkSize || c.compressed > MaxChunkSize {
		return nil, fmt.Errorf("DSAR chunk %d exceeds %d-byte decode limit", index, MaxChunkSize)
	}
	data, err := d.source.Read(c.physical, uint64(c.compressed))
	if err != nil {
		return nil, err
	}
	switch c.kind {
	case 0:
	case 3:
		decoded := make([]byte, int(c.size))
		n, err := lz4.UncompressBlock(data, decoded)
		if err != nil {
			return nil, fmt.Errorf("DSAR LZ4 chunk %d: %w", index, err)
		}
		data = decoded[:n]
	default:
		return nil, fmt.Errorf("unsupported DSAR compression %d", c.kind)
	}
	if len(data) != int(c.size) {
		return nil, fmt.Errorf("DSAR chunk %d decoded %d bytes, expected %d", index, len(data), c.size)
	}
	chunkCache.Lock()
	defer chunkCache.Unlock()
	if e := chunkCache.items[key]; e != nil {
		chunkCache.order.MoveToFront(e)
		return e.Value.(cachedChunk).data, nil
	}
	for chunkCache.order.Len() >= 32 || chunkCache.bytes+len(data) > MaxChunkSize {
		e := chunkCache.order.Back()
		old := e.Value.(cachedChunk)
		delete(chunkCache.items, old.key)
		chunkCache.bytes -= len(old.data)
		chunkCache.order.Remove(e)
	}
	chunkCache.items[key] = chunkCache.order.PushFront(cachedChunk{key, data})
	chunkCache.bytes += len(data)
	return data, nil
}

func (d *DSAR) Read(offset, size uint64) ([]byte, error) {
	if err := checkRange(d.size, offset, size); err != nil {
		return nil, err
	}
	result := make([]byte, int(size))
	for at := 0; at < len(result); {
		i := sort.Search(len(d.chunks), func(i int) bool { return d.chunks[i].logical > offset }) - 1
		if i < 0 || offset-d.chunks[i].logical >= uint64(d.chunks[i].size) {
			return nil, fmt.Errorf("DSAR has no chunk at offset %d", offset)
		}
		data, err := d.chunk(i)
		if err != nil {
			return nil, err
		}
		n := copy(result[at:], data[int(offset-d.chunks[i].logical):])
		at += n
		offset += uint64(n)
	}
	return result, nil
}
