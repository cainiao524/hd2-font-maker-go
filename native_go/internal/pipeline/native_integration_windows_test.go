package pipeline

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"hd2font/native/internal/atlas"
	"hd2font/native/internal/fontcore"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/sfntio"
)

// Opt-in native integration uses authored synthetic outlines only. It is kept
// separate from the parent's full saved-inventory performance benchmark.
func TestNativeSyntheticBuild(t *testing.T) {
	if os.Getenv("HD2_NATIVE_PIPELINE_TEST") != "1" {
		t.Skip("set HD2_NATIVE_PIPELINE_TEST=1 after native processor validation")
	}
	bin, err := filepath.Abs(filepath.Join("..", "..", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	for _, outlines := range []bool{false, true} {
		name := "original-outlines"
		if outlines {
			name = "processed-outlines"
		}
		t.Run(name, func(t *testing.T) {
			inv, options := pipelineFixture(t)
			options.BinDir = bin
			options.ProcessOutlines = outlines
			options.AtlasExecutable = filepath.Join(bin, "..", "..", "vendor", "msdf-atlas-gen", "msdf-atlas-gen.exe")
			processor, err := fontcore.NewProcessor(bin, 2)
			if err != nil {
				t.Fatal(err)
			}
			defer processor.Close()
			report, err := Build(context.Background(), inv, processor, options, func(message string) { t.Log(message) })
			if err != nil {
				t.Fatal(err)
			}
			if !report.FullReplacement || !report.OfflineReadbackVerified || len(report.Atlases) != 3 || report.ZIPSHA256 == "" {
				t.Fatal("native build report is incomplete")
			}
		})
	}
}

func TestNativeFamilyAtlasUsesSiblingGlyphs(t *testing.T) {
	if os.Getenv("HD2_NATIVE_PIPELINE_TEST") != "1" {
		t.Skip("set HD2_NATIVE_PIPELINE_TEST=1 for native family atlas integration")
	}
	bin, err := filepath.Abs(filepath.Join("..", "..", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	for _, outlines := range []bool{false, true} {
		t.Run(fmt.Sprintf("outlines=%v", outlines), func(t *testing.T) {
			inv, _ := pipelineFixture(t)
			opts := familyFixture(t)
			// Only the Bold sibling draws U+570B. Its authored advance (0.75 em)
			// differs from the original game's 0.5 em, so source errors are visible.
			raw, err := os.ReadFile(opts.Family[0].Path)
			if err != nil {
				t.Fatal(err)
			}
			f, err := sfntio.Parse(raw, 0)
			if err != nil {
				t.Fatal(err)
			}
			upm := binary.BigEndian.Uint16(f.Tables["head"][18:])
			count := binary.BigEndian.Uint16(f.Tables["hhea"][34:])
			for i := 0; i < int(count); i++ {
				binary.BigEndian.PutUint16(f.Tables["hmtx"][4*i:], upm*3/4)
			}
			binary.BigEndian.PutUint16(f.Tables["hhea"][10:], upm*3/4)
			raw, err = f.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(opts.Family[0].Path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			opts.BinDir, opts.ProcessOutlines = bin, outlines
			opts.AtlasExecutable = filepath.Join(bin, "../../vendor/msdf-atlas-gen/msdf-atlas-gen.exe")
			processor, err := fontcore.NewProcessor(bin, 2)
			if err != nil {
				t.Fatal(err)
			}
			defer processor.Close()
			generate := func(ctx context.Context, sources []atlas.Source, options atlas.Options) (*atlas.Result, error) {
				for _, source := range sources {
					if slices.Contains(source.Charset, rune(0x570b)) && !source.Custom {
						return nil, fmt.Errorf("family U+570B was classified as an original-font fallback: %+v", source)
					}
				}
				result, err := atlas.Generate(ctx, sources, options)
				if err != nil {
					return nil, err
				}
				glyph, ok := result.Glyphs[0x570b]
				if !ok || math.Abs(glyph.Advance-0.75) > 1e-6 {
					return nil, fmt.Errorf("U+570B used game fallback instead of family Bold: advance=%g sources=%+v", glyph.Advance, result.Report.Sources)
				}
				return result, nil
			}
			report, err := build(context.Background(), inv, &nativeOps{Processor: processor}, opts, nil, generate)
			if err != nil {
				t.Fatal(err)
			}
			if !report.OfflineReadbackVerified || len(report.Atlases) != 4 {
				t.Fatalf("family output did not verify all four atlases: verified=%v atlases=%d", report.OfflineReadbackVerified, len(report.Atlases))
			}
			files := zipContents(t, opts.OutputZIP)
			for _, archive := range []string{"09caa46bc556e38f", "c0b7644cc5c4aaa8"} {
				_, payload, _, _ := savedResource(t, files, archive, 4)
				face, err := inventory.InspectFace(payload[16:])
				if err != nil {
					t.Fatal(err)
				}
				if got := float64(face.Advances[0x570b]) / float64(face.UPM); math.Abs(got-0.75) > 1e-6 {
					t.Fatalf("runtime font %s disagrees with atlas family glyph: %g", archive, got)
				}
			}
		})
	}
}

func TestNativeFamilyBuild(t *testing.T) {
	if os.Getenv("HD2_NATIVE_PIPELINE_TEST") != "1" {
		t.Skip("set HD2_NATIVE_PIPELINE_TEST=1 for native family integration")
	}
	bin, err := filepath.Abs(filepath.Join("..", "..", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	for _, outlines := range []bool{false, true} {
		name := "original-outlines"
		if outlines {
			name = "processed-outlines"
		}
		t.Run(name, func(t *testing.T) {
			inv, _ := pipelineFixture(t)
			opts := familyFixture(t)
			opts.BinDir, opts.ProcessOutlines = bin, outlines
			opts.AtlasExecutable = filepath.Join(bin, "../../vendor/msdf-atlas-gen/msdf-atlas-gen.exe")
			processor, err := fontcore.NewProcessor(bin, 2)
			if err != nil {
				t.Fatal(err)
			}
			defer processor.Close()
			report, err := Build(context.Background(), inv, processor, opts, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.FamilyFaces) != 2 || !report.FullReplacement || !report.OfflineReadbackVerified {
				t.Fatal("family output did not pass native verification")
			}
			methods, names := map[int]string{}, map[int]string{}
			for _, style := range report.StyleAdaptation {
				methods[style.TargetWeight], names[style.TargetWeight] = style.Method, style.SourceName
			}
			if methods[700] != "family_face" || names[700] != "Synthetic Pixel Bold" {
				t.Fatal("native Bold was not used", methods, names)
			}
			if methods[400] != "family_face" || names[400] != "Synthetic Pixel" || methods[500] != "family_synthetic_emphasis" {
				t.Fatal("Regular and Medium sources are incorrect", methods, names)
			}
			foundTC := false
			for _, coverage := range report.Coverage {
				if coverage.Group == "tc" {
					foundTC = true
					if coverage.CustomCharacters != 3 {
						t.Fatalf("the Bold sibling's character coverage was lost: %+v", coverage)
					}
				}
			}
			if !foundTC {
				t.Fatal("missing traditional Chinese coverage report")
			}
		})
	}
}
