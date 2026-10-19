package inventory

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"hd2font/native/internal/archiveio"
	"hd2font/native/internal/sfntio"
)

func syntheticFont(t *testing.T, name string, chars []rune) []byte {
	t.Helper()
	chars = append([]rune(nil), chars...)
	sort.Slice(chars, func(i, j int) bool { return chars[i] < chars[j] })
	count := len(chars) + 1
	head, hhea, maxp := make([]byte, 54), make([]byte, 36), make([]byte, 32)
	binary.BigEndian.PutUint32(head, 0x10000)
	binary.BigEndian.PutUint32(head[12:], 0x5f0f3cf5)
	binary.BigEndian.PutUint16(head[18:], 1000)
	binary.BigEndian.PutUint32(hhea, 0x10000)
	binary.BigEndian.PutUint16(hhea[34:], uint16(count))
	binary.BigEndian.PutUint32(maxp, 0x10000)
	binary.BigEndian.PutUint16(maxp[4:], uint16(count))
	hmtx := make([]byte, 4*count)
	for i := 0; i < count; i++ {
		binary.BigEndian.PutUint16(hmtx[4*i:], 500)
	}
	units := utf16.Encode([]rune(name))
	names := make([]byte, 18+2*len(units))
	binary.BigEndian.PutUint16(names[2:], 1)
	binary.BigEndian.PutUint16(names[4:], 18)
	for i, v := range []uint16{3, 1, 0x409, 4, uint16(2 * len(units)), 0} {
		binary.BigEndian.PutUint16(names[6+2*i:], v)
	}
	for i, v := range units {
		binary.BigEndian.PutUint16(names[18+2*i:], v)
	}
	cmap := make([]byte, 28+12*len(chars))
	binary.BigEndian.PutUint16(cmap[2:], 1)
	binary.BigEndian.PutUint16(cmap[4:], 3)
	binary.BigEndian.PutUint16(cmap[6:], 10)
	binary.BigEndian.PutUint32(cmap[8:], 12)
	binary.BigEndian.PutUint16(cmap[12:], 12)
	binary.BigEndian.PutUint32(cmap[16:], uint32(len(cmap)-12))
	binary.BigEndian.PutUint32(cmap[24:], uint32(len(chars)))
	for i, cp := range chars {
		r := cmap[28+12*i:]
		binary.BigEndian.PutUint32(r, uint32(cp))
		binary.BigEndian.PutUint32(r[4:], uint32(cp))
		binary.BigEndian.PutUint32(r[8:], uint32(i+1))
	}
	os2 := make([]byte, 78)
	binary.BigEndian.PutUint16(os2[4:], 500)
	post := make([]byte, 32)
	binary.BigEndian.PutUint32(post, 0x30000)
	f := &sfntio.Font{Version: 0x10000, Tables: map[string][]byte{"head": head, "hhea": hhea, "hmtx": hmtx, "maxp": maxp, "name": names, "cmap": cmap, "OS/2": os2, "post": post, "glyf": {}, "loca": make([]byte, 2*(count+1))}}
	data, err := f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func wrapped(raw []byte) []byte {
	payload := make([]byte, 16+len(raw))
	binary.LittleEndian.PutUint32(payload, uint32(len(raw)))
	copy(payload[16:], raw)
	return payload
}

func fixtureResource(kind string, id uint64, payload, stream, gpu []byte) archiveio.Resource {
	typ := archiveio.TypeRecord{TypeID: archiveio.ResourceHash(kind), Unknown0: 77}
	return archiveio.Resource{Entry: archiveio.Entry{NameID: id, TypeID: typ.TypeID, MainAlignment: 16, GPUAlignment: 16}, Type: typ, Main: payload, Stream: stream, GPU: gpu}
}

func writeArchive(t *testing.T, dir, name string, resources []archiveio.Resource) {
	t.Helper()
	parts, err := archiveio.MakePatchParts(resources)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []struct {
		suffix string
		data   []byte
	}{{"", parts.Main}, {".stream", parts.Stream}, {".gpu_resources", parts.GPU}} {
		if len(part.data) == 0 {
			continue
		}
		if err = os.WriteFile(filepath.Join(dir, name+part.suffix), part.data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func glyphPayload(keys []uint32) []byte {
	p := make([]byte, 88+32*len(keys))
	binary.LittleEndian.PutUint32(p[8:], math.Float32bits(32))
	binary.LittleEndian.PutUint32(p[68:], uint32(len(keys)))
	binary.LittleEndian.PutUint32(p[72:], 88)
	binary.LittleEndian.PutUint32(p[80:], uint32(len(p)))
	for i, key := range keys {
		binary.LittleEndian.PutUint32(p[88+4*i:], key)
		binary.LittleEndian.PutUint32(p[88+4*len(keys)+28*i+24:], math.Float32bits(16))
	}
	return p
}

func texturePayload(size int) []byte {
	p := make([]byte, 340)
	copy(p[192:], "DDS ")
	binary.LittleEndian.PutUint32(p[204:], uint32(size))
	binary.LittleEndian.PutUint32(p[208:], uint32(size))
	binary.LittleEndian.PutUint32(p[320:], 28)
	return p
}

func scanFixture(t *testing.T) (*Inventory, string) {
	t.Helper()
	dir := t.TempDir()
	latin := syntheticFont(t, "Noto Sans Synthetic", []rune{'?', 'A'})
	sc := syntheticFont(t, "M Ying Hei PRC W5", []rune{'?', 'A', 0x4e2d})
	tc := syntheticFont(t, "MJNgaiHK-Medium", []rune{'?', 'A', 0x570b})
	runtime, err := archiveio.MakeRuntime(make([]byte, 40), []archiveio.RuntimePart{{Metrics: [3]float32{1, 2, 3}, Data: latin}, {Metrics: [3]float32{4, 5, 6}, Data: latin}, {Metrics: [3]float32{7, 8, 9}, Data: tc}})
	if err != nil {
		t.Fatal(err)
	}
	writeArchive(t, dir, "0123456789abcdef", []archiveio.Resource{
		fixtureResource("ttf", 1, wrapped(latin), nil, nil),
		fixtureResource("runtime_font", 2, runtime, nil, nil),
		fixtureResource("texture", 3, []byte("unrelated: must not inspect"), nil, nil),
	})
	writeArchive(t, dir, "09caa46bc556e38f", []archiveio.Resource{
		fixtureResource("ttf", 4, wrapped(sc), nil, nil),
		fixtureResource("font", 5, glyphPayload([]uint32{0, '?', 'A', 0x4e2d}), nil, nil),
		fixtureResource("texture", 6, texturePayload(2), []byte("stream-data"), bytes.Repeat([]byte{19}, 20)),
		fixtureResource("texture", 7, texturePayload(1), nil, nil),
	})
	var progress []string
	inv, err := Scan(dir, func(s string) { progress = append(progress, s) })
	if err != nil {
		t.Fatal(err)
	}
	if len(progress) < 2 {
		t.Fatal("missing progress reports")
	}
	return inv, dir
}

func TestInspectFaceAndPreferredCmap(t *testing.T) {
	raw := syntheticFont(t, "Noto Sans Synthetic", []rune{65, 0x1f600})
	face, err := InspectFace(append(raw, []byte("unrelated trailing bytes")...))
	if err != nil {
		t.Fatal(err)
	}
	size, err := archiveio.SFNTSize(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(face.Data) != size || face.Digest != Digest(raw[:size]) || face.Group != "latin" || face.UPM != 1000 || face.UnitsPerEm != 1000 || face.Weight != 500 || face.Format != "ttf" {
		t.Fatalf("metadata: %+v", face)
	}
	if face.Cmap[0x1f600] != 2 || face.Advances[65] != 500 {
		t.Fatal("wrong full Unicode mapping", face.Cmap)
	}
	parsed, err := sfntio.Parse(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	preferred := parsed.Tables["cmap"][12:]
	// A conflicting BMP format 6 appears first, but Windows full Unicode wins.
	cmap := make([]byte, 32+len(preferred))
	binary.BigEndian.PutUint16(cmap[2:], 2)
	for i, v := range []uint16{3, 1} {
		binary.BigEndian.PutUint16(cmap[4+2*i:], v)
	}
	binary.BigEndian.PutUint32(cmap[8:], 20)
	binary.BigEndian.PutUint16(cmap[12:], 3)
	binary.BigEndian.PutUint16(cmap[14:], 10)
	binary.BigEndian.PutUint32(cmap[16:], 32)
	for i, v := range []uint16{6, 12, 0, 66, 1, 1} {
		binary.BigEndian.PutUint16(cmap[20+2*i:], v)
	}
	copy(cmap[32:], preferred)
	parsed.Tables["cmap"] = cmap
	modified, err := parsed.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	got, err := InspectFace(modified)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Chars, face.Chars) {
		t.Fatal("cmap preference differs")
	}
	if _, err = InspectFace(raw[:20]); err == nil {
		t.Fatal("truncated SFNT accepted")
	}
	// Raw SFNTs need not include the final table's alignment padding.
	trimmed := raw[:size]
	if _, err = InspectFace(trimmed); err != nil {
		t.Fatal("unpadded SFNT:", err)
	}
	if python := os.Getenv("INVENTORY_ORACLE_PYTHON"); python != "" {
		compareOracle(t, python, "", [][]byte{raw, modified})
	}
}

func TestScanUniqueFacesResourcesAndAtlasSelection(t *testing.T) {
	inv, dir := scanFixture(t)
	if len(inv.Faces) != 3 || len(inv.Groups) != 3 || len(inv.Resources) != 6 || !inv.Complete() {
		t.Fatalf("scan: faces %d, groups %d, resources %d", len(inv.Faces), len(inv.Groups), len(inv.Resources))
	}
	for _, r := range inv.Resources {
		if r.TypeRecord.Unknown0 != 77 {
			t.Fatal("lost original type metadata")
		}
		if r.Kind == "runtime_font" && (len(r.Faces) != 3 || r.Faces[0] != r.Faces[1] || r.Faces[0] != inv.Groups["latin"][0]) {
			t.Fatal("runtime slot order or deduplication changed")
		}
		if r.Entry.NameID == 3 {
			t.Fatal("unrelated texture was scanned")
		}
	}
	selected, err := inv.SelectAtlases([]string{"sc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Texture.Entry.NameID != 6 || selected[0].Template.Group != "sc" || !reflect.DeepEqual(selected[0].Keys, []uint32{0, '?', 'A', 0x4e2d}) {
		t.Fatal("incorrect atlas selection", selected)
	}
	if result, err := inv.SelectAtlases([]string{"tc"}); err != nil || len(result) != 0 {
		t.Fatal("out-of-scope atlas selected", err)
	}
	if python := os.Getenv("INVENTORY_ORACLE_PYTHON"); python != "" {
		compareOracle(t, python, dir, nil)
	}
}

func TestSnapshotPortableAndVerified(t *testing.T) {
	inv, game := scanFixture(t)
	directory := filepath.Join(t.TempDir(), "snapshot")
	if err := Save(inv, directory); err != nil {
		t.Fatal(err)
	}
	// Removal is restricted to the synthetic directory returned by t.TempDir.
	if err := os.RemoveAll(game); err != nil {
		t.Fatal(err)
	}
	moved := directory + "-moved"
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(moved)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Catalog != nil || !loaded.Complete() || !reflect.DeepEqual(inv.FaceOrder, loaded.FaceOrder) {
		t.Fatal("snapshot retains installation dependency or loses face order")
	}
	for i, r := range loaded.Resources {
		want := inv.Resources[i]
		if r.Entry != want.Entry || r.TypeRecord != want.TypeRecord || !bytes.Equal(r.Payload, want.Payload) || !bytes.Equal(r.Stream, want.Stream) || !bytes.Equal(r.GPU, want.GPU) {
			t.Fatal("resource changed in snapshot")
		}
		for _, f := range r.Faces {
			if f != loaded.Faces[f.Digest] {
				t.Fatal("face pointer is not deduplicated")
			}
		}
	}
	if _, err = loaded.SelectAtlases([]string{"sc"}); err != nil {
		t.Fatal(err)
	}
	if err = loaded.Save(moved); err != nil {
		t.Fatal("saving existing snapshot:", err)
	}
	manifest, err := os.ReadFile(filepath.Join(moved, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifest), game) {
		t.Fatal("snapshot contains installation path")
	}
	var m Manifest
	if err = json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	faceBlob := filepath.Join(moved, "blobs", m.Faces[0].Blob+".bin")
	if err = os.WriteFile(faceBlob, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(moved); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatal("corrupted blob accepted", err)
	}
	// Blob references cannot turn into arbitrary relative file paths.
	m.Faces[0].Blob = "../../outside"
	bad, _ := json.Marshal(m)
	if err = os.WriteFile(filepath.Join(moved, "manifest.json"), bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(moved); err == nil {
		t.Fatal("invalid blob reference accepted")
	}
}

func TestLegacyMissingCompanionsAreExplicit(t *testing.T) {
	inv, _ := scanFixture(t)
	for _, r := range inv.Resources {
		if r.Entry.NameID == 6 {
			r.Stream = nil
			r.GPU = nil
			r.MissingParts = []string{"stream", "gpu"}
		}
	}
	dir := t.TempDir()
	if err := inv.Save(dir); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Complete() {
		t.Fatal("legacy snapshot incorrectly reported complete")
	}
	if _, err = loaded.SelectAtlases([]string{"sc"}); err != nil {
		t.Fatal(err)
	}
}

func TestLanguageScope(t *testing.T) {
	inv, _ := scanFixture(t)
	custom := &Face{Chars: map[rune]struct{}{'A': {}, 0x4e2d: {}, 0x570b: {}}}
	inv.Groups["emoji"] = []*Face{{Chars: map[rune]struct{}{0x1f600: {}}}}
	selected, coverage, err := LanguagePlan(inv, custom, "custom.ttf", 2, []string{"sc"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected["sc"].Index != 2 || selected["latin"].Path == "" {
		t.Fatal("wrong language plan", selected)
	}
	for _, row := range coverage {
		if (row.Group == "tc" || row.Group == "emoji") && row.Selected {
			t.Fatal("incidental coverage expanded scope")
		}
		if row.Group == "sc" && (row.CustomCharacters != 2 || row.OriginalFallbackCharacters != 1) {
			t.Fatal("coverage counts", row)
		}
	}
	groups, err := ResourceGroups([]string{"tc", "sc", "tc", "latin"})
	if err != nil || !reflect.DeepEqual(groups, []string{"latin", "sc", "tc"}) {
		t.Fatal(groups, err)
	}
	for _, languages := range [][]string{{}, {"other"}, {"arabic"}} {
		if _, err = NormalizeLanguages(languages); err == nil {
			t.Fatal("invalid language accepted", languages)
		}
	}
	if _, _, err = LanguagePlan(inv, custom, "custom.ttf", 0, []string{"jp"}, false); err == nil {
		t.Fatal("missing group accepted")
	}
	if _, _, err = LanguagePlan(inv, &Face{Chars: map[rune]struct{}{0x1234: {}}}, "font", 0, []string{"sc"}, false); err == nil {
		t.Fatal("zero covered characters accepted")
	}
}

func TestSavedLocalSnapshot(t *testing.T) {
	dir := os.Getenv("INVENTORY_LOCAL_SNAPSHOT")
	if dir == "" {
		t.Skip("set INVENTORY_LOCAL_SNAPSHOT to the private exported saved pickle")
	}
	inv, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Faces) != 21 || len(inv.Resources) != 63 {
		t.Fatalf("unexpected saved inventory %d faces/%d resources", len(inv.Faces), len(inv.Resources))
	}
	if inv.Catalog != nil {
		t.Fatal("offline snapshot has a live catalog")
	}
	atlases, err := inv.SelectAtlases([]string{"latin", "sc", "tc", "jp", "ko"})
	if err != nil {
		t.Fatal(err)
	}
	if len(atlases) != 13 {
		t.Fatalf("expected 13 verified atlas bindings, got %d", len(atlases))
	}
	t.Logf("saved inventory: %d unique faces, %d groups, %d resources, %d verified atlas bindings, complete=%v", len(inv.Faces), len(inv.Groups), len(inv.Resources), len(atlases), inv.Complete())
}

func compareOracle(t *testing.T, python, game string, faces [][]byte) {
	t.Helper()
	request := map[string]any{"game": game}
	var inputs []string
	for _, raw := range faces {
		inputs = append(inputs, hex.EncodeToString(raw))
	}
	request["faces"] = inputs
	input, _ := json.Marshal(request)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "testdata/oracle.py")
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Python oracle: %v\n%s", err, out)
	}
	var got struct {
		Faces []struct {
			Name, Group, Digest string
			Chars               []rune
			Cmap                map[string]uint16
		}
		Resources int
	}
	if err = json.Unmarshal(out, &got); err != nil {
		t.Fatal(err, string(out))
	}
	var expected []*Face
	if game != "" {
		inv, err := Scan(game, nil)
		if err != nil {
			t.Fatal(err)
		}
		expected = inv.OrderedFaces()
		if len(inv.Resources) != got.Resources {
			t.Fatal("Python resource count differs")
		}
	} else {
		for _, raw := range faces {
			f, err := InspectFace(raw)
			if err != nil {
				t.Fatal(err)
			}
			expected = append(expected, f)
		}
	}
	if len(got.Faces) != len(expected) {
		t.Fatal("Python face count differs")
	}
	for i, f := range expected {
		row := got.Faces[i]
		if row.Name != f.Name || row.Group != f.Group || row.Digest != f.Digest || !reflect.DeepEqual(row.Chars, f.Codepoints()) {
			t.Fatalf("Python face differs for %s: %+v", f.Name, row)
		}
		for cp, gid := range f.Cmap {
			if row.Cmap[string([]rune{cp})] != gid {
				t.Fatalf("Python cmap differs U+%04X", cp)
			}
		}
	}
}
