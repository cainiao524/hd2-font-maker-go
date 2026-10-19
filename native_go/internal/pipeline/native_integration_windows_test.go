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
