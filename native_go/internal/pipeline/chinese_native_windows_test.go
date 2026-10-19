package pipeline

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"hd2font/native/internal/atlas"
	"hd2font/native/internal/fontcore"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/sfntio"
)

func TestNativeV13AtlasSourcePrecedence(t *testing.T) {
	if os.Getenv("HD2_NATIVE_PIPELINE_TEST") != "1" {
		t.Skip("set HD2_NATIVE_PIPELINE_TEST=1 for native glyph integration")
	}
	bin, err := filepath.Abs(filepath.Join("..", "..", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	processor, err := fontcore.NewProcessor(bin, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer processor.Close()

	inv, options := v13ScopeFixture(t)
	source, err := syntheticFont("Synthetic Pixel", 400, 1000, []rune{'?', 'A', 0x4e2d, 0x4e8c, 0x1f600})
	if err != nil {
		t.Fatal(err)
	}
	font, err := sfntio.Parse(source, 0)
	if err != nil {
		t.Fatal(err)
	}
	face, err := inventory.InspectFace(source)
	if err != nil {
		t.Fatal(err)
	}
	// U+4E2D deliberately differs from the original .5em fallback. The native
	// 1.3 path keeps the authored advance instead of applying extra 1.5 spacing.
	binary.BigEndian.PutUint16(font.Tables["hmtx"][4*int(face.Cmap[0x4e2d]):], 750)
	source, err = font.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(options.FontPath, source, 0600); err != nil {
		t.Fatal(err)
	}
	options.Languages = []string{"sc"}
	options.MatchWeights = false
	options.ProcessOutlines = false
	options.AtlasExecutable = filepath.Join(bin, "..", "..", "vendor", "msdf-atlas-gen", "msdf-atlas-gen.exe")

	var scSources []atlas.Source
	var customAdvance int
	wrapped := func(ctx context.Context, sources []atlas.Source, opts atlas.Options) (*atlas.Result, error) {
		if slices.Contains(opts.Keys, rune(0x4e2d)) {
			scSources = append([]atlas.Source(nil), sources...)
			if len(sources) != 0 && sources[0].Custom {
				data, err := os.ReadFile(sources[0].FontPath)
				if err != nil {
					return nil, err
				}
				face, err := inventory.InspectFace(data)
				if err != nil {
					return nil, err
				}
				customAdvance = int(face.Advances[0x4e2d])
			}
		}
		return atlas.Generate(ctx, sources, opts)
	}
	report, err := build(context.Background(), inv, &nativeOps{Processor: processor}, options, nil, wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if !coverageByGroup(report)["sc"].Selected {
		t.Fatal("SC group was not selected")
	}
	if len(scSources) == 0 || !scSources[0].Custom {
		t.Fatal("SC atlas did not request the styled custom face")
	}
	if customAdvance != 750 {
		t.Fatalf("custom SC atlas advance = %d, want authored 750", customAdvance)
	}
	for _, source := range scSources[1:] {
		if slices.Contains(source.Charset, rune(0x4e2d)) {
			t.Fatal("original fallback shadowed the overlapping custom U+4E2D glyph")
		}
	}
	files := zipContents(t, options.OutputZIP)
	for _, archive := range []string{"0de4b81e19a79aa8", "41c8eff8188e4862", "09caa46bc556e38f"} {
		_, payload, _, _ := savedResource(t, files, archive, 4)
		out, err := inventory.InspectFace(payload[16:])
		if err != nil {
			t.Fatal(err)
		}
		if out.Advances[0x4e2d] != 750 {
			t.Errorf("%s lost authored U+4E2D advance: %d", archive, out.Advances[0x4e2d])
		}
	}
	for archive := range inventory.AtlasGroups() {
		group, _ := inventory.AtlasGroup(archive)
		if group != "latin" && group != "sc" {
			if _, ok := files["Fonts/"+archive+".patch_0"]; ok {
				t.Errorf("unselected atlas archive %s emitted", archive)
			}
		}
	}
	if report.EngineProfile != "python-1.3.7-parity-v1" {
		t.Fatal("native build used the wrong engine profile")
	}
}
