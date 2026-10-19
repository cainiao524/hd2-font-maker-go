//go:build windows && amd64

package harfbuzz

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"testing"
)

type sparseCase struct {
	Source, Output []byte
	Mapping        map[uint32]uint32
}

func sparseRemap(t *testing.T, e *Engine, source []byte, offset uint32) sparseCase {
	t.Helper()
	count := uint32(binary.BigEndian.Uint16(parsed(t, source).Tables["maxp"][4:]))
	request := map[uint32]uint32{0: 0}
	for gid := uint32(1); gid < count; gid++ {
		request[gid] = offset + gid - 1
	}
	out, actual, err := e.Remap(source, request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, request) {
		t.Fatalf("unexpected completed mapping %v", actual)
	}
	f := parsed(t, out)
	if got := uint32(binary.BigEndian.Uint16(f.Tables["maxp"][4:])); got != offset+count-1 {
		t.Fatalf("output glyph slots %d", got)
	}
	before, err := e.NewFace(source, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()
	after, err := e.NewFace(out, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	for old, newID := range request {
		a, err := before.Draw(old, 0)
		if err != nil {
			t.Fatal(err)
		}
		b, err := after.Draw(newID, 0)
		if err != nil || !reflect.DeepEqual(a, b) {
			t.Fatalf("outline %d -> %d changed: %v", old, newID, err)
		}
	}
	if offset > 1 {
		for _, gid := range []uint32{1, offset / 2, offset - 1} {
			p, err := after.Draw(gid, 0)
			if err != nil || len(p) != 0 {
				t.Fatalf("gap glyph %d has outline: %v", gid, err)
			}
		}
	}
	t.Logf("offset=%d sourceGlyphs=%d outputSlots=%d hmtx=%d vmtx=%d loca=%d post=%d", offset, count, offset+count-1, len(f.Tables["hmtx"]), len(f.Tables["vmtx"]), len(f.Tables["loca"]), len(f.Tables["post"]))
	return sparseCase{source, out, actual}
}

func TestSparseMetadataHighOffsets(t *testing.T) {
	e := engine(t)
	source := extra(t, "sparse-20.ttf")
	var cases []sparseCase
	for _, offset := range []uint32{1, 100, 55377, 60000} {
		t.Run(fmt.Sprint(offset), func(t *testing.T) { cases = append(cases, sparseRemap(t, e, source, offset)) })
	}
	python := os.Getenv("HD2_FONTTOOLS_PYTHON")
	if python == "" {
		return
	}
	data, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, "sparse_oracle.py", "verify")
	cmd.Stdin = bytes.NewReader(data)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sparse FontTools oracle: %v\n%s", err, output)
	}
	t.Logf("independent sparse metadata oracle: %s", output)
}

// Explicit opt-in: this fixture remains private and is never copied or bundled.
func TestPrivateSparseRemap(t *testing.T) {
	path := os.Getenv("HD2_HARFBUZZ_PRIVATE_REMAP_INPUT")
	if path == "" {
		t.Skip("private local reproduction not requested")
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	e := engine(t)
	for _, offset := range []uint32{55377, 60000} {
		t.Run(fmt.Sprint(offset), func(t *testing.T) { sparseRemap(t, e, source, offset) })
	}
}
