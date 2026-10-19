package cffconv

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
)

// readIndex is a small test-only structural reader. The independent fontTools
// test below also reads every complete CFF table and executes its charstrings.
func readIndex(t *testing.T, b []byte, offset int) ([][]byte, int) {
	t.Helper()
	count := int(binary.BigEndian.Uint16(b[offset:]))
	if count == 0 {
		return nil, offset + 2
	}
	size := int(b[offset+2])
	if size < 1 || size > 4 {
		t.Fatalf("invalid offset size %d", size)
	}
	base := offset + 3 + (count+1)*size - 1
	offsets := make([]int, count+1)
	for i := range offsets {
		for j := 0; j < size; j++ {
			offsets[i] = offsets[i]<<8 | int(b[offset+3+i*size+j])
		}
	}
	if offsets[0] != 1 {
		t.Fatal("INDEX offsets do not start at one")
	}
	objects := make([][]byte, count)
	for i := range objects {
		if offsets[i+1] < offsets[i] || base+offsets[i+1] > len(b) {
			t.Fatal("INDEX offset outside data")
		}
		objects[i] = b[base+offsets[i] : base+offsets[i+1]]
	}
	return objects, base + offsets[count]
}

func TestIndexOffsetSizes(t *testing.T) {
	for _, tc := range []struct{ length, size int }{{0, 1}, {254, 1}, {255, 2}, {65534, 2}, {65535, 3}, {16777215, 4}} {
		t.Run(fmt.Sprint(tc.length), func(t *testing.T) {
			data := bytes.Repeat([]byte{42}, tc.length)
			out, err := makeIndex([][]byte{data})
			if err != nil {
				t.Fatal(err)
			}
			if int(out[2]) != tc.size {
				t.Fatalf("offset size=%d want %d", out[2], tc.size)
			}
			objects, end := readIndex(t, out, 0)
			if end != len(out) || len(objects) != 1 || !bytes.Equal(objects[0], data) {
				t.Fatal("INDEX round trip failed")
			}
		})
	}
	empty, err := makeIndex(nil)
	if err != nil || !bytes.Equal(empty, []byte{0, 0}) {
		t.Fatal("empty INDEX must be two zero bytes")
	}
	if _, err := makeIndex(make([][]byte, 65536)); err == nil {
		t.Fatal("accepted excess INDEX count")
	}
}

func TestTableNameAndStringIndexes(t *testing.T) {
	empty, _ := Encode(nil, 500, false)
	for _, count := range []int{1, 2, 300, 64610} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			glyphs := make([][]byte, count)
			for i := range glyphs {
				glyphs[i] = empty
			}
			out, err := Table("SyntheticCFF", glyphs, [4]int16{-1, -2, 3, 4}, 2048)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out[:4], []byte{1, 0, 4, 4}) {
				t.Fatal("bad CFF1 header")
			}
			names, pos := readIndex(t, out, 4)
			if len(names) != 1 || string(names[0]) != "SyntheticCFF" {
				t.Fatal("bad Name INDEX")
			}
			top, pos := readIndex(t, out, pos)
			if len(top) != 1 {
				t.Fatal("bad Top DICT INDEX")
			}
			strings, pos := readIndex(t, out, pos)
			if len(strings) != count-1 {
				t.Fatal("bad String INDEX count")
			}
			for i, s := range strings {
				if string(s) != fmt.Sprintf("gid%d", i+1) {
					t.Fatal("bad generated GID name")
				}
			}
			global, _ := readIndex(t, out, pos)
			if len(global) != 0 {
				t.Fatal("unexpected global subroutines")
			}
			if !bytes.Equal(glyphs[0], empty) {
				t.Fatal("changed caller charstring")
			}
		})
	}
}

func TestTableRejectsInvalidInput(t *testing.T) {
	valid := [][]byte{{139, 14}}
	for _, name := range []string{"", "Name With Spaces", "Bad/Name", "Bad[Name", "Bad\x00Name", "Bad\x80Name", string(bytes.Repeat([]byte{'A'}, 128))} {
		if _, err := Table(name, valid, [4]int16{}, 1000); err == nil {
			t.Fatalf("accepted name %q", name)
		}
	}
	for _, cs := range [][][]byte{nil, {nil}, {{139}}, {make([]byte, 65536)}, make([][]byte, 64611)} {
		if _, err := Table("GoodName", cs, [4]int16{}, 1000); err == nil {
			t.Fatal("accepted invalid charstrings")
		}
	}
	if _, err := Table("GoodName", valid, [4]int16{}, 0); err == nil {
		t.Fatal("accepted zero UPM")
	}
	if _, err := Table("GoodName", valid, [4]int16{1, 0, 0, 0}, 1000); err == nil {
		t.Fatal("accepted reversed bbox")
	}
}

func BenchmarkTable4096(b *testing.B) {
	cs, err := Encode(benchmarkPath(), 600, true)
	if err != nil {
		b.Fatal(err)
	}
	glyphs := make([][]byte, 4096)
	for i := range glyphs {
		glyphs[i] = cs
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Table("SyntheticCFF", glyphs, [4]int16{-30, -100, 450, 101}, 1000); err != nil {
			b.Fatal(err)
		}
	}
}
