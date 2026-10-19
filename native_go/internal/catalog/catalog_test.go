package catalog

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/pierrec/lz4/v4"
	"hd2font/native/internal/archiveio"
)

func save(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func dsarBytes(t *testing.T, parts [][]byte, compressed bool) []byte {
	t.Helper()
	result := make([]byte, 32+32*len(parts))
	copy(result, "DSAR")
	binary.LittleEndian.PutUint32(result[8:], uint32(len(parts)))
	binary.LittleEndian.PutUint32(result[12:], uint32(len(result)))
	var logical uint64
	for i, part := range parts {
		data := part
		kind := byte(0)
		if compressed {
			buf := make([]byte, lz4.CompressBlockBound(len(part)))
			n, err := lz4.CompressBlock(part, buf, nil)
			if err != nil {
				t.Fatal(err)
			}
			if n > 0 {
				data = buf[:n]
				kind = 3
			}
		}
		row := result[32+32*i:]
		binary.LittleEndian.PutUint64(row, logical)
		binary.LittleEndian.PutUint64(row[8:], uint64(len(result)))
		binary.LittleEndian.PutUint32(row[16:], uint32(len(part)))
		binary.LittleEndian.PutUint32(row[20:], uint32(len(data)))
		row[24] = kind
		result = append(result, data...)
		logical += uint64(len(part))
	}
	binary.LittleEndian.PutUint64(result[16:], logical)
	return result
}

func TestSourcesExactRanges(t *testing.T) {
	parts := [][]byte{bytes.Repeat([]byte("abc"), 120), bytes.Repeat([]byte("DEF"), 140), []byte("tail")}
	expected := bytes.Join(parts, nil)
	dir := t.TempDir()
	plainPath, dsarPath := filepath.Join(dir, "plain"), filepath.Join(dir, "compressed")
	save(t, plainPath, expected)
	save(t, dsarPath, dsarBytes(t, parts, true))
	p, err := OpenPlain(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	d, err := OpenDSAR(dsarPath)
	if err != nil {
		t.Fatal(err)
	}
	if d.chunks[0].kind != 3 {
		t.Fatal("test did not exercise LZ4")
	}
	for _, source := range []Source{p, d} {
		for _, span := range [][2]uint64{{0, 0}, {0, uint64(len(expected))}, {350, 30}, {358, 425}, {uint64(len(expected)), 0}} {
			got, err := source.Read(span[0], span[1])
			if err != nil || !bytes.Equal(got, expected[span[0]:span[0]+span[1]]) {
				t.Fatalf("range %v: %x, %v", span, got, err)
			}
		}
		for _, span := range [][2]uint64{{math.MaxUint64, 1}, {1, math.MaxUint64}, {uint64(len(expected)), 1}} {
			if _, err := source.Read(span[0], span[1]); err == nil {
				t.Fatalf("accepted invalid range %v", span)
			}
		}
		got, _ := source.Read(0, 1)
		got[0] = '!'
		again, _ := source.Read(0, 1)
		if again[0] != expected[0] {
			t.Fatal("caller mutated cached bytes")
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				got, err := d.Read(350, 100)
				if err != nil || !bytes.Equal(got, expected[350:450]) {
					t.Errorf("concurrent read: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	save(t, plainPath, []byte("short"))
	if _, err := p.Read(0, p.Size()); err == nil {
		t.Fatal("truncated file accepted")
	}
}

func TestDSARRejectsMalformedRanges(t *testing.T) {
	base := dsarBytes(t, [][]byte{[]byte("abcd"), []byte("efgh")}, false)
	for name, mutate := range map[string]func([]byte){
		"magic":    func(b []byte) { b[0] = '!' },
		"table":    func(b []byte) { binary.LittleEndian.PutUint32(b[8:], math.MaxUint32) },
		"physical": func(b []byte) { binary.LittleEndian.PutUint64(b[40:], math.MaxUint64) },
		"overlap":  func(b []byte) { binary.LittleEndian.PutUint64(b[64:], 1) },
		"kind":     func(b []byte) { b[56] = 2 },
		"length":   func(b []byte) { binary.LittleEndian.PutUint32(b[52:], 3) },
		"lz4":      func(b []byte) { b[56] = 3 },
		"gap":      func(b []byte) { binary.LittleEndian.PutUint64(b[32:], 1); binary.LittleEndian.PutUint32(b[48:], 3) },
	} {
		t.Run(name, func(t *testing.T) {
			data := append([]byte(nil), base...)
			mutate(data)
			path := filepath.Join(t.TempDir(), "dsar")
			save(t, path, data)
			d, err := OpenDSAR(path)
			if err == nil {
				_, err = d.Read(0, d.Size())
			}
			if err == nil {
				t.Fatal("malformed DSAR accepted")
			}
		})
	}
	// The declared output cannot force a multi-gigabyte LZ4 allocation.
	oversized := dsarBytes(t, [][]byte{[]byte("x")}, false)
	binary.LittleEndian.PutUint64(oversized[16:], MaxChunkSize+1)
	binary.LittleEndian.PutUint32(oversized[48:], MaxChunkSize+1)
	oversized[56] = 3
	path := filepath.Join(t.TempDir(), "huge")
	save(t, path, oversized)
	d, err := OpenDSAR(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Read(0, 1); err == nil {
		t.Fatal("oversized decode accepted")
	}
}

func bundleIndex(name string, size uint64, entries []BundleEntry, bundles []string) []byte {
	data := make([]byte, 48+4*len(bundles))
	copy(data, "DSAA")
	binary.LittleEndian.PutUint32(data[12:], uint32(len(bundles)))
	binary.LittleEndian.PutUint32(data[16:], 1)
	binary.LittleEndian.PutUint64(data[24:], size)
	binary.LittleEndian.PutUint32(data[32:], uint32(len(data)))
	data = append(data, append([]byte(name), 0)...)
	for i, bundle := range bundles {
		binary.LittleEndian.PutUint32(data[48+4*i:], uint32(len(data)))
		data = append(data, append([]byte(bundle), 0)...)
	}
	binary.LittleEndian.PutUint32(data[36:], uint32(len(entries)))
	binary.LittleEndian.PutUint64(data[40:], uint64(len(data)))
	for _, e := range entries {
		row := make([]byte, 16)
		binary.LittleEndian.PutUint32(row, uint32(e.ArchiveOffset))
		binary.LittleEndian.PutUint32(row[8:], uint32(e.BundleOffset))
		row[15] = byte(e.BundleIndex)
		data = append(data, row...)
	}
	return data
}

func TestBundledCatalogAndLooseOverride(t *testing.T) {
	const name = "0123456789abcdef"
	typ := archiveio.TypeRecord{TypeID: archiveio.ResourceHash("ttf"), Unknown0: 7}
	e := archiveio.Entry{NameID: 123, TypeID: typ.TypeID, MainAlignment: 16}
	parts, err := archiveio.MakePatchParts([]archiveio.Resource{{Entry: e, Type: typ, Main: []byte("synthetic-font"), Stream: []byte("stream"), GPU: []byte("gpu")}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err = os.Mkdir(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	cut := uint64(97)
	bundle0 := append([]byte("prefix!"), parts.Main[:cut]...)
	bundle1 := append([]byte("prefix2"), parts.Main[cut:]...)
	save(t, filepath.Join(dataDir, "one.nxa"), dsarBytes(t, [][]byte{bundle0}, true))
	save(t, filepath.Join(dataDir, "two.nxa"), dsarBytes(t, [][]byte{bundle1}, true))
	index := bundleIndex(name, uint64(len(parts.Main)), []BundleEntry{{0, 7, 0}, {cut, 7, 1}}, []string{"one.nxa", "two.nxa"})
	save(t, filepath.Join(dataDir, "bundles.nxa"), dsarBytes(t, [][]byte{index}, true))
	save(t, filepath.Join(dataDir, name+".stream"), parts.Stream)
	save(t, filepath.Join(dataDir, name+".gpu_resources"), dsarBytes(t, [][]byte{parts.GPU}, false))
	save(t, filepath.Join(dataDir, name+".patch_0"), []byte("ignored"))
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.ArchiveNames(), []string{name}) {
		t.Fatal(c.ArchiveNames())
	}
	idx, err := c.Resources(name)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 1 || idx.Types[0].Unknown0 != 7 {
		t.Fatal(idx)
	}
	main, stream, gpu, err := c.ReadParts(name, idx.Entries[0])
	if err != nil || string(main) != "synthetic-font" || string(stream) != "stream" || string(gpu) != "gpu" {
		t.Fatalf("parts %q/%q/%q: %v", main, stream, gpu, err)
	}
	if _, err = c.Bundle(2); err == nil {
		t.Fatal("invalid bundle index accepted")
	}
	if _, err = c.Resources("absent"); err == nil {
		t.Fatal("missing archive accepted")
	}
	delete(c.Sources, name+".stream")
	if _, _, _, err = c.ReadParts(name, idx.Entries[0]); err == nil {
		t.Fatal("missing companion accepted")
	}
	if python := os.Getenv("INVENTORY_ORACLE_PYTHON"); python != "" {
		request := map[string]any{"game": dir, "archive": name, "dsar": filepath.Join(dataDir, "two.nxa")}
		input, _ := json.Marshal(request)
		cmd := exec.Command(python, "testdata/oracle.py")
		cmd.Stdin = bytes.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("oracle: %v\n%s", err, out)
		}
		var got struct{ Main, Stream, GPU, DSAR string }
		if err = json.Unmarshal(out, &got); err != nil {
			t.Fatal(err, string(out))
		}
		if got.Main != hex.EncodeToString(main) || got.Stream != hex.EncodeToString(stream) || got.GPU != hex.EncodeToString(gpu) || got.DSAR != hex.EncodeToString(bundle1) {
			t.Fatal("Python catalog differs", string(out))
		}
	}
	// The index still exists, but a matching loose archive takes precedence.
	save(t, filepath.Join(dataDir, name), parts.Main)
	c, err = Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Archives[name].(*Plain); !ok {
		t.Fatal("loose archive did not override bundle")
	}
	if _, err = c.Resources(name); err != nil {
		t.Fatal(err)
	}
}

func TestBundleIndexRejectsBadNamesAndTables(t *testing.T) {
	for _, name := range []string{"../outside.nxa", "..", `C:\outside.nxa`, ""} {
		c := &Catalog{Sources: make(map[string]Source)}
		data := bundleIndex("0123456789abcdef", 1, []BundleEntry{{0, 0, 0}}, []string{name})
		if err := c.readBundleIndex(data); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	valid := bundleIndex("0123456789abcdef", 1, []BundleEntry{{0, 0, 0}}, []string{"one.nxa"})
	for n := 0; n < len(valid); n++ {
		c := &Catalog{Sources: make(map[string]Source)}
		if err := c.readBundleIndex(valid[:n]); err == nil {
			t.Fatalf("accepted index truncated at %d", n)
		}
	}
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("empty catalog accepted")
	}
}

func TestArchiveEncounterOrder(t *testing.T) {
	dir := t.TempDir()
	const bundled = "ffffffffffffffff"
	const loose = "0000000000000000"
	index := bundleIndex(bundled, 1, []BundleEntry{{0, 0, 0}}, []string{"unopened.nxa"})
	save(t, filepath.Join(dir, "bundles.nxa"), dsarBytes(t, [][]byte{index}, false))
	save(t, filepath.Join(dir, bundled), []byte("override"))
	save(t, filepath.Join(dir, loose), []byte("new loose source"))
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.ArchiveNames(), []string{bundled, loose}) {
		t.Fatal("changed Python archive/fallback order", c.ArchiveNames())
	}
}
