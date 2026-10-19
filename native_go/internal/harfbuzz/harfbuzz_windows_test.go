//go:build windows && amd64

package harfbuzz

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"hd2font/native/internal/geometry"
	"hd2font/native/internal/sfntio"
)

func engine(t *testing.T) *Engine {
	t.Helper()
	e, err := Open(filepath.Join("..", "..", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testfixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func parsed(t *testing.T, data []byte) *sfntio.Font {
	t.Helper()
	f, err := sfntio.Parse(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func assertStatic(t *testing.T, data []byte) {
	t.Helper()
	f := parsed(t, data)
	for _, tag := range []string{"fvar", "gvar", "avar", "HVAR", "VVAR", "MVAR", "CFF2"} {
		if len(f.Tables[tag]) != 0 {
			t.Errorf("static output still has %s", tag)
		}
	}
}

func TestABIAndVersion(t *testing.T) {
	if unsafe.Sizeof(nativeResult{}) != 288 || unsafe.Sizeof(nativePath{}) != 272 ||
		unsafe.Sizeof(nativeFace{}) != 264 || unsafe.Sizeof(geometry.Command{}) != 28 ||
		unsafe.Sizeof(axis{}) != 8 || unsafe.Sizeof(mapping{}) != 8 {
		t.Fatal("C ABI layout")
	}
	if got := engine(t).Version(); got != "14.6.0" {
		t.Fatalf("version %s", got)
	}
}

func TestInstance(t *testing.T) {
	e := engine(t)
	for _, name := range []string{"base.ttf", "base.otf", "variable.ttf", "cff2.otf"} {
		t.Run(name, func(t *testing.T) {
			source := fixture(t, name)
			out, err := e.Instance(source, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertStatic(t, out)
			if name == "cff2.otf" && len(parsed(t, out).Tables["CFF "]) == 0 {
				t.Fatal("missing CFF1")
			}
			for gid := uint32(0); gid < 4; gid++ {
				want, err := e.NativeDraw(source, gid, 1000)
				if err != nil {
					t.Fatal(err)
				}
				got, err := e.NativeDraw(out, gid, 1000)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("glyph %d changed: %v", gid, err)
				}
			}
		})
	}
	out, err := e.Instance(fixture(t, "variable.ttf"), 0, map[string]float32{"wght": 900})
	if err != nil {
		t.Fatal(err)
	}
	assertStatic(t, out)
	oracle := fixture(t, "variable-900-oracle.ttf")
	for gid := uint32(0); gid < 4; gid++ {
		want, err := e.NativeDraw(oracle, gid, 1000)
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.NativeDraw(out, gid, 1000)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("instance glyph %d does not match FontTools: %v", gid, err)
		}
	}
	gotMetrics := parsed(t, out).Tables["hmtx"]
	wantMetrics := parsed(t, oracle).Tables["hmtx"]
	if !reflect.DeepEqual(gotMetrics, wantMetrics) {
		t.Fatalf("instance metrics %v != %v", gotMetrics, wantMetrics)
	}
}

func TestSubsetClosureAndRemapOffset(t *testing.T) {
	e := engine(t)
	for _, ext := range []string{"ttf", "otf"} {
		t.Run(ext, func(t *testing.T) {
			source := fixture(t, "donor."+ext)
			subset, err := e.Subset(source, []uint32{66, 87})
			if err != nil {
				t.Fatal(err)
			}
			f := parsed(t, subset)
			if binary.BigEndian.Uint16(f.Tables["maxp"][4:]) != 4 {
				t.Fatal("lost GSUB ligature closure")
			}
			for _, tag := range []string{"GSUB", "GPOS", "GDEF"} {
				if len(f.Tables[tag]) == 0 {
					t.Fatalf("lost %s", tag)
				}
			}
			want := map[uint32]uint32{0: 0, 1: 4, 2: 5, 3: 6}
			for _, request := range []map[uint32]uint32{want, {1: 4, 2: 5}} {
				out, actual, err := e.Remap(source, request)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(actual, want) {
					t.Fatalf("mapping %v", actual)
				}
				if binary.BigEndian.Uint16(parsed(t, out).Tables["maxp"][4:]) != 7 {
					t.Fatal("offset font does not have 7 glyph slots")
				}
				for old, next := range actual {
					before, err := e.NativeDraw(source, old, 1000)
					if err != nil {
						t.Fatal(err)
					}
					after, err := e.NativeDraw(out, next, 1000)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("remapped glyph %d -> %d changed: %v", old, next, err)
					}
				}
				for gid := uint32(1); gid < 4; gid++ {
					path, err := e.NativeDraw(out, gid, 1000)
					if err != nil || len(path) != 0 {
						t.Fatalf("gap glyph %d: %v %v", gid, path, err)
					}
				}
			}
		})
	}
}

func TestEmptySelectionAndErrors(t *testing.T) {
	e := engine(t)
	data := fixture(t, "donor.ttf")
	for _, cps := range [][]uint32{nil, {0x10ffff}} {
		out, err := e.Subset(data, cps)
		if err != nil {
			t.Fatal(err)
		}
		if binary.BigEndian.Uint16(parsed(t, out).Tables["maxp"][4:]) != 4 {
			t.Fatal("empty subset must retain compatibility glyphs and their closure")
		}
		glyphs, err := e.Shape(out, "BW", ShapeOptions{})
		if err != nil || len(glyphs) != 2 || glyphs[0].ID != 0 || glyphs[1].ID != 0 {
			t.Fatalf("compatibility glyphs leaked into Unicode cmap: %v %v", glyphs, err)
		}
	}
	if _, actual, err := e.Remap(data, nil); err != nil || !reflect.DeepEqual(actual, map[uint32]uint32{0: 0}) {
		t.Fatalf("empty remap %v %v", actual, err)
	}
	for _, request := range []map[uint32]uint32{{0: 4}, {1: 0}, {1: 4, 2: 4}, {5: 6}, {1: 65535}, {1: 65534, 2: 65533}} {
		if _, _, err := e.Remap(data, request); err == nil {
			t.Fatalf("accepted invalid mapping %v", request)
		}
	}
	if _, _, err := e.Remap(data, map[uint32]uint32{1: 5, 2: 4}); err == nil || !strings.Contains(err.Error(), "non-monotonic") {
		t.Fatalf("permutation error: %v", err)
	}
	for _, input := range [][]byte{nil, []byte("invalid font"), data[:32]} {
		if _, err := e.Instance(input, 0, nil); err == nil {
			t.Fatal("accepted invalid font")
		}
	}
	for _, axes := range []map[string]float32{{"BAD": 1}, {"xxxx": 1}, {"wght": float32(math.NaN())}} {
		if _, err := e.Instance(fixture(t, "variable.ttf"), 0, axes); err == nil {
			t.Fatalf("accepted invalid axes %v", axes)
		}
	}
	if _, err := e.Instance(data, 1, nil); err == nil {
		t.Fatal("accepted invalid standalone face index")
	}
	if _, err := e.Subset(data, []uint32{0xd800}); err != nil {
		t.Fatalf("rejected opaque source cmap code: %v", err)
	}
	if _, err := e.Subset(data, []uint32{0x110000}); err == nil {
		t.Fatal("accepted invalid codepoint")
	}
	if _, err := e.NativeDraw(data, 4, 0); err == nil {
		t.Fatal("accepted invalid glyph")
	}
}

func TestSharedFaceConcurrentDrawAndLifetime(t *testing.T) {
	e := engine(t)
	data := fixture(t, "base.otf")
	f, err := e.NewFace(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	want, err := f.Draw(1, 1000)
	if err != nil || len(want) == 0 {
		t.Fatalf("draw: %v", err)
	}
	clear(data) // The face must own a copy, including under GC and concurrent use.
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			for j := 0; j < 60; j++ {
				got, err := f.Draw(1, 1000)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Errorf("concurrent draw: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
	doubled, err := f.Draw(1, 2000)
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		for p := range want[i].Points {
			if doubled[i].Points[p] != 2*want[i].Points[p] {
				t.Fatal("UPM scaling")
			}
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Draw(1, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed face: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Subset(nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed engine: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
}
