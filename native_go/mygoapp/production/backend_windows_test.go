//go:build windows && amd64

package production

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font/gofont/goregular"
	"hd2font/native/internal/archiveio"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/pipeline"
	"hd2font/native/mygoapp"
)

func testBackend(t *testing.T) *Backend {
	t.Helper()
	binDir, err := filepath.Abs("../../bin")
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(binDir, filepath.Join(t.TempDir(), "UserData"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func fontFile(t *testing.T, data []byte, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func request(t *testing.T) mygoapp.BuildRequest {
	t.Helper()
	return mygoapp.BuildRequest{GameFolder: filepath.Join(t.TempDir(), "game"), Font: mygoapp.FontSelection{Path: fontFile(t, goregular.TTF, "source.ttf"), Axes: map[string]float32{"wght": 520}}, Languages: []string{"sc", "tc"}, MatchWeights: false, ProcessOutlines: true, IncludeSymbols: true, OutputZIP: filepath.Join(t.TempDir(), "font.zip")}
}

func TestImportPreservesCompleteOriginal(t *testing.T) {
	b := testBackend(t)
	source := fontFile(t, goregular.TTF, "Original.ttf")
	info, err := b.InspectFont(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if info.Path == source || len(info.Faces) != 1 {
		t.Fatalf("unexpected import: %+v", info)
	}
	want := filepath.Join(b.userData, "Fonts", digest(goregular.TTF), "Original.ttf")
	if info.Path != want {
		t.Fatalf("nonportable import path %s", info.Path)
	}
	for _, path := range []string{source, info.Path} {
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, goregular.TTF) {
			t.Fatalf("changed bytes: %s, %v", path, err)
		}
	}
	again, err := b.InspectFont(context.Background(), info.Path)
	if err != nil || again.Path != info.Path {
		t.Fatalf("repeat import: %v", err)
	}
	data := append([]byte(nil), goregular.TTF...)
	data[len(data)-1] ^= 1
	if err := os.WriteFile(info.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyStoredSource(b.userData, info.Path, data); err == nil {
		t.Fatal("modified library font accepted")
	}
}

func TestPureGoInspectCollectionsAndCFF(t *testing.T) {
	for _, name := range []string{"collection.ttc", "cid.otf", "variable-cff2.otf", "two-axis.ttf"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("../../internal/harfbuzz/testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			info, err := inspectData(context.Background(), data, name)
			if err != nil {
				t.Fatal(err)
			}
			if len(info.Faces) == 0 || info.Faces[0].Name == "" {
				t.Fatal("no face metadata")
			}
			if name == "collection.ttc" && len(info.Faces) < 2 {
				t.Fatal("collection faces lost")
			}
			if name == "two-axis.ttf" && len(info.Faces[0].Axes) != 2 {
				t.Fatalf("lost axes: %+v", info.Faces[0])
			}
		})
	}
}

func TestGeneratePassesFullSourceAndOptions(t *testing.T) {
	b := testBackend(t)
	req := request(t)
	inv := &inventory.Inventory{}
	b.deps.scan = func(ctx context.Context, path string, progress func(string)) (*inventory.Inventory, error) {
		if path != req.GameFolder {
			t.Error("wrong game path")
		}
		progress("Reading font index 10/20")
		return inv, ctx.Err()
	}
	b.deps.build = func(ctx context.Context, got *inventory.Inventory, opts pipeline.Options, progress func(string)) (*pipeline.Report, error) {
		if got != inv {
			t.Error("inventory replaced")
		}
		full, err := os.ReadFile(opts.FontPath)
		if err != nil || !bytes.Equal(full, goregular.TTF) {
			t.Fatal("build did not receive complete original font")
		}
		if opts.FaceIndex != req.Font.FaceIndex || !reflect.DeepEqual(opts.Axes, req.Font.Axes) || !slices.Equal(opts.Languages, req.Languages) || opts.MatchWeights || !opts.ProcessOutlines || !opts.IncludeSymbols {
			t.Fatalf("request mismatch: %+v", opts)
		}
		if opts.BinDir != b.binDir || opts.WorkDir != filepath.Join(b.userData, "Work") || opts.AtlasExecutable != filepath.Join(b.binDir, "vendor", "msdf-atlas-gen", "msdf-atlas-gen.exe") {
			t.Fatal("runtime paths are not portable")
		}
		opts.Axes["wght"] = 700
		opts.Languages[0] = "latin"
		progress("Preparing font weights 2/3")
		if err := os.WriteFile(opts.OutputZIP, []byte("mock-only package"), 0600); err != nil {
			return nil, err
		}
		return &pipeline.Report{Output: opts.OutputZIP, OfflineReadbackVerified: true, Selections: []pipeline.SelectionReport{{Group: "sc"}, {Group: "tc"}, {Group: "sc"}}}, nil
	}
	var updates []mygoapp.Progress
	result, err := b.Generate(context.Background(), req, func(p mygoapp.Progress) { updates = append(updates, p) })
	if err != nil {
		t.Fatal(err)
	}
	if req.Font.Axes["wght"] != 520 || req.Languages[0] != "sc" {
		t.Fatal("adapter changed caller request")
	}
	if result.OutputZIP != req.OutputZIP || !slices.Equal(result.ResourceGroups, []string{"sc", "tc"}) || result.Cached {
		t.Fatalf("result mismatch: %+v", result)
	}
	if !slices.ContainsFunc(updates, func(p mygoapp.Progress) bool { return p.Stage == "weights" && p.Completed == 2 && p.Total == 3 }) {
		t.Fatal("phase counts not forwarded")
	}
}

func TestCancelledJobDoesNotPoisonNextBuild(t *testing.T) {
	b := testBackend(t)
	req := request(t)
	b.deps.scan = func(context.Context, string, func(string)) (*inventory.Inventory, error) {
		return &inventory.Inventory{}, nil
	}
	started := make(chan struct{})
	b.deps.build = func(ctx context.Context, _ *inventory.Inventory, _ pipeline.Options, _ func(string)) (*pipeline.Report, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := b.Generate(ctx, req, nil); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("build did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel returned %v", err)
	}
	b.deps.build = func(_ context.Context, _ *inventory.Inventory, opts pipeline.Options, _ func(string)) (*pipeline.Report, error) {
		if err := os.WriteFile(opts.OutputZIP, []byte("second independent job"), 0600); err != nil {
			return nil, err
		}
		return &pipeline.Report{Output: opts.OutputZIP, OfflineReadbackVerified: true}, nil
	}
	if _, err := b.Generate(context.Background(), req, nil); err != nil {
		t.Fatalf("later job failed: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Generate(context.Background(), req, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed backend: %v", err)
	}
}

func TestRejectGameWritesAndUnverifiedOutput(t *testing.T) {
	b := testBackend(t)
	req := request(t)
	b.deps.scan = func(context.Context, string, func(string)) (*inventory.Inventory, error) {
		return &inventory.Inventory{}, nil
	}
	b.deps.build = func(context.Context, *inventory.Inventory, pipeline.Options, func(string)) (*pipeline.Report, error) {
		return &pipeline.Report{Output: req.OutputZIP}, nil
	}
	if _, err := b.Generate(context.Background(), req, nil); err == nil || !strings.Contains(err.Error(), "verify") {
		t.Fatalf("unverified output accepted: %v", err)
	}
	req.OutputZIP = filepath.Join(req.GameFolder, "font.zip")
	if _, err := b.Generate(context.Background(), req, nil); err == nil {
		t.Fatal("game output accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanInventory(ctx, "unused", func(string) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel scan: %v", err)
	}
}

func TestRealPreviewPerJobAndCleanup(t *testing.T) {
	b := testBackend(t)
	if _, err := os.Stat(filepath.Join(b.binDir, "hd2harfbuzz.dll")); err != nil {
		t.Skip("native DLLs not built")
	}
	path := fontFile(t, goregular.TTF, "preview.ttf")
	req := mygoapp.PreviewRequest{Font: mygoapp.FontSelection{Path: path}, Text: "Super Earth! 0123456789", Width: 800, Height: 240, ProcessOutlines: false}
	regular, err := b.Preview(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.MatchWeights = true
	bold, err := b.Preview(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var p1, p2 bytes.Buffer
	if err := png.Encode(&p1, regular.Image); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&p2, bold.Image); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(p1.Bytes(), p2.Bytes()) {
		t.Fatal("native emphasis did not change preview")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Preview(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled preview accepted")
	}
	if _, err := b.Preview(context.Background(), req); err != nil {
		t.Fatalf("subsequent preview failed: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(b.userData, "Work"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("preview font files leaked")
	}
}

func TestNativePipelineWithSyntheticInventory(t *testing.T) {
	b := testBackend(t)
	if _, err := os.Stat(filepath.Join(b.binDir, "hd2harfbuzz.dll")); err != nil {
		t.Skip("native DLLs not built")
	}
	face, err := inventory.InspectFace(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face.Group = "latin"
	payload := make([]byte, 16+len(face.Data))
	binary.LittleEndian.PutUint32(payload, uint32(len(face.Data)))
	copy(payload[16:], face.Data)
	typeID := archiveio.ResourceHash("ttf")
	resource := &inventory.Resource{Archive: "123456789abcdef0", Kind: "ttf", Entry: archiveio.Entry{NameID: 17, TypeID: typeID}, TypeRecord: archiveio.TypeRecord{TypeID: typeID}, Payload: payload, Faces: []*inventory.Face{face}}
	inv := &inventory.Inventory{Faces: map[string]*inventory.Face{face.Digest: face}, Groups: map[string][]*inventory.Face{"latin": {face}}, FaceOrder: []string{face.Digest}, Resources: []*inventory.Resource{resource}}
	b.deps.scan = func(context.Context, string, func(string)) (*inventory.Inventory, error) { return inv, nil }
	req := request(t)
	req.Font.Axes = nil
	req.Languages = []string{"latin"}
	req.ProcessOutlines = false
	req.IncludeSymbols = false
	result, err := b.Generate(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes <= 0 || !slices.Equal(result.ResourceGroups, []string{"latin"}) {
		t.Fatalf("bad native pipeline result: %+v", result)
	}
	reader, err := zip.OpenReader(result.OutputZIP)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if len(reader.File) < 3 {
		t.Fatal("native pipeline did not package patch and metadata")
	}
	for _, entry := range reader.File {
		f, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, f)
		_ = f.Close()
		if err != nil {
			t.Fatalf("ZIP readback %s: %v", entry.Name, err)
		}
	}
}

func TestPreviewNativeFontFormatsAndIndependentFlags(t *testing.T) {
	b := testBackend(t)
	if _, err := os.Stat(filepath.Join(b.binDir, "hd2harfbuzz.dll")); err != nil {
		t.Skip("native DLLs not built")
	}
	for _, name := range []string{"collection.ttc", "cid.otf", "variable-cff2.otf", "two-axis.ttf"} {
		t.Run(name, func(t *testing.T) {
			path, err := filepath.Abs(filepath.Join("../../internal/harfbuzz/testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			for _, flags := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
				req := mygoapp.PreviewRequest{Font: mygoapp.FontSelection{Path: path}, Text: "A?", Width: 400, Height: 180, MatchWeights: flags[0], ProcessOutlines: flags[1]}
				result, err := b.Preview(context.Background(), req)
				if err != nil {
					t.Fatalf("weights=%v outlines=%v: %v", flags[0], flags[1], err)
				}
				if result.Image == nil {
					t.Fatal("empty native preview")
				}
			}
		})
	}
}
