//go:build windows && amd64

package production

import (
	"context"
	"errors"
	"os"
	"testing"

	"hd2font/native/internal/inventory"
	"hd2font/native/internal/pipeline"
	"hd2font/native/mygoapp"
)

// Use messages from a real five-face Noto build, including the resource
// readback after atlas generation. Each batch's completion is not job completion.
func TestNativeBuildReportsUsefulOverallProgress(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "verified", true: "verification_failed"}[fail], func(t *testing.T) {
			b := testBackend(t)
			req := request(t)
			b.deps.scan = func(_ context.Context, _ string, progress func(string)) (*inventory.Inventory, error) {
				progress("Reading font index 0/3552")
				progress("Reading font index 3500/3552")
				progress("Found 21 unique fonts in 11 resource groups")
				return &inventory.Inventory{}, nil
			}
			verificationError := errors.New("saved ZIP member checksum mismatch")
			b.deps.build = func(_ context.Context, _ *inventory.Inventory, opts pipeline.Options, progress func(string)) (*pipeline.Report, error) {
				for _, message := range []string{
					"Preparing selected font", "Preparing family faces 1/4", "Preparing family faces 4/4",
					"Preparing font weights 1/4", "Preparing font weights 4/4",
					"Merging full font repertoires 4/4", "Conforming font formats 9/9",
					"Generating MTSDF atlases 1/12", "Generating MTSDF atlases 12/12",
					"Writing and reading back font resources",
					"Packaging ZIP and verifying every saved member", "Packaging 1/25: Fonts/example.patch_0",
					"Packaging 25/25: 使用说明.txt", "Font package complete: result.zip",
				} {
					progress(message)
				}
				if fail {
					return nil, verificationError
				}
				if err := os.WriteFile(opts.OutputZIP, []byte("mock verified output"), 0600); err != nil {
					return nil, err
				}
				return &pipeline.Report{Output: opts.OutputZIP, OfflineReadbackVerified: true}, nil
			}
			var updates []mygoapp.Progress
			_, err := b.Generate(context.Background(), req, func(p mygoapp.Progress) { updates = append(updates, p) })
			if fail && !errors.Is(err, verificationError) || !fail && err != nil {
				t.Fatalf("generation result: %v", err)
			}
			previous := 0.0
			advanced := 0
			for _, p := range updates {
				if p.Fraction < previous || p.Fraction > 1 {
					t.Fatalf("invalid or regressing progress: previous=%v update=%+v", previous, p)
				}
				if p.Stage != "done" && p.Fraction >= 1 {
					t.Fatalf("unverified output reported complete: %+v", p)
				}
				if p.Fraction > previous && p.Fraction < 1 {
					advanced++
				}
				if p.Detail == "Writing and reading back font resources" && p.Stage != "package" {
					t.Fatalf("late readback classified as an earlier stage: %+v", p)
				}
				previous = p.Fraction
			}
			if advanced < 6 {
				t.Fatalf("expected incremental overall progress, got %d advances: %+v", advanced, updates)
			}
			last := updates[len(updates)-1]
			if fail && last.Fraction >= 1 || !fail && (last.Stage != "done" || last.Fraction != 1) {
				t.Fatalf("completion does not reflect verification: %+v", last)
			}
		})
	}
}
