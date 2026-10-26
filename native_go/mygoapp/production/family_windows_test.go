//go:build windows && amd64

package production

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
	"hd2font/native/internal/archiveio"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/pipeline"
	"hd2font/native/internal/sfntio"
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

// mixedFamily writes static Regular/Bold and a same-family variable font.
// The variable sorts first, exposing accidental promotion to the base face.
func mixedFamily(t *testing.T) (boldPath, variablePath string) {
	t.Helper()
	data, err := os.ReadFile("../../internal/testfixtures/variable.ttf")
	if err != nil {
		t.Fatal(err)
	}
	f, err := sfntio.Parse(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"fvar", "gvar", "avar", "HVAR", "MVAR", "VVAR", "STAT"} {
		delete(f.Tables, tag)
	}
	regular, err := f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint16(f.Tables["OS/2"][4:], 700)
	bold, err := f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, raw := range map[string][]byte{"a-variable.ttf": data, "b-regular.ttf": regular, "c-bold.ttf": bold} {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "c-bold.ttf"), filepath.Join(dir, "a-variable.ttf")
}

func TestFamilyDiscoveryExcludesVariableSiblings(t *testing.T) {
	bold, variable := mixedFamily(t)
	b := testBackend(t)
	info, err := b.InspectFont(context.Background(), bold)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Siblings) != 1 || filepath.Base(info.Siblings[0].Path) != "b-regular.ttf" {
		t.Fatalf("static family includes a variable face: %+v", info.Siblings)
	}
	info, err = b.InspectFont(context.Background(), variable)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Faces) != 1 || len(info.Faces[0].Axes) != 1 || info.Faces[0].Axes[0].Tag != "wght" {
		t.Fatalf("direct variable import lost its axis: %+v", info.Faces)
	}
}

func TestMixedDirectoryGeneratesPinnedStaticBold(t *testing.T) {
	bold, _ := mixedFamily(t)
	b := testBackend(t)
	info, err := b.InspectFont(context.Background(), bold)
	if err != nil {
		t.Fatal(err)
	}
	members := append([]mygoapp.FamilyFace{{Path: info.Path, FaceIndex: 0, Name: info.Faces[0].Name, Style: info.Faces[0].Style}}, info.Siblings...)
	weights := make([]uint16, len(members))
	for i, m := range members {
		weights[i] = uint16(m.Style.Weight)
	}
	primary, _ := inventory.PickWeight(weights, 400)
	source := members[primary]
	req := request(t)
	req.Font = mygoapp.FontSelection{Path: source.Path, FaceIndex: source.FaceIndex, WeightFaces: map[int]int{}}
	for i, m := range members {
		if i != primary {
			req.Font.Family = append(req.Font.Family, m)
			if m.Path == info.Path {
				req.Font.WeightFaces[700] = len(req.Font.Family)
			}
		}
	}
	if req.Font.WeightFaces[700] == 0 {
		t.Fatal("static Bold was not available for pinning")
	}
	req.MatchWeights, req.ProcessOutlines, req.IncludeSymbols = true, false, false
	req.Languages = []string{"latin"}
	game, err := sfntio.Parse(goregular.TTF, 0)
	if err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint16(game.Tables["OS/2"][4:], 700)
	gameData, err := game.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	face, err := inventory.InspectFace(gameData)
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
	var report *pipeline.Report
	b.deps.build = func(ctx context.Context, inv *inventory.Inventory, opts pipeline.Options, progress func(string)) (*pipeline.Report, error) {
		report, err = b.build(ctx, inv, opts, progress)
		return report, err
	}
	if _, err = b.Generate(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if !report.OfflineReadbackVerified || len(report.StyleAdaptation) != 1 || report.StyleAdaptation[0].Method != "family_face" || len(report.FamilyFaces) != 2 {
		t.Fatalf("static Bold was ignored: styles=%+v family=%v", report.StyleAdaptation, report.FamilyFaces)
	}
}
