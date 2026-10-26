//go:build windows && amd64

package production

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"hd2font/native/internal/inventory"
	"hd2font/native/internal/pipeline"
	"hd2font/native/mygoapp"
)

func TestGenerateSkipsUnusedFamily(t *testing.T) {
	for _, tc := range []struct {
		name         string
		variable     bool
		matchWeights bool
		wantMissing  bool
	}{
		{"disabled", false, false, false},
		{"variable", true, true, false},
		{"static", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := testBackend(t)
			req := request(t)
			req.MatchWeights = tc.matchWeights
			if tc.variable {
				data, err := os.ReadFile("../../internal/testfixtures/variable.ttf")
				if err != nil {
					t.Fatal(err)
				}
				req.Font = mygoapp.FontSelection{Path: fontFile(t, data, "variable.ttf")}
			}
			req.Font.Family = []mygoapp.FamilyFace{{Path: filepath.Join(t.TempDir(), "missing.ttf")}}
			built := false
			b.deps.scan = func(context.Context, string, func(string)) (*inventory.Inventory, error) {
				return &inventory.Inventory{}, nil
			}
			b.deps.build = func(_ context.Context, _ *inventory.Inventory, opts pipeline.Options, _ func(string)) (*pipeline.Report, error) {
				built = true
				if len(opts.Family) != 0 {
					t.Fatalf("unused family reached the pipeline: %+v", opts.Family)
				}
				if err := os.WriteFile(opts.OutputZIP, []byte("mock-only package"), 0600); err != nil {
					return nil, err
				}
				return &pipeline.Report{Output: opts.OutputZIP, OfflineReadbackVerified: true}, nil
			}
			_, err := b.Generate(context.Background(), req, nil)
			if tc.wantMissing {
				if !errors.Is(err, os.ErrNotExist) || built {
					t.Fatalf("missing active family was not rejected before build: built=%v, err=%v", built, err)
				}
			} else if err != nil || !built {
				t.Fatalf("unused sibling blocked generation: built=%v, err=%v", built, err)
			}
		})
	}
}
