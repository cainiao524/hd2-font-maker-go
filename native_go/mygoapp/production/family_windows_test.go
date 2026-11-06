//go:build windows && amd64

package production

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomedium"
	"golang.org/x/image/font/gofont/goregular"
	"hd2font/native/internal/archiveio"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/pipeline"
	"hd2font/native/internal/sfntio"
	"hd2font/native/mygoapp"
)

func TestExplicitThreeWeightsPreviewUsesAuthoredFaces(t *testing.T) {
	b := testBackend(t)
	var faces []mygoapp.FamilyFace
	regular, err := sfntio.Parse(goregular.TTF, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, raw := range [][]byte{goregular.TTF, gomedium.TTF, gobold.TTF} {
		// Go Bold declares 600 and Go Medium has its own family name. Set
		// fixture metadata to one 400/500/700 family without altering drawings.
		f, err := sfntio.Parse(raw, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.Tables["name"] = regular.Tables["name"]
		binary.BigEndian.PutUint16(f.Tables["OS/2"][4:], []uint16{400, 500, 700}[i])
		raw, err = f.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		info, err := b.InspectFont(context.Background(), fontFile(t, raw, strconv.Itoa(i)+".ttf"))
		if err != nil {
			t.Fatal(err)
		}
		faces = append(faces, mygoapp.FamilyFace{Path: info.Path, FaceIndex: info.Faces[0].Index, Name: info.Faces[0].Name, Style: info.Faces[0].Style})
	}
	selection := mygoapp.FontSelection{Path: faces[0].Path, Family: faces[1:], SelectedFiles: []string{faces[0].Path, faces[1].Path, faces[2].Path}}
	render := func(req mygoapp.PreviewRequest) []byte {
		t.Helper()
		result, err := b.Preview(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, result.Image); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	var previous []byte
	for i, weight := range []int{400, 500, 700} {
		req := mygoapp.PreviewRequest{Font: selection, MatchWeights: true, Weight: weight, Text: "Major Order Rewards 0123", Width: 700, Height: 180}
		got := render(req)
		req.MatchWeights = false
		req.Font = mygoapp.FontSelection{Path: faces[i].Path}
		if !bytes.Equal(got, render(req)) {
			t.Fatalf("weight %d did not use its authored file", weight)
		}
		if bytes.Equal(got, previous) {
			t.Fatalf("weight %d reused the previous drawing", weight)
		}
		previous = got
	}
}

func TestImportDoesNotDiscoverNeighboringCopies(t *testing.T) {
	bold, _ := mixedFamily(t)
	for _, name := range []string{"b-regular.ttf", "c-bold.ttf"} {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(bold), name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(bold), "copy-"+name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	b := testBackend(t)
	info, err := b.InspectFont(context.Background(), bold)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Siblings) != 0 {
		t.Fatalf("file copies were mistaken for new weights: %+v", info.Siblings)
	}
}

func TestDuplicateSavedFamilyPreviewMatchesSingleFont(t *testing.T) {
	b := testBackend(t)
	source := fontFile(t, goregular.TTF, "regular.ttf")
	copyPath := fontFile(t, goregular.TTF, "regular (1).ttf")
	req := mygoapp.PreviewRequest{Font: mygoapp.FontSelection{Path: source}, MatchWeights: true, Text: "Major Order Rewards 0123", Width: 700, Height: 180}
	render := func() []byte {
		t.Helper()
		result, err := b.Preview(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, result.Image); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	for _, target := range []int{400, 500, 700} {
		req.Weight, req.Font.Family = target, nil
		want := render()
		req.Font.Family = []mygoapp.FamilyFace{{Path: copyPath, Style: mygoapp.FaceStyle{Weight: 400}}}
		if !bytes.Equal(want, render()) {
			t.Fatalf("duplicate copy changed weight %d preview", target)
		}
	}
	req.Weight, req.MatchWeights = 700, false
	want := render()
	req.MatchWeights, req.Font.WeightFaces = true, map[int]int{700: 1}
	if !bytes.Equal(want, render()) {
		t.Fatal("manual choice of a removed duplicate no longer preserves the original")
	}
}

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

func TestImportReadsOnlySelectedFile(t *testing.T) {
	bold, variable := mixedFamily(t)
	b := testBackend(t)
	info, err := b.InspectFont(context.Background(), bold)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Siblings) != 0 {
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

func TestExplicitFamilyGeneratesPinnedStaticBold(t *testing.T) {
	bold, _ := mixedFamily(t)
	b := testBackend(t)
	info, err := b.InspectFont(context.Background(), bold)
	if err != nil {
		t.Fatal(err)
	}
	regular, err := b.InspectFont(context.Background(), filepath.Join(filepath.Dir(bold), "b-regular.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	members := []mygoapp.FamilyFace{
		{Path: info.Path, FaceIndex: 0, Name: info.Faces[0].Name, Style: info.Faces[0].Style},
		{Path: regular.Path, FaceIndex: 0, Name: regular.Faces[0].Name, Style: regular.Faces[0].Style},
	}
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
