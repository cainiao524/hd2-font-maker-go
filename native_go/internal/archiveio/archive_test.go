package archiveio

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type goldenResource struct {
	Entry  [13]uint64 `json:"entry"`
	Type   [7]uint64  `json:"type"`
	Main   string     `json:"main"`
	Stream string     `json:"stream"`
	GPU    string     `json:"gpu"`
}

type goldenPatch struct {
	Name      string           `json:"name"`
	MainOnly  bool             `json:"main_only"`
	Resources []goldenResource `json:"resources"`
	Expected  [3]string        `json:"expected"`
}

type goldenFixture struct {
	Hashes []struct {
		Text  string `json:"text"`
		Value uint64 `json:"value"`
	} `json:"hashes"`
	Patches []goldenPatch `json:"patches"`
	Runtime struct {
		Template string `json:"template"`
		Parts    []struct {
			Metrics [3]float32 `json:"metrics"`
			Data    string     `json:"data"`
		} `json:"parts"`
		Expected string   `json:"expected"`
		Decoded  []string `json:"decoded"`
	} `json:"runtime"`
}

func fixture(t *testing.T) goldenFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/python_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden goldenFixture
	if err = json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	return golden
}

func unhex(t *testing.T, value string) []byte {
	t.Helper()
	data, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func resourcesFor(t *testing.T, rows []goldenResource) []Resource {
	t.Helper()
	resources := make([]Resource, len(rows))
	for i, row := range rows {
		e, typ := row.Entry, row.Type
		resources[i] = Resource{
			Entry: Entry{e[0], e[1], e[2], e[3], e[4], e[5], e[6], uint32(e[7]), uint32(e[8]), uint32(e[9]), uint32(e[10]), uint32(e[11]), uint32(e[12])},
			Type:  TypeRecord{uint32(typ[0]), uint32(typ[1]), typ[2], uint32(typ[3]), uint32(typ[4]), uint32(typ[5]), uint32(typ[6])},
			Main:  unhex(t, row.Main), Stream: unhex(t, row.Stream), GPU: unhex(t, row.GPU),
		}
	}
	return resources
}

func TestPythonResourceHashGolden(t *testing.T) {
	for _, sample := range fixture(t).Hashes {
		if got := ResourceHash(sample.Text); got != sample.Value {
			t.Errorf("ResourceHash(%q) = %#x, want %#x", sample.Text, got, sample.Value)
		}
	}
}

func TestPythonPatchGolden(t *testing.T) {
	for _, sample := range fixture(t).Patches {
		t.Run(sample.Name, func(t *testing.T) {
			resources := resourcesFor(t, sample.Resources)
			original := resourcesFor(t, sample.Resources)
			var actual [3][]byte
			if sample.MainOnly {
				main, err := MakePatch(resources)
				if err != nil {
					t.Fatal(err)
				}
				actual[0] = main
			} else {
				parts, err := MakePatchParts(resources)
				if err != nil {
					t.Fatal(err)
				}
				actual = [3][]byte{parts.Main, parts.Stream, parts.GPU}
				folder := t.TempDir()
				paths := [3]string{filepath.Join(folder, "main"), filepath.Join(folder, "main.stream"), filepath.Join(folder, "main.gpu_resources")}
				sizes, err := WritePatchParts(resources, paths)
				if err != nil {
					t.Fatal(err)
				}
				for i, path := range paths {
					saved, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(saved, actual[i]) || sizes[i] != uint64(len(saved)) {
						t.Errorf("channel %d streamed output differs", i)
					}
				}
			}
			var sizes [3]uint64
			for i := range actual {
				if !bytes.Equal(actual[i], unhex(t, sample.Expected[i])) {
					t.Errorf("channel %d differs from original Python bytes", i)
				}
				sizes[i] = uint64(len(actual[i]))
			}
			reader, err := NewReader(bytes.NewReader(actual[0]), sizes[0])
			if err != nil {
				t.Fatal(err)
			}
			index, err := ReadIndex(reader)
			if err != nil {
				t.Fatal(err)
			}
			if err = index.ValidateBounds(sizes); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original, resources) {
				t.Fatal("builder mutated its input")
			}
		})
	}
}

func TestRecordRoundTripAndTruncation(t *testing.T) {
	raw := unhex(t, fixture(t).Patches[0].Expected[0])
	h, err := ParseHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	h.Reserved0[5], h.Reserved1[17] = 31, 91
	data, _ := h.MarshalBinary()
	h2, err := ParseHeader(data)
	if err != nil || h != h2 || len(data) != HeaderSize {
		t.Fatalf("header round trip: %v", err)
	}
	typ, err := ParseType(raw[HeaderSize:])
	if err != nil {
		t.Fatal(err)
	}
	data, _ = typ.MarshalBinary()
	typ2, err := ParseType(data)
	if err != nil || typ != typ2 || len(data) != TypeSize {
		t.Fatalf("type round trip: %v", err)
	}
	e, err := ParseEntry(raw[HeaderSize+int(h.TypeCount)*TypeSize:])
	if err != nil {
		t.Fatal(err)
	}
	data, _ = e.MarshalBinary()
	e2, err := ParseEntry(data)
	if err != nil || e != e2 || len(data) != EntrySize {
		t.Fatalf("entry round trip: %v", err)
	}
	for n := 0; n < HeaderSize; n++ {
		if _, err := ParseHeader(raw[:n]); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("short header %d: %v", n, err)
		}
	}
	if _, err := ParseType(raw[:TypeSize-1]); err == nil {
		t.Error("accepted short type")
	}
	if _, err := ParseEntry(raw[:EntrySize-1]); err == nil {
		t.Error("accepted short entry")
	}
	raw[0] ^= 1
	if _, err := ParseHeader(raw); err == nil {
		t.Error("accepted bad magic")
	}
}

func TestBoundedReaderAndMalformedIndex(t *testing.T) {
	r, err := NewReader(bytes.NewReader([]byte("abc")), 3)
	if err != nil {
		t.Fatal(err)
	}
	if r.Size() != 3 {
		t.Fatal("wrong size")
	}
	for _, test := range [][2]uint64{{4, 0}, {3, 1}, {math.MaxUint64, 2}, {1, math.MaxUint64}} {
		if _, err := r.Read(test[0], test[1]); err == nil {
			t.Errorf("accepted invalid range %v", test)
		}
	}
	if empty, err := r.Read(3, 0); err != nil || len(empty) != 0 {
		t.Fatalf("EOF empty range: %v", err)
	}
	section, err := r.Section(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(section)
	if err != nil || string(data) != "bc" {
		t.Fatalf("section: %q, %v", data, err)
	}
	r, _ = NewReader(bytes.NewReader([]byte("a")), 3)
	if _, err := r.Read(0, 3); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated reader: %v", err)
	}
	if _, err := NewReader(nil, 0); err == nil {
		t.Error("accepted nil reader")
	}
	if _, err := NewReader(bytes.NewReader(nil), math.MaxUint64); err == nil {
		t.Error("accepted unsupported reader size")
	}
	header, _ := (Header{Magic: Magic, TypeCount: math.MaxUint32, EntryCount: math.MaxUint32}).MarshalBinary()
	r, _ = NewReader(bytes.NewReader(header), uint64(len(header)))
	if _, err := ReadIndex(r); err == nil {
		t.Error("accepted out-of-bounds index tables")
	}
}

func TestPatchRejectionsAndExistingFiles(t *testing.T) {
	resource := Resource{Entry: Entry{NameID: 1, TypeID: 7}, Type: TypeRecord{TypeID: 7}, Main: []byte("new")}
	if _, err := MakePatch(nil); err == nil {
		t.Error("accepted empty single-file patch")
	}
	if _, err := MakePatchParts([]Resource{resource, resource}); err == nil {
		t.Error("accepted duplicate resource")
	}
	other := resource
	other.Type.TypeID = 8
	if _, err := MakePatchParts([]Resource{other}); err == nil {
		t.Error("accepted mismatched type")
	}
	other = resource
	other.Entry.StreamSize = 1
	if _, err := MakePatch([]Resource{other}); err == nil {
		t.Error("discarded unhandled external data")
	}
	folder := t.TempDir()
	paths := [3]string{filepath.Join(folder, "new"), filepath.Join(folder, "old-release"), filepath.Join(folder, "gpu")}
	if err := os.WriteFile(paths[1], []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := WritePatchParts([]Resource{resource}, paths); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing file error: %v", err)
	}
	data, err := os.ReadFile(paths[1])
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing output changed: %q, %v", data, err)
	}
	if _, err := os.Stat(paths[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete output remains: %v", err)
	}
}

func TestValidateCompanionBounds(t *testing.T) {
	parts, err := MakePatchParts([]Resource{{Entry: Entry{NameID: 1, TypeID: 7}, Type: TypeRecord{TypeID: 7}, Main: []byte("main"), Stream: []byte("stream"), GPU: []byte("gpu")}})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := NewReader(bytes.NewReader(parts.Main), uint64(len(parts.Main)))
	index, err := ReadIndex(r)
	if err != nil {
		t.Fatal(err)
	}
	sizes := [3]uint64{uint64(len(parts.Main)), uint64(len(parts.Stream)), uint64(len(parts.GPU))}
	for _, change := range []func(*Entry){
		func(e *Entry) { e.MainOffset = 1 },
		func(e *Entry) { e.MainOffset = math.MaxUint64 },
		func(e *Entry) { e.StreamSize++ },
		func(e *Entry) { e.GPUOffset = math.MaxUint64 },
	} {
		mutated := index
		mutated.Entries = append([]Entry(nil), index.Entries...)
		change(&mutated.Entries[0])
		if err := mutated.ValidateBounds(sizes); err == nil {
			t.Error("accepted invalid resource bounds")
		}
	}
}

func TestPythonRuntimeGolden(t *testing.T) {
	sample := fixture(t).Runtime
	parts := make([]RuntimePart, len(sample.Parts))
	for i, part := range sample.Parts {
		parts[i] = RuntimePart{Metrics: part.Metrics, Data: unhex(t, part.Data)}
	}
	actual, err := MakeRuntime(unhex(t, sample.Template), parts)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, unhex(t, sample.Expected)) {
		t.Fatal("runtime container differs from original Python bytes")
	}
	decoded, err := RuntimeParts(actual)
	if err != nil {
		t.Fatal(err)
	}
	for i, part := range decoded {
		for j, metric := range part.Metrics {
			if math.Float32bits(metric) != math.Float32bits(parts[i].Metrics[j]) {
				t.Errorf("part %d metric %d differs", i, j)
			}
		}
		if !bytes.Equal(part.Data, unhex(t, sample.Decoded[i])) {
			t.Errorf("part %d font size differs", i)
		}
		part.Data[0] ^= 1
	}
	if !bytes.Equal(actual, unhex(t, sample.Expected)) {
		t.Fatal("decoded font data aliases input")
	}
}

func TestRuntimeMalformedAndRelocatedTable(t *testing.T) {
	sample := fixture(t).Runtime
	good := unhex(t, sample.Expected)
	for _, change := range []func([]byte) []byte{
		func(p []byte) []byte { return p[:39] },
		func(p []byte) []byte { binary.LittleEndian.PutUint32(p[32:], 0); return p },
		func(p []byte) []byte { binary.LittleEndian.PutUint32(p[32:], math.MaxUint32); return p },
		func(p []byte) []byte { binary.LittleEndian.PutUint32(p[36:], 39); return p },
		func(p []byte) []byte { binary.LittleEndian.PutUint32(p[52:], 40); return p },
		func(p []byte) []byte { binary.LittleEndian.PutUint32(p[52:], math.MaxUint32); return p },
		func(p []byte) []byte { p[80] = 'x'; return p },
	} {
		if _, err := RuntimeParts(change(bytes.Clone(good))); err == nil {
			t.Error("accepted malformed runtime container")
		}
	}
	// The decoder accepts the original format's table pointer, not just 40.
	relocated := append(bytes.Clone(good[:40]), make([]byte, 16)...)
	relocated = append(relocated, good[40:]...)
	binary.LittleEndian.PutUint32(relocated[36:], 56)
	for i := range 2 {
		at := 56 + 16*i + 12
		binary.LittleEndian.PutUint32(relocated[at:], binary.LittleEndian.Uint32(relocated[at:])+16)
	}
	if parts, err := RuntimeParts(relocated); err != nil || len(parts) != 2 {
		t.Fatalf("relocated table: %v", err)
	}
	if _, err := MakeRuntime(make([]byte, 39), nil); err == nil {
		t.Error("accepted short template")
	}
	empty, err := MakeRuntime(make([]byte, 40), nil)
	if err != nil || len(empty) != 40 || binary.LittleEndian.Uint32(empty[36:]) != 40 {
		t.Fatalf("empty encoder: %v", err)
	}
	if _, err = RuntimeParts(empty); err == nil {
		t.Error("empty decoder must match Python rejection")
	}
	font := unhex(t, sample.Parts[0].Data)
	binary.BigEndian.PutUint32(font[20:], math.MaxUint32)
	binary.BigEndian.PutUint32(font[24:], math.MaxUint32)
	if _, err = SFNTSize(font); err == nil {
		t.Error("accepted overflowing SFNT table bounds")
	}
}

func FuzzRuntimeParts(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 40))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = RuntimeParts(data) })
}

func FuzzReadIndex(f *testing.F) {
	f.Add([]byte{})
	header, _ := (Header{Magic: Magic}).MarshalBinary()
	f.Add(header)
	f.Fuzz(func(t *testing.T, data []byte) {
		r, _ := NewReader(bytes.NewReader(data), uint64(len(data)))
		_, _ = ReadIndex(r)
	})
}
