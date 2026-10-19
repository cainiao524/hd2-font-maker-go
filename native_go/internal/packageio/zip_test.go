package packageio

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestZIPStreamingParallelRoundTrip(t *testing.T) {
	folder := t.TempDir()
	source := filepath.Join(folder, "large.bin")
	large := bytes.Repeat([]byte{0, 1, 2, 127, 128, 255}, 400000)
	if err := os.WriteFile(source, large, 0600); err != nil {
		t.Fatal(err)
	}
	files := []File{
		{Name: "Fonts/\u5927\u5b57\u4f53.bin", Path: source},
		{Name: "empty.stream"},
		{Name: "\u8bf4\u660e.txt", Data: []byte("\u7b80\u7e41\u570b\u56fd")},
		{Name: "data/patch", Data: bytes.Repeat([]byte("patch"), 60000)},
		{Name: "data/patch.gpu_resources", Data: []byte("gpu")},
	}
	expected := [][]byte{large, {}, files[2].Data, files[3].Data, files[4].Data}
	work := filepath.Join(folder, "work")
	var firstZIP []byte
	var firstReport Report
	for _, workers := range []int{1, 2, 999} {
		target := filepath.Join(folder, fmt.Sprintf("workers-%d.zip", workers))
		var progress []Progress
		report, err := WriteZIP(context.Background(), files, target, Options{
			Level: flate.BestSpeed, Workers: workers, WorkDir: work,
			Progress: func(p Progress) {
				progress = append(progress, p)
				spools, err := filepath.Glob(filepath.Join(work, "hd2-zip-*", "member-*.deflate"))
				if err != nil || len(spools) > 1 {
					t.Errorf("worker window exceeded: %d spools, %v", len(spools), err)
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if report.Size != int64(len(data)) || report.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
			t.Fatal("ZIP hash or size differs")
		}
		reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		if len(reader.File) != len(files) || len(progress) != len(files) {
			t.Fatal("member or progress count differs")
		}
		for i, member := range reader.File {
			if member.Name != files[i].Name || member.Method != zip.Deflate || member.NonUTF8 || member.Flags&0x800 == 0 {
				t.Errorf("member %d lost order, Unicode name or DEFLATE method", i)
			}
			if p := progress[i]; p.Name != files[i].Name || p.Done != i+1 || p.Total != len(files) {
				t.Errorf("progress %d: %+v", i, p)
			}
			stream, err := member.Open()
			if err != nil {
				t.Fatal(err)
			}
			payload, err := io.ReadAll(stream)
			_ = stream.Close()
			if err != nil || !bytes.Equal(payload, expected[i]) {
				t.Fatalf("member %d payload changed: %v", i, err)
			}
			info := report.Members[i]
			if info.SHA256 != fmt.Sprintf("%x", sha256.Sum256(payload)) || info.CRC32 != crc32.ChecksumIEEE(payload) || info.Size != uint64(len(payload)) {
				t.Errorf("member %d digest differs", i)
			}
		}
		if firstZIP == nil {
			firstZIP, firstReport = data, report
		} else if !bytes.Equal(data, firstZIP) || !reflect.DeepEqual(report, firstReport) {
			t.Fatal("worker scheduling changed ZIP bytes or report")
		}
		entries, err := os.ReadDir(work)
		if err != nil || len(entries) != 0 {
			t.Fatalf("temporary files remain: %v, %v", entries, err)
		}
	}
}

func TestEmptyZIPAndCompressionLevels(t *testing.T) {
	for _, level := range []int{flate.HuffmanOnly, flate.DefaultCompression, flate.NoCompression, flate.BestCompression} {
		t.Run(fmt.Sprint(level), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "empty.zip")
			report, err := WriteZIP(context.Background(), nil, path, Options{Level: level})
			if err != nil || len(report.Members) != 0 || report.Size != 22 {
				t.Fatalf("empty ZIP: %+v, %v", report, err)
			}
			path = filepath.Join(t.TempDir(), "data.zip")
			if _, err := WriteZIP(context.Background(), []File{{Name: "data", Data: []byte("aaaa")}}, path, Options{Level: level}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestZIPReadbackDetectsCorruptionAndMismatch(t *testing.T) {
	folder := t.TempDir()
	path := filepath.Join(folder, "test.zip")
	report, err := WriteZIP(context.Background(), []File{{Name: "data", Data: []byte("preserve these bytes")}}, path, Options{Level: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := VerifyZIP(context.Background(), path, nil); err != nil || !reflect.DeepEqual(got, report) {
		t.Fatalf("independent readback: %v", err)
	}
	wrong := append([]MemberInfo(nil), report.Members...)
	wrong[0].SHA256 = "incorrect"
	if _, err := VerifyZIP(context.Background(), path, wrong); err == nil {
		t.Error("accepted SHA mismatch")
	}
	if _, err := VerifyZIP(context.Background(), path, []MemberInfo{}); err == nil {
		t.Error("accepted member count mismatch")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	central := bytes.Index(data, []byte("PK\x01\x02"))
	if central < 0 {
		t.Fatal("no central directory")
	}
	data[central+16] ^= 1
	corrupt := filepath.Join(folder, "corrupt.zip")
	if err := os.WriteFile(corrupt, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyZIP(context.Background(), corrupt, nil); !errors.Is(err, zip.ErrChecksum) {
		t.Fatalf("CRC corruption: %v", err)
	}
	if err := os.WriteFile(corrupt, data[:len(data)-10], 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyZIP(context.Background(), corrupt, nil); err == nil {
		t.Error("accepted truncated ZIP")
	}
}

func TestZIPCancellationAndFailedSourceCleanup(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelEarly), func(t *testing.T) {
			folder := t.TempDir()
			target, work := filepath.Join(folder, "test.zip"), filepath.Join(folder, "work")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelEarly {
				cancel()
			}
			files := []File{{Name: "one", Data: bytes.Repeat([]byte("one"), 100000)}, {Name: "two", Data: bytes.Repeat([]byte("two"), 100000)}, {Name: "three"}}
			_, err := WriteZIP(ctx, files, target, Options{Level: 1, WorkDir: work, Progress: func(Progress) { cancel() }})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation: %v", err)
			}
			if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial target remains: %v", err)
			}
			entries, err := os.ReadDir(work)
			if err != nil && !errors.Is(err, os.ErrNotExist) || len(entries) != 0 {
				t.Fatalf("spools remain: %v, %v", entries, err)
			}
		})
	}
	folder := t.TempDir()
	work, target := filepath.Join(folder, "work"), filepath.Join(folder, "test.zip")
	_, err := WriteZIP(context.Background(), []File{{Name: "missing", Path: filepath.Join(folder, "absent")}, {Name: "other", Data: []byte("other")}}, target, Options{Level: 1, WorkDir: work})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing source: %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial target remains: %v", err)
	}
	entries, err := os.ReadDir(work)
	if err != nil || len(entries) != 0 {
		t.Fatalf("spools remain: %v, %v", entries, err)
	}
}

func TestZIPInputValidationAndExistingOutput(t *testing.T) {
	for _, files := range [][]File{
		{{Name: "../escape"}}, {{Name: "/absolute"}}, {{Name: "C:/drive"}}, {{Name: "back\\slash"}}, {{Name: ""}}, {{Name: "directory/"}}, {{Name: "."}}, {{Name: "a\x00b"}}, {{Name: string([]byte{0xff})}},
		{{Name: "same"}, {Name: "same"}},
		{{Name: "ambiguous", Path: "source", Data: []byte{}}},
	} {
		if err := validateInputs(files, Options{Level: 1}); err == nil {
			t.Errorf("accepted invalid inputs: %+v", files)
		}
	}
	for _, level := range []int{-3, 10} {
		if err := validateInputs(nil, Options{Level: level}); err == nil {
			t.Errorf("accepted level %d", level)
		}
	}
	path := filepath.Join(t.TempDir(), "old-release.zip")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteZIP(context.Background(), nil, path, Options{Level: 1}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing target: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing target modified: %q, %v", data, err)
	}
}

func TestZIP64RawHeader(t *testing.T) {
	// Exercise the standard writer's ZIP64 metadata without allocating 4 GiB.
	// This is deliberately metadata-only; it is not a valid payload fixture.
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	size := uint64(math.MaxUint32) + 1
	if _, err := writer.CreateRaw(memberHeader(MemberInfo{Name: "large", Size: size, CompressedSize: size + 7})); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := buffer.Bytes()
	if binary.LittleEndian.Uint16(data[4:]) != 45 || binary.LittleEndian.Uint32(data[18:]) != math.MaxUint32 || binary.LittleEndian.Uint32(data[22:]) != math.MaxUint32 {
		t.Fatal("ZIP64 local header is missing")
	}
	nameLength := int(binary.LittleEndian.Uint16(data[26:]))
	extra := data[30+nameLength:]
	if binary.LittleEndian.Uint16(extra) != 1 || binary.LittleEndian.Uint64(extra[4:]) != size || binary.LittleEndian.Uint64(extra[12:]) != size+7 {
		t.Fatal("ZIP64 local sizes differ")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(reader.File) != 1 {
		t.Fatalf("ZIP64 central directory: %v", err)
	}
	if member := reader.File[0]; member.UncompressedSize64 != size || member.CompressedSize64 != size+7 {
		t.Fatal("ZIP64 central sizes differ")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestZIPOutputFailure(t *testing.T) {
	_, err := assemble(context.Background(), []File{{Name: "data", Data: []byte("data")}}, failingWriter{}, t.TempDir(), Options{Level: 1})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("output failure: %v", err)
	}
}
