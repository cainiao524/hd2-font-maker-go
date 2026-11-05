package pipeline

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"hd2font/native/internal/archiveio"
	"hd2font/native/internal/atlas"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/sfntio"
)

func zipContents(t *testing.T, path string) map[string][]byte {
	t.Helper()
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	out := make(map[string][]byte)
	for _, member := range z.File {
		r, err := member.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[member.Name] = data
	}
	return out
}
func savedResource(t *testing.T, files map[string][]byte, archive string, id uint64) (archiveio.Entry, []byte, []byte, []byte) {
	t.Helper()
	name := "Fonts/" + archive + ".patch_0"
	main, stream, gpu := files[name], files[name+".stream"], files[name+".gpu_resources"]
	r, err := archiveio.NewReader(bytes.NewReader(main), uint64(len(main)))
	if err != nil {
		t.Fatal(err)
	}
	index, err := archiveio.ReadIndex(r)
	if err != nil {
		t.Fatal(err)
	}
	if err = index.ValidateBounds([3]uint64{uint64(len(main)), uint64(len(stream)), uint64(len(gpu))}); err != nil {
		t.Fatal(err)
	}
	for _, e := range index.Entries {
		if e.NameID == id {
			return e, main[e.MainOffset : e.MainOffset+uint64(e.MainSize)], stream[e.StreamOffset : e.StreamOffset+uint64(e.StreamSize)], gpu[e.GPUOffset : e.GPUOffset+uint64(e.GPUSize)]
		}
	}
	t.Fatalf("missing resource %s/%d", archive, id)
	return archiveio.Entry{}, nil, nil, nil
}

func TestCompleteBuildScopesSlotsCoverageAndAtlases(t *testing.T) {
	inv, options := pipelineFixture(t)
	ops := &fakeOps{padRegistration: true}
	generator := &fakeAtlas{}
	report, err := build(context.Background(), inv, ops, options, nil, generator.generate)
	if err != nil {
		t.Fatal(err)
	}
	if !report.FullReplacement || report.KeepRuntimeFonts || !report.NoesisFamilyStylePreserved || !report.OfflineReadbackVerified || report.RuntimeVerified || report.Installed {
		t.Fatalf("incorrect status report: %+v", report)
	}
	if report.ZIPSHA256 == "" || report.ZIPBytes == 0 || report.Output != options.OutputZIP {
		t.Fatal("missing saved output identity")
	}
	if len(report.Atlases) != 3 || len(generator.calls) != 2 {
		t.Fatalf("atlas dedupe: %d uses/%d jobs", len(report.Atlases), len(generator.calls))
	}
	if len(ops.styles) != 1 || ops.styles[0].weight != 600 {
		t.Fatalf("single regular should only embolden 700 as Style(600): %+v", ops.styles)
	}
	for _, style := range ops.styles {
		if !style.pixel || style.outlines {
			t.Fatal("raw 256 UPM pixel-grid or outline flag lost")
		}
	}
	methods := map[int]string{}
	for _, row := range report.StyleAdaptation {
		methods[row.TargetWeight] = row.Method
	}
	if methods[500] != "authored_static" || methods[700] != "synthetic_emphasis" {
		t.Fatalf("single-font rows: %v", methods)
	}
	files := zipContents(t, options.OutputZIP)
	for name := range files {
		if strings.Contains(name, "c0b7644cc5c4aaa8") {
			t.Fatal("unselected TC resources emitted")
		}
	}
	for _, name := range []string{"manifest.json", "font-build-report.json", "README.txt", "使用说明.txt"} {
		if len(files[name]) == 0 {
			t.Fatalf("missing %s", name)
		}
	}
	var manifest struct {
		Version int
		Guid    string
		Name    string
		Options []struct{ Include []string }
	}
	if err = json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 1 || manifest.Guid != manifestGUID(options.Name) || !reflect.DeepEqual(manifest.Options[0].Include, []string{"Fonts"}) {
		t.Fatal("invalid manager manifest")
	}
	_, runtime, _, _ := savedResource(t, files, "0123456789abcdef", 9)
	parts, err := archiveio.RuntimeParts(runtime)
	if err != nil {
		t.Fatal(err)
	}
	var originalRuntime *inventory.Resource
	for _, r := range inv.Resources {
		if r.Kind == "runtime_font" {
			originalRuntime = r
		}
	}
	old, err := archiveio.RuntimeParts(originalRuntime.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 4 || !bytes.Equal(parts[3].Data, old[3].Data) || parts[3].Metrics != old[3].Metrics {
		t.Fatal("runtime slot layout or outside-scope TC font changed")
	}
	if !bytes.Equal(parts[1].Data, parts[2].Data) {
		t.Fatal("repeated selected runtime slots differ")
	}
	for i := 0; i < 3; i++ {
		face, err := inventory.InspectFace(parts[i].Data)
		if err != nil {
			t.Fatal(err)
		}
		for _, cp := range []rune{0x4e8c, 0x1f600} {
			if _, ok := face.Chars[cp]; !ok {
				t.Fatalf("slot %d lost full source U+%X", i, cp)
			}
		}
		if i > 0 {
			for _, cp := range []rune{0x4e09, 0x570b, 0x5708} {
				if _, ok := face.Chars[cp]; !ok {
					t.Fatalf("SC slot %d lost its SC/TC fallback U+%X", i, cp)
				}
			}
		} else {
			for _, cp := range []rune{0x4e09, 0x570b, 0x5708} {
				if _, ok := face.Chars[cp]; ok {
					t.Fatalf("Latin slot received unrelated SC/TC fallback U+%X", cp)
				}
			}
		}
	}
	if len(report.RuntimeSlots) != 1 || !reflect.DeepEqual(report.RuntimeSlots[0].ReplacedSlots, []int{0, 1, 2}) || report.RuntimeSlots[0].RetainedSelectedOriginalSlots != 0 {
		t.Fatal("incorrect complete replacement slot report")
	}
	for _, archive := range []string{"0de4b81e19a79aa8", "41c8eff8188e4862", "09caa46bc556e38f"} {
		_, payload, _, _ := savedResource(t, files, archive, 5)
		keys, err := atlas.GlyphKeys(payload)
		if err != nil {
			t.Fatal(err)
		}
		set := make(map[uint32]bool)
		for _, cp := range keys {
			set[cp] = true
		}
		for _, cp := range []uint32{0x4e2d, 0x570b} {
			if !set[cp] {
				t.Fatal("actual Chinese UI key missing", archive, cp, keys)
			}
		}
		for _, cp := range []uint32{0x4e8c, 0x4e09, 0x5708, 0x1f600} {
			if set[cp] {
				t.Fatalf("atlas %s fabricated full-font key U+%04X", archive, cp)
			}
		}
		e, _, stream, gpu := savedResource(t, files, archive, 6)
		if e.GPUSize != 20 || len(gpu) != 20 || len(stream) != 0 {
			t.Fatal("missing legacy GPU was not replaced with new complete mip chain")
		}
	}
	for name, info := range report.Files {
		if int64(len(files[name])) != info.Bytes || inventory.Digest(files[name]) != info.SHA256 {
			t.Fatal("report member readback differs", name)
		}
	}
}

func TestUniformAndNativeVariableWeightStrategies(t *testing.T) {
	for _, mode := range []string{"uniform", "variable"} {
		t.Run(mode, func(t *testing.T) {
			inv, options := pipelineFixture(t)
			ops := &fakeOps{}
			generator := &fakeAtlas{}
			if mode == "uniform" {
				options.MatchWeights = false
			} else {
				options.Axes = map[string]float32{"wght": 650, "wdth": 90}
				ops.axes = []axis{{"wght", 100, 400, 800}, {"wdth", 50, 100, 150}}
			}
			report, err := build(context.Background(), inv, ops, options, nil, generator.generate)
			if err != nil {
				t.Fatal(err)
			}
			if len(ops.styles) != 0 {
				t.Fatal("synthetic emphasis used for uniform/native variable mode")
			}
			if mode == "uniform" {
				if ops.merges != 1 || ops.conversions != 1 || report.Performance["unique_styles"] != 1 {
					t.Fatal("uniform jobs were not deduplicated", report.Performance)
				}
			} else {
				values := map[float32]bool{}
				for _, axes := range ops.preparations {
					values[axes["wght"]] = true
					if axes["wdth"] != 90 {
						t.Fatal("unrelated variation axis lost")
					}
				}
				if len(ops.preparations) != 3 || !values[650] || !values[750] || !values[800] {
					t.Fatal("wrong native wght default/offset/clamp", ops.preparations)
				}
			}
		})
	}
}

func TestMissingCmapAndCancellationPreserveOutput(t *testing.T) {
	inv, options := pipelineFixture(t)
	old := []byte("previous published output")
	if err := os.WriteFile(options.OutputZIP, old, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := build(context.Background(), inv, &fakeOps{drop: 0x1f600}, options, nil, (&fakeAtlas{}).generate)
	if err == nil || !strings.Contains(err.Error(), "lost") {
		t.Fatal("incomplete full source cmap accepted", err)
	}
	got, err := os.ReadFile(options.OutputZIP)
	if err != nil || !bytes.Equal(got, old) {
		t.Fatal("failed build replaced output")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = build(ctx, inv, &fakeOps{}, options, nil, (&fakeAtlas{}).generate); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}

func TestMissingNonAtlasCompanionRejected(t *testing.T) {
	inv, options := pipelineFixture(t)
	for _, r := range inv.Resources {
		if r.Kind == "runtime_font" {
			r.Entry.GPUSize = 16
			r.MissingParts = []string{"gpu"}
		}
	}
	if _, err := build(context.Background(), inv, &fakeOps{}, options, nil, (&fakeAtlas{}).generate); err == nil {
		t.Fatal("unavailable non-atlas companion fabricated")
	}
}

func styleMethods(report *Report) map[int]string {
	out := map[int]string{}
	for _, row := range report.StyleAdaptation {
		out[row.TargetWeight] = row.Method
	}
	return out
}

func styleWeights(ops *fakeOps) map[int]bool {
	out := map[int]bool{}
	for _, style := range ops.styles {
		out[style.weight] = true
	}
	return out
}

func TestSingleFontThinsRegularAndModeratesBold(t *testing.T) {
	inv, options := pipelineFixture(t)
	options.Languages = []string{"sc", "tc"}
	ops := &fakeOps{}
	report, err := build(context.Background(), inv, ops, options, nil, (&fakeAtlas{}).generate)
	if err != nil {
		t.Fatal(err)
	}
	got := styleWeights(ops)
	if got[500] || got[700] || !got[300] || !got[600] {
		t.Fatalf("regular anchor styles: %+v", ops.styles)
	}
	methods := styleMethods(report)
	if methods[400] != "synthetic_reduction" || methods[500] != "authored_static" || methods[700] != "synthetic_emphasis" {
		t.Fatalf("regular anchor methods: %v", methods)
	}

	options.WeightFaces = map[int]int{700: 0, 400: 0}
	ops = &fakeOps{}
	report, err = build(context.Background(), inv, ops, options, nil, (&fakeAtlas{}).generate)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops.styles) != 0 {
		t.Fatalf("pinned single face still synthesized: %+v", ops.styles)
	}
	methods = styleMethods(report)
	if methods[400] != "authored_static" || methods[700] != "authored_static" {
		t.Fatalf("pins ignored: %v", methods)
	}
}

func TestSingleFontMetadataStillAnchorsMedium(t *testing.T) {
	for _, declared := range []uint16{0, 300, 400, 500, 700, 900} {
		t.Run(strconv.Itoa(int(declared)), func(t *testing.T) {
			inv, options := pipelineFixture(t)
			options.Languages = []string{"sc", "tc"}
			raw, err := os.ReadFile(options.FontPath)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := sfntio.Parse(raw, 0)
			if err != nil {
				t.Fatal(err)
			}
			binary.BigEndian.PutUint16(parsed.Tables["OS/2"][4:], declared)
			rewritten, err := parsed.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(options.FontPath, rewritten, 0600); err != nil {
				t.Fatal(err)
			}
			ops := &fakeOps{}
			report, err := build(context.Background(), inv, ops, options, nil, (&fakeAtlas{}).generate)
			if err != nil {
				t.Fatal(err)
			}
			got := styleWeights(ops)
			if len(ops.styles) != 2 || !got[300] || !got[600] {
				t.Fatalf("medium anchor styles: %+v", ops.styles)
			}
			methods := styleMethods(report)
			if methods[400] != "synthetic_reduction" || methods[500] != "authored_static" || methods[700] != "synthetic_emphasis" {
				t.Fatalf("medium anchor methods: %v", methods)
			}
			for _, style := range ops.styles {
				if style.sourceWeight != declared {
					t.Fatal("a derived weight was used as the source of another weight")
				}
			}
		})
	}
}

func TestSingleFontCanBeAssignedToRegular(t *testing.T) {
	inv, options := pipelineFixture(t)
	options.Languages = []string{"sc", "tc"}
	options.SingleFontWeight = 400
	ops := &fakeOps{}
	report, err := build(context.Background(), inv, ops, options, nil, (&fakeAtlas{}).generate)
	if err != nil {
		t.Fatal(err)
	}
	methods, weights := styleMethods(report), styleWeights(ops)
	if methods[400] != "authored_static" || methods[500] != "synthetic_emphasis" || methods[700] != "synthetic_emphasis" || len(ops.styles) != 2 || !weights[500] || !weights[700] {
		t.Fatalf("regular source did not generate medium and bold: methods=%v styles=%+v", methods, ops.styles)
	}
	for _, style := range ops.styles {
		if style.sourceWeight != 400 {
			t.Fatal("medium and bold must each derive from the imported font")
		}
	}
	options.MatchWeights = false
	ops = &fakeOps{}
	if _, err := build(context.Background(), inv, ops, options, nil, (&fakeAtlas{}).generate); err != nil {
		t.Fatal(err)
	}
	if len(ops.styles) != 0 {
		t.Fatal("source assignment should not synthesize when matching is disabled")
	}
}
