package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"hd2font/native/internal/fontcore"
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
