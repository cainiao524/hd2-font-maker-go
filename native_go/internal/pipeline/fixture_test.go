package pipeline

import (
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"unicode/utf16"

	"hd2font/native/internal/archiveio"
	"hd2font/native/internal/atlas"
	"hd2font/native/internal/geometry"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/sfntio"
)

func syntheticFont(name string, weight int, upm uint16, characters []rune) ([]byte, error) {
	set := make(map[rune]bool)
	for _, cp := range characters {
		set[cp] = true
	}
	chars := make([]rune, 0, len(set))
	for cp := range set {
		chars = append(chars, cp)
	}
	sort.Slice(chars, func(i, j int) bool { return chars[i] < chars[j] })
	count := len(chars) + 1
	be := binary.BigEndian
	head, hhea, maxp, os2, post := make([]byte, 54), make([]byte, 36), make([]byte, 32), make([]byte, 96), make([]byte, 32)
	be.PutUint32(head, 0x10000)
	be.PutUint32(head[12:], 0x5f0f3cf5)
	be.PutUint16(head[18:], upm)
	be.PutUint16(head[40:], upm/2)
	be.PutUint16(head[42:], upm*3/4)
	be.PutUint16(head[46:], 8)
	be.PutUint16(head[50:], 1)
	be.PutUint32(hhea, 0x10000)
	be.PutUint16(hhea[4:], upm*3/4)
	be.PutUint16(hhea[6:], uint16(-int(upm)/4))
	be.PutUint16(hhea[10:], upm/2)
	be.PutUint16(hhea[18:], 1)
	be.PutUint16(hhea[34:], uint16(count))
	be.PutUint32(maxp, 0x10000)
	be.PutUint16(maxp[4:], uint16(count))
	be.PutUint16(maxp[6:], 4)
	be.PutUint16(maxp[8:], 1)
	be.PutUint16(maxp[14:], 2)
	be.PutUint16(os2, 3)
	be.PutUint16(os2[2:], upm/2)
	be.PutUint16(os2[4:], uint16(weight))
	be.PutUint16(os2[6:], 5)
	be.PutUint16(os2[62:], 64)
	be.PutUint16(os2[68:], upm*3/4)
	be.PutUint16(os2[70:], uint16(-int(upm)/4))
	be.PutUint16(os2[74:], upm*3/4)
	be.PutUint16(os2[76:], upm/4)
	be.PutUint32(post, 0x30000)
	hmtx, loca := make([]byte, 4*count), make([]byte, 4*(count+1))
	var glyf []byte
	var path geometry.Path
	path.MoveTo(0, 0)
	path.LineTo(0, float64(upm)*.75)
	path.LineTo(float64(upm)*.5, float64(upm)*.75)
	path.LineTo(float64(upm)*.5, 0)
	path.Close()
	glyph, err := sfntio.EncodeGlyph(path)
	if err != nil {
		return nil, err
	}
	for i := 0; i < count; i++ {
		be.PutUint16(hmtx[4*i:], upm/2)
		be.PutUint32(loca[4*i:], uint32(len(glyf)))
		glyf = append(glyf, glyph.Data...)
		for len(glyf)%4 != 0 {
			glyf = append(glyf, 0)
		}
	}
	be.PutUint32(loca[4*count:], uint32(len(glyf)))
	units := utf16.Encode([]rune(name))
	ids := []uint16{1, 2, 4, 6, 16, 17}
	names := make([]byte, 6+12*len(ids))
	be.PutUint16(names[2:], uint16(len(ids)))
	be.PutUint16(names[4:], uint16(len(names)))
	for i, id := range ids {
		row := names[6+12*i:]
		for j, v := range []uint16{3, 1, 0x409, id, uint16(2 * len(units)), uint16(i * 2 * len(units))} {
			be.PutUint16(row[2*j:], v)
		}
	}
	for range ids {
		for _, u := range units {
			names = be.AppendUint16(names, u)
		}
	}
	cmap := make([]byte, 28+12*len(chars))
	be.PutUint16(cmap[2:], 1)
	be.PutUint16(cmap[4:], 3)
	be.PutUint16(cmap[6:], 10)
	be.PutUint32(cmap[8:], 12)
	be.PutUint16(cmap[12:], 12)
	be.PutUint32(cmap[16:], uint32(len(cmap)-12))
	be.PutUint32(cmap[24:], uint32(len(chars)))
	for i, cp := range chars {
		row := cmap[28+12*i:]
		be.PutUint32(row, uint32(cp))
		be.PutUint32(row[4:], uint32(cp))
		be.PutUint32(row[8:], uint32(i+1))
	}
	f := &sfntio.Font{Version: 0x10000, Tables: map[string][]byte{"head": head, "hhea": hhea, "hmtx": hmtx, "maxp": maxp, "name": names, "OS/2": os2, "post": post, "loca": loca, "glyf": glyf, "cmap": cmap}}
	return f.Bytes()
}

func faceFixture(t *testing.T, name string, weight int, chars ...rune) *inventory.Face {
	t.Helper()
	raw, err := syntheticFont(name, weight, 1000, chars)
	if err != nil {
		t.Fatal(err)
	}
	face, err := inventory.InspectFace(raw)
	if err != nil {
		t.Fatal(err)
	}
	return face
}

func resourceFixture(archive, kind string, id uint64, payload []byte, faces ...*inventory.Face) *inventory.Resource {
	typeID := archiveio.ResourceHash(kind)
	return &inventory.Resource{Archive: archive, Kind: kind, Entry: archiveio.Entry{NameID: id, TypeID: typeID, MainSize: uint32(len(payload)), MainAlignment: 16, GPUAlignment: 16}, TypeRecord: archiveio.TypeRecord{TypeID: typeID, Count: 1, Unknown0: 73}, Payload: payload, Faces: faces}
}
func wrappedFont(face *inventory.Face) []byte {
	out := make([]byte, 16+len(face.Data))
	binary.LittleEndian.PutUint32(out, uint32(len(face.Data)))
	out[8] = 42
	copy(out[16:], face.Data)
	return out
}
func bakedFixture(keys []uint32) []byte {
	p := make([]byte, 88+32*len(keys))
	binary.LittleEndian.PutUint32(p[8:], math.Float32bits(32))
	binary.LittleEndian.PutUint32(p[12:], math.Float32bits(40))
	binary.LittleEndian.PutUint32(p[16:], math.Float32bits(30))
	binary.LittleEndian.PutUint32(p[68:], uint32(len(keys)))
	binary.LittleEndian.PutUint32(p[72:], 88)
	binary.LittleEndian.PutUint32(p[80:], uint32(len(p)))
	for i, cp := range keys {
		binary.LittleEndian.PutUint32(p[88+4*i:], cp)
		binary.LittleEndian.PutUint32(p[88+4*len(keys)+28*i+24:], math.Float32bits(16))
	}
	return p
}
func textureFixture() []byte {
	p := make([]byte, 340)
	copy(p[192:], "DDS ")
	binary.LittleEndian.PutUint32(p[204:], 2)
	binary.LittleEndian.PutUint32(p[208:], 2)
	binary.LittleEndian.PutUint32(p[320:], 28)
	return p
}

func pipelineFixture(t *testing.T) (*inventory.Inventory, Options) {
	t.Helper()
	latin := faceFixture(t, "Noto Sans Synthetic Bold", 700, '?', 'A', 'B', 'Z')
	sc := faceFixture(t, "M Ying Hei PRC W5", 500, '?', 'A', 0x4e2d, 0x4e09)
	tc := faceFixture(t, "MJNgaiHK-Medium", 400, '?', 'A', 0x570b, 0x5708)
	inv := &inventory.Inventory{Faces: map[string]*inventory.Face{}, Groups: map[string][]*inventory.Face{"latin": {latin}, "sc": {sc}, "tc": {tc}}}
	for _, f := range []*inventory.Face{latin, sc, tc} {
		inv.Faces[f.Digest] = f
		inv.FaceOrder = append(inv.FaceOrder, f.Digest)
	}
	for _, item := range []struct {
		archive string
		face    *inventory.Face
		keys    []uint32
	}{
		{"0de4b81e19a79aa8", latin, []uint32{0, '?', 'A', 'B'}}, {"41c8eff8188e4862", latin, []uint32{0, '?', 'A', 'B'}},
		{"09caa46bc556e38f", sc, []uint32{0, '?', 'A', 0x4e2d}}, {"c0b7644cc5c4aaa8", tc, []uint32{0, '?', 'A', 0x570b}},
	} {
		texture := resourceFixture(item.archive, "texture", 6, textureFixture())
		texture.Entry.GPUSize = 20
		texture.MissingParts = []string{"gpu"}
		inv.Resources = append(inv.Resources, resourceFixture(item.archive, "ttf", 4, wrappedFont(item.face), item.face), resourceFixture(item.archive, "font", 5, bakedFixture(item.keys)), texture)
	}
	runtime, err := archiveio.MakeRuntime(make([]byte, 40), []archiveio.RuntimePart{{Metrics: [3]float32{1, 2, 3}, Data: latin.Data}, {Metrics: [3]float32{4, 5, 6}, Data: sc.Data}, {Metrics: [3]float32{7, 8, 9}, Data: sc.Data}, {Metrics: [3]float32{10, 11, 12}, Data: tc.Data}})
	if err != nil {
		t.Fatal(err)
	}
	inv.Resources = append(inv.Resources, resourceFixture("0123456789abcdef", "runtime_font", 9, runtime, latin, sc, sc, tc))
	dir := t.TempDir()
	raw, err := syntheticFont("Synthetic Pixel", 400, 256, []rune{'?', 'A', 0x4e2d, 0x4e8c, 0x1f600})
	if err != nil {
		t.Fatal(err)
	}
	fontPath := filepath.Join(dir, "custom.ttf")
	if err = os.WriteFile(fontPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.FontPath = fontPath
	opts.OutputZIP = filepath.Join(dir, "output.zip")
	opts.WorkDir = dir
	return inv, opts
}

type fakeOps struct {
	mu           sync.Mutex
	axes         []axis
	preparations []map[string]float32
	styles       []struct {
		weight          int
		pixel, outlines bool
	}
	merges, conversions, subsets int
	drop                         rune
	padRegistration              bool
}

func (f *fakeOps) Prepare(ctx context.Context, data []byte, index uint32, axes map[string]float32, outlines bool) ([]byte, error) {
	f.mu.Lock()
	f.preparations = append(f.preparations, cloneAxes(axes))
	f.mu.Unlock()
	parsed, err := sfntio.Parse(data, int(index))
	if err != nil {
		return nil, err
	}
	if binary.BigEndian.Uint16(parsed.Tables["head"][18:]) < 1000 {
		binary.BigEndian.PutUint16(parsed.Tables["head"][18:], 1024)
	}
	if weight, ok := axes["wght"]; ok {
		binary.BigEndian.PutUint16(parsed.Tables["OS/2"][4:], uint16(weight))
	}
	return parsed.Bytes()
}
func (f *fakeOps) PrepareAtlasFallback(ctx context.Context, data []byte, outlines bool) ([]byte, error) {
	return data, ctx.Err()
}
func (f *fakeOps) Style(ctx context.Context, data []byte, weight int, pixel, outlines bool) ([]byte, error) {
	f.mu.Lock()
	f.styles = append(f.styles, struct {
		weight          int
		pixel, outlines bool
	}{weight, pixel, outlines})
	f.mu.Unlock()
	p, err := sfntio.Parse(data, 0)
	if err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint16(p.Tables["OS/2"][4:], uint16(weight))
	return p.Bytes()
}
func (f *fakeOps) Merge(ctx context.Context, data []byte, fallbacks [][]byte) ([]byte, error) {
	f.mu.Lock()
	f.merges++
	f.mu.Unlock()
	var faces []*inventory.Face
	for _, raw := range append([][]byte{data}, fallbacks...) {
		face, err := inventory.InspectFace(raw)
		if err != nil {
			return nil, err
		}
		faces = append(faces, face)
	}
	set := unionFaces(faces...)
	delete(set, f.drop)
	var chars []rune
	for cp := range set {
		chars = append(chars, cp)
	}
	return syntheticFont("Synthetic merged", int(faces[0].Weight), faces[0].UPM, chars)
}
func (f *fakeOps) Conform(ctx context.Context, data []byte, ttf bool, upm uint16, layout []byte, outlines bool) ([]byte, [3]float32, error) {
	f.mu.Lock()
	f.conversions++
	f.mu.Unlock()
	p, err := sfntio.Parse(data, 0)
	if err != nil {
		return nil, [3]float32{}, err
	}
	binary.BigEndian.PutUint16(p.Tables["head"][18:], upm)
	if !ttf {
		p.Version = 0x4f54544f
	}
	result, err := p.Bytes()
	return result, [3]float32{float32(upm) * .75, -float32(upm) * .25, float32(upm)}, err
}
func (f *fakeOps) Register(data, template []byte) ([]byte, error) {
	p, err := sfntio.Parse(data, 0)
	if err != nil {
		return nil, err
	}
	original, err := sfntio.Parse(template, 0)
	if err != nil {
		return nil, err
	}
	p.Tables["name"] = original.Tables["name"]
	copy(p.Tables["OS/2"][4:8], original.Tables["OS/2"][4:8])
	result, err := p.Bytes()
	if err == nil && f.padRegistration {
		result = append(result, 0, 0, 0)
	}
	return result, err
}
func (f *fakeOps) Axes([]byte, int) ([]axis, error) { return f.axes, nil }
func (f *fakeOps) Subset(ctx context.Context, data []byte, chars []rune) ([]byte, error) {
	f.mu.Lock()
	f.subsets++
	f.mu.Unlock()
	face, err := inventory.InspectFace(data)
	if err != nil {
		return nil, err
	}
	return syntheticFont(face.Name, int(face.Weight), face.UPM, chars)
}

type fakeAtlas struct {
	mu    sync.Mutex
	calls [][]atlas.Source
	keys  [][]rune
}

func (f *fakeAtlas) generate(ctx context.Context, sources []atlas.Source, opts atlas.Options) (*atlas.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, sources)
	f.keys = append(f.keys, opts.Keys)
	f.mu.Unlock()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	copy(img.Pix, []byte{9, 31, 240, 0, 5, 6, 7, 8, 90, 91, 92, 93, 200, 201, 202, 203})
	glyphs := make(map[rune]atlas.Glyph)
	report := atlas.Report{DefaultGlyphCharacters: []string{}}
	for _, source := range sources {
		data, err := os.ReadFile(source.FontPath)
		if err != nil {
			return nil, err
		}
		face, err := inventory.InspectFace(data)
		if err != nil {
			return nil, err
		}
		for _, cp := range source.Charset {
			if _, ok := face.Chars[cp]; !ok {
				return nil, fmt.Errorf("fake atlas unsupported codepoint")
			}
			glyphs[cp] = atlas.Glyph{Unicode: cp, Advance: .5}
		}
		report.Sources = append(report.Sources, atlas.SourceReport{Font: source.Name, Characters: len(source.Charset), Custom: source.Custom})
	}
	return &atlas.Result{Image: img, Pixels: img.Pix, Glyphs: glyphs, Report: report}, nil
}
