package catalog

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"hd2font/native/internal/archiveio"
)

const Boot = "9ba626afa44a3aa3"

var sourceName = regexp.MustCompile(`^[0-9a-f]{16}(?:\.stream|\.gpu_resources)?$`)

type BundleEntry struct {
	ArchiveOffset uint64
	BundleOffset  uint64
	BundleIndex   int
}

type Bundled struct {
	catalog *Catalog
	size    uint64
	entries []BundleEntry
}

func NewBundled(c *Catalog, size uint64, entries []BundleEntry) (*Bundled, error) {
	if c == nil || size > math.MaxInt64 {
		return nil, fmt.Errorf("invalid bundled source")
	}
	for i, e := range entries {
		if e.ArchiveOffset >= size || (i == 0 && e.ArchiveOffset != 0) || (i > 0 && e.ArchiveOffset <= entries[i-1].ArchiveOffset) {
			return nil, fmt.Errorf("invalid bundled entry %d offset", i)
		}
		if e.BundleIndex < 0 || e.BundleIndex >= len(c.BundleNames) {
			return nil, fmt.Errorf("invalid bundle index %d", e.BundleIndex)
		}
	}
	if size != 0 && len(entries) == 0 {
		return nil, fmt.Errorf("nonempty bundled source has no entries")
	}
	return &Bundled{catalog: c, size: size, entries: append([]BundleEntry(nil), entries...)}, nil
}

func (b *Bundled) Size() uint64 { return b.size }

func (b *Bundled) Read(offset, size uint64) ([]byte, error) {
	if err := checkRange(b.size, offset, size); err != nil {
		return nil, err
	}
	result := make([]byte, int(size))
	for at := 0; at < len(result); {
		i := sort.Search(len(b.entries), func(i int) bool { return b.entries[i].ArchiveOffset > offset }) - 1
		if i < 0 {
			return nil, fmt.Errorf("no bundled entry at offset %d", offset)
		}
		e := b.entries[i]
		end := b.size
		if i+1 < len(b.entries) {
			end = b.entries[i+1].ArchiveOffset
		}
		take := min(uint64(len(result)-at), end-offset)
		bundle, err := b.catalog.Bundle(e.BundleIndex)
		if err != nil {
			return nil, err
		}
		within := offset - e.ArchiveOffset
		if within > math.MaxUint64-e.BundleOffset {
			return nil, fmt.Errorf("bundle offset overflow")
		}
		data, err := bundle.Read(e.BundleOffset+within, take)
		if err != nil {
			return nil, err
		}
		at += copy(result[at:], data)
		offset += take
	}
	return result, nil
}

// Catalog indexes base archives only. Mods and patch files are not scanned.
// Exported maps are for lookup; callers must not mutate them during reads.
type Catalog struct {
	Data        string
	Archives    map[string]Source
	Sources     map[string]Source
	BundleNames []string
	sourceOrder []string
	mu          sync.Mutex
	bundles     map[int]*DSAR
}

func ReadOnlyCatalog(gamePath string) (*Catalog, error) { return Open(gamePath) }

func Open(gamePath string) (*Catalog, error) {
	if gamePath == "~" || strings.HasPrefix(gamePath, "~/") || strings.HasPrefix(gamePath, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		gamePath = filepath.Join(home, strings.TrimLeft(gamePath[1:], `/\`))
	}
	data, err := filepath.Abs(gamePath)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(filepath.Join(data, "data")); err == nil && info.IsDir() {
		data = filepath.Join(data, "data")
	}
	files, err := os.ReadDir(data)
	if err != nil {
		return nil, err
	}
	c := &Catalog{Data: data, Archives: make(map[string]Source), Sources: make(map[string]Source), bundles: make(map[int]*DSAR)}
	if info, err := os.Stat(filepath.Join(data, "bundles.nxa")); err == nil && info.Mode().IsRegular() {
		index, err := OpenDSAR(filepath.Join(data, "bundles.nxa"))
		if err != nil {
			return nil, err
		}
		payload, err := index.Read(0, index.Size())
		if err != nil {
			return nil, err
		}
		if err = c.readBundleIndex(payload); err != nil {
			return nil, err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, file := range files {
		if !sourceName.MatchString(file.Name()) || file.IsDir() {
			continue
		}
		plain, err := OpenPlain(filepath.Join(data, file.Name()))
		if err != nil {
			return nil, err
		}
		magic, err := plain.Read(0, min(4, plain.Size()))
		if err != nil {
			return nil, err
		}
		var source Source = plain
		if string(magic) == "DSAR" {
			source, err = OpenDSAR(plain.Path)
			if err != nil {
				return nil, err
			}
		}
		// Loose files override the corresponding bundled source.
		if _, exists := c.Sources[file.Name()]; !exists {
			c.sourceOrder = append(c.sourceOrder, file.Name())
		}
		c.Sources[file.Name()] = source
	}
	for name, source := range c.Sources {
		if !strings.Contains(name, ".") {
			c.Archives[name] = source
		}
	}
	if len(c.Archives) == 0 {
		return nil, fmt.Errorf("no HD2 base archives found in %s", data)
	}
	return c, nil
}

func (c *Catalog) readBundleIndex(data []byte) error {
	if len(data) < 24 || string(data[:4]) != "DSAA" {
		return fmt.Errorf("invalid bundles.nxa DSAA header")
	}
	nb, na := uint64(binary.LittleEndian.Uint32(data[12:])), uint64(binary.LittleEndian.Uint32(data[16:]))
	if err := checkRange(uint64(len(data)), 24, 24*na+4*nb); err != nil {
		return fmt.Errorf("DSAA tables: %w", err)
	}
	filename := func(offset uint64) (string, error) {
		if offset >= uint64(len(data)) {
			return "", fmt.Errorf("DSAA filename offset exceeds index")
		}
		raw := data[int(offset):]
		end := bytes.IndexByte(raw, 0)
		if end < 0 {
			return "", fmt.Errorf("unterminated DSAA filename")
		}
		name := string(raw[:end])
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\:`) {
			return "", fmt.Errorf("invalid DSAA filename %q", name)
		}
		for _, b := range raw[:end] {
			if b < 32 || b > 126 {
				return "", fmt.Errorf("non-ASCII DSAA filename")
			}
		}
		return name, nil
	}
	c.BundleNames = make([]string, int(nb))
	for i := range c.BundleNames {
		name, err := filename(uint64(binary.LittleEndian.Uint32(data[24+24*int(na)+4*i:])))
		if err != nil {
			return err
		}
		c.BundleNames[i] = name
	}
	for i := 0; i < int(na); i++ {
		row := data[24+24*i:]
		size, count, at := binary.LittleEndian.Uint64(row), uint64(binary.LittleEndian.Uint32(row[12:])), binary.LittleEndian.Uint64(row[16:])
		name, err := filename(uint64(binary.LittleEndian.Uint32(row[8:])))
		if err != nil {
			return err
		}
		if !sourceName.MatchString(name) {
			continue
		}
		if _, exists := c.Sources[name]; exists {
			return fmt.Errorf("duplicate DSAA source %q", name)
		}
		if err := checkRange(uint64(len(data)), at, count*16); err != nil {
			return fmt.Errorf("DSAA entries: %w", err)
		}
		entries := make([]BundleEntry, int(count))
		for j := range entries {
			r := data[int(at)+16*j:]
			entries[j] = BundleEntry{uint64(binary.LittleEndian.Uint32(r)), uint64(binary.LittleEndian.Uint32(r[8:])), int(r[15])}
		}
		c.Sources[name], err = NewBundled(c, size, entries)
		if err != nil {
			return fmt.Errorf("DSAA source %s: %w", name, err)
		}
		c.sourceOrder = append(c.sourceOrder, name)
	}
	return nil
}

func (c *Catalog) ArchiveNames() []string {
	names := make([]string, 0, len(c.Archives))
	seen := make(map[string]bool, len(c.Archives))
	for _, name := range c.sourceOrder {
		if _, ok := c.Archives[name]; ok && !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	// Manually constructed catalogs retain deterministic ordering as well.
	var remaining []string
	for name := range c.Archives {
		if !seen[name] {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	return append(names, remaining...)
}

func (c *Catalog) Bundle(index int) (*DSAR, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if index < 0 || index >= len(c.BundleNames) {
		return nil, fmt.Errorf("bundle index %d out of range", index)
	}
	if bundle := c.bundles[index]; bundle != nil {
		return bundle, nil
	}
	bundle, err := OpenDSAR(filepath.Join(c.Data, c.BundleNames[index]))
	if err != nil {
		return nil, err
	}
	if c.bundles == nil {
		c.bundles = make(map[int]*DSAR)
	}
	c.bundles[index] = bundle
	return bundle, nil
}

func (c *Catalog) Resources(name string) (archiveio.Index, error) {
	source, ok := c.Archives[name]
	if !ok {
		return archiveio.Index{}, fmt.Errorf("unknown archive %q", name)
	}
	r, err := archiveio.NewReader(readerAt{source}, source.Size())
	if err != nil {
		return archiveio.Index{}, err
	}
	index, err := archiveio.ReadIndex(r)
	if err != nil {
		return index, fmt.Errorf("archive %s: %w", name, err)
	}
	return index, nil
}

func (c *Catalog) ReadParts(name string, e archiveio.Entry) (main, stream, gpu []byte, err error) {
	parts := [3][]byte{}
	offsets := [3]uint64{e.MainOffset, e.StreamOffset, e.GPUOffset}
	sizes := [3]uint32{e.MainSize, e.StreamSize, e.GPUSize}
	for channel, suffix := range [3]string{"", ".stream", ".gpu_resources"} {
		if sizes[channel] == 0 {
			continue
		}
		source, ok := c.Sources[name+suffix]
		if !ok {
			return nil, nil, nil, fmt.Errorf("missing companion %s%s", name, suffix)
		}
		parts[channel], err = source.Read(offsets[channel], uint64(sizes[channel]))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("resource %016x channel %d: %w", e.NameID, channel, err)
		}
	}
	return parts[0], parts[1], parts[2], nil
}
