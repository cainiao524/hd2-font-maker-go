package sfntio

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"hd2font/native/internal/geometry"
)

type oracleFixture struct {
	IntegrationFont string `json:"integration_font"`
	Fonts           []struct {
		Version  uint32            `json:"version"`
		Tables   map[string]string `json:"tables"`
		Expected string            `json:"expected"`
	} `json:"fonts"`
	Glyphs []struct {
		Name            string      `json:"name"`
		Commands        [][]float64 `json:"commands"`
		Bounds          [4]int16    `json:"bounds"`
		Coordinates     [][2]int16  `json:"coordinates"`
		Ends            []uint16    `json:"ends"`
		FontToolsBytes  string      `json:"fonttools_bytes"`
		DecodedCommands [][]float64 `json:"decoded_commands"`
	} `json:"glyphs"`
}

func fixture(t *testing.T) oracleFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/fonttools_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var result oracleFixture
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func unhex(t *testing.T, text string) []byte {
	t.Helper()
	data, err := hex.DecodeString(text)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func commands(rows [][]float64) geometry.Path {
	var path geometry.Path
	for _, row := range rows {
		command := geometry.Command{Verb: uint32(row[0])}
		for i, value := range row[1:] {
			command.Points[i] = float32(value)
		}
		path = append(path, command)
	}
	return path
}

func record(data []byte, tag string) []byte {
	for i := 0; i < int(binary.BigEndian.Uint16(data[4:])); i++ {
		r := data[12+16*i : 28+16*i]
		if string(r[:4]) == tag {
			return r
		}
	}
	return nil
}

func TestFontToolsSFNTGolden(t *testing.T) {
	for _, sample := range fixture(t).Fonts {
		t.Run(fmt.Sprintf("%08x", sample.Version), func(t *testing.T) {
			f := &Font{Version: sample.Version, Tables: make(map[string][]byte)}
			for tag, text := range sample.Tables {
				f.Tables[tag] = unhex(t, text)
			}
			data, err := f.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, unhex(t, sample.Expected)) {
				t.Fatal("standalone font differs from FontTools SFNTWriter bytes")
			}
			if checksum(data) != 0xb1b0afba {
				t.Fatal("whole-font checksum differs")
			}
			parsed, err := Parse(data, 0)
			if err != nil || parsed.Version != f.Version {
				t.Fatalf("parse: %v", err)
			}
			for tag, table := range parsed.Tables {
				original := unhex(t, sample.Tables[tag])
				if !bytes.Equal(original, f.Tables[tag]) {
					t.Fatalf("Bytes mutated input table %s", tag)
				}
				payload := bytes.Clone(table)
				if tag == "head" {
					clear(payload[8:12])
					clear(original[8:12])
				}
				if !bytes.Equal(payload, original) {
					t.Errorf("table %q changed", tag)
				}
				r := record(data, tag)
				if checksum(payload) != binary.BigEndian.Uint32(r[4:]) || binary.BigEndian.Uint32(r[8:])%4 != 0 || int(binary.BigEndian.Uint32(r[12:])) != len(table) {
					t.Errorf("table %q checksum/alignment/length differs", tag)
				}
			}
			roundTrip, err := parsed.Bytes()
			if err != nil || !bytes.Equal(roundTrip, data) {
				t.Fatalf("round trip differs: %v", err)
			}
			parsed.Tables["GSUB"][0] ^= 1
			if bytes.Equal(parsed.Tables["GSUB"], f.Tables["GSUB"]) || !bytes.Equal(data, unhex(t, sample.Expected)) {
				t.Fatal("parsed tables alias source data")
			}
		})
	}
}

func makeCollection(t *testing.T, version uint32) []byte {
	t.Helper()
	fixtures := fixture(t).Fonts
	first, second := unhex(t, fixtures[0].Expected), unhex(t, fixtures[1].Expected)
	headerSize := 20
	if version == 0x20000 {
		headerSize += 12
	}
	data := make([]byte, headerSize)
	copy(data, "ttcf")
	binary.BigEndian.PutUint32(data[4:], version)
	binary.BigEndian.PutUint32(data[8:], 2)
	binary.BigEndian.PutUint32(data[12:], uint32(headerSize))
	binary.BigEndian.PutUint32(data[16:], uint32(headerSize+len(first)))
	var sharedOffset uint32
	for i, face := range [][]byte{first, second} {
		base := uint32(len(data))
		for j := 0; j < int(binary.BigEndian.Uint16(face[4:])); j++ {
			r := face[12+16*j:]
			offset := base + binary.BigEndian.Uint32(r[8:])
			if string(r[:4]) == "GSUB" {
				if i == 0 {
					sharedOffset = offset
				} else {
					offset = sharedOffset
				}
			}
			binary.BigEndian.PutUint32(r[8:], offset)
		}
		data = append(data, face...)
	}
	return data
}

func TestTTCAbsoluteOffsetsSharedTablesAndSelection(t *testing.T) {
	for _, version := range []uint32{0x10000, 0x20000} {
		data := makeCollection(t, version)
		for i, expected := range fixture(t).Fonts[:2] {
			face, err := Parse(data, i)
			if err != nil {
				t.Fatal(err)
			}
			output, err := face.Bytes()
			if err != nil || !bytes.Equal(output, unhex(t, expected.Expected)) {
				t.Fatalf("TTC v%x face %d differs: %v", version, i, err)
			}
		}
		first, _ := Parse(data, 0)
		second, _ := Parse(data, 1)
		first.Tables["GSUB"][0] ^= 1
		if bytes.Equal(first.Tables["GSUB"], second.Tables["GSUB"]) {
			t.Fatal("shared TTC table copies alias")
		}
		if _, err := Parse(data, 2); err == nil {
			t.Error("accepted out-of-range TTC face")
		}
	}
}

func TestMalformedSFNTAndTTC(t *testing.T) {
	good := unhex(t, fixture(t).Fonts[0].Expected)
	for _, mutate := range []func([]byte) []byte{
		func(p []byte) []byte { return p[:11] },
		func(p []byte) []byte { p[0] = 255; return p },
		func(p []byte) []byte { binary.BigEndian.PutUint16(p[4:], 65535); return p },
		func(p []byte) []byte { copy(p[28:32], p[12:16]); return p },
		func(p []byte) []byte { binary.BigEndian.PutUint32(p[20:], math.MaxUint32); return p },
		func(p []byte) []byte { binary.BigEndian.PutUint32(p[24:], math.MaxUint32); return p },
		func(p []byte) []byte { binary.BigEndian.PutUint32(p[20:], 0); return p },
	} {
		if _, err := Parse(mutate(bytes.Clone(good)), 0); err == nil {
			t.Error("accepted malformed SFNT")
		}
	}
	for _, index := range []int{-1, 1} {
		if _, err := Parse(good, index); err == nil {
			t.Errorf("accepted standalone index %d", index)
		}
	}
	ttc := makeCollection(t, 0x20000)
	for _, mutate := range []func([]byte) []byte{
		func(p []byte) []byte { binary.BigEndian.PutUint32(p[4:], 0x30000); return p },
		func(p []byte) []byte { binary.BigEndian.PutUint32(p[8:], math.MaxUint32); return p },
		func(p []byte) []byte { return p[:20] },
		func(p []byte) []byte { binary.BigEndian.PutUint32(p[12:], 0); return p },
		func(p []byte) []byte { binary.BigEndian.PutUint32(p[12:], math.MaxUint32); return p },
	} {
		if _, err := Parse(mutate(bytes.Clone(ttc)), 0); err == nil {
			t.Error("accepted malformed TTC")
		}
	}
	for _, f := range []*Font{nil, {}, {Version: 1}, {Version: 0x10000}, {Version: 0x10000, Tables: map[string][]byte{"head": make([]byte, 11)}}, {Version: 0x10000, Tables: map[string][]byte{"head": make([]byte, 54), "bad": nil}}} {
		if _, err := f.Bytes(); err == nil {
			t.Error("accepted unencodable font")
		}
	}
	tables := map[string][]byte{"head": make([]byte, 54)}
	for i := range 4096 {
		tables[fmt.Sprintf("%04x", i)] = nil
	}
	if _, err := (&Font{Version: 0x10000, Tables: tables}).Bytes(); err == nil {
		t.Error("accepted overflowing searchRange")
	}
}

func TestParserRepairsStaleChecksumsAndUnsortedDirectory(t *testing.T) {
	expected := unhex(t, fixture(t).Fonts[0].Expected)
	data := bytes.Clone(expected)
	first := bytes.Clone(data[12:28])
	copy(data[12:28], data[28:44])
	copy(data[28:44], first)
	clear(data[16:20])
	head := record(data, "head")
	offset := int(binary.BigEndian.Uint32(head[8:]))
	clear(data[offset+8 : offset+12])
	f, err := Parse(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	output, err := f.Bytes()
	if err != nil || !bytes.Equal(output, expected) {
		t.Fatalf("checksum repair/reordering: %v", err)
	}
}

func TestPathopsDefaultPenGlyphOracle(t *testing.T) {
	for _, sample := range fixture(t).Glyphs {
		t.Run(sample.Name, func(t *testing.T) {
			path := commands(sample.Commands)
			before := append(geometry.Path(nil), path...)
			glyph, err := EncodeGlyph(path)
			if err != nil {
				t.Fatal(err)
			}
			if glyph.Bounds != sample.Bounds || int(glyph.Points) != len(sample.Coordinates) || int(glyph.Contours) != len(sample.Ends) {
				t.Fatalf("oracle metadata differs: bounds %v/%v, points %d/%d, contours %d/%d", glyph.Bounds, sample.Bounds, glyph.Points, len(sample.Coordinates), glyph.Contours, len(sample.Ends))
			}
			want := commands(sample.DecodedCommands)
			for _, data := range [][]byte{glyph.Data, unhex(t, sample.FontToolsBytes)} {
				decoded, err := DecodeGlyph(data)
				if err != nil || !reflect.DeepEqual(decoded, want) {
					t.Fatalf("FontTools-drawn path differs: got %+v, want %+v, err %v", decoded, want, err)
				}
			}
			if !reflect.DeepEqual(path, before) {
				t.Fatal("encoder changed input path")
			}
			// Quantization cleanup calls this without reparsing the whole font.
			for range 8 {
				decoded, err := DecodeGlyph(glyph.Data)
				if err != nil {
					t.Fatal(err)
				}
				next, err := EncodeGlyph(decoded)
				if err != nil {
					t.Fatal(err)
				}
				nextPath, err := DecodeGlyph(next.Data)
				if err != nil || !reflect.DeepEqual(nextPath, want) {
					t.Fatalf("cleanup round trip changed quantized outline: %v", err)
				}
				glyph = next
			}
		})
	}
}

func TestGlyphRepeatFlagsAndOpenContours(t *testing.T) {
	var path geometry.Path
	path.MoveTo(0, 0)
	for i := 1; i <= 600; i++ {
		path.LineTo(float64(i), 0)
	}
	glyph, err := EncodeGlyph(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(glyph.Data) >= 1200 || glyph.Points != 601 {
		t.Fatal("repeated flags were not packed")
	}
	path.Close()
	decoded, err := DecodeGlyph(glyph.Data)
	if err != nil || !reflect.DeepEqual(path, decoded) {
		t.Fatalf("repeat flag/open contour round trip: %v", err)
	}
	open := commands([][]float64{{0, 0, 0}, {1, 1, 0}, {0, 5, 5}, {1, 6, 6}})
	closed := commands([][]float64{{0, 0, 0}, {1, 1, 0}, {4}, {0, 5, 5}, {1, 6, 6}, {4}})
	a, err := EncodeGlyph(open)
	if err != nil {
		t.Fatal(err)
	}
	b, err := EncodeGlyph(closed)
	if err != nil || !bytes.Equal(a.Data, b.Data) {
		t.Fatalf("implicit closure differs: %v", err)
	}
}

func TestGlyphInputErrorsAndLimits(t *testing.T) {
	for _, path := range []geometry.Path{
		{{Verb: geometry.Cubic}}, {{Verb: 99}}, {{Verb: geometry.Line}}, {{Verb: geometry.Quad}},
		{{Verb: geometry.Move, Points: [6]float32{float32(math.NaN()), 0}}},
		{{Verb: geometry.Move, Points: [6]float32{0, float32(math.Inf(1))}}},
		{{Verb: geometry.Move, Points: [6]float32{32767.5, 0}}},
		{{Verb: geometry.Move, Points: [6]float32{-32768.51, 0}}},
		{{Verb: geometry.Move, Points: [6]float32{-32768, 0}}, {Verb: geometry.Line, Points: [6]float32{32767, 0}}},
		{{Verb: geometry.Move}, {Verb: geometry.Close}, {Verb: geometry.Line}},
	} {
		if _, err := EncodeGlyph(path); err == nil {
			t.Errorf("accepted unsupported path %+v", path)
		}
	}
	var path geometry.Path
	path.MoveTo(-1, 0)
	for i := 1; i < 65535; i++ {
		path.LineTo(float64(i%2), 0)
	}
	if glyph, err := EncodeGlyph(path); err != nil || glyph.Points != 65535 {
		t.Fatalf("maximum point count: %d, %v", glyph.Points, err)
	}
	path.LineTo(2, 0)
	if _, err := EncodeGlyph(path); err == nil {
		t.Error("accepted point count overflow")
	}
	path = nil
	for range 32768 {
		path.MoveTo(0, 0)
		path.LineTo(0, 0)
		path.Close()
	}
	if _, err := EncodeGlyph(path); err == nil {
		t.Error("accepted signed contour count overflow")
	}
}

func rawGlyph(points [][2]int16, on []bool) []byte {
	data := make([]byte, 10)
	binary.BigEndian.PutUint16(data, 1)
	data = binary.BigEndian.AppendUint16(data, uint16(len(points)-1))
	data = binary.BigEndian.AppendUint16(data, 0)
	for _, value := range on {
		if value {
			data = append(data, 1)
		} else {
			data = append(data, 0)
		}
	}
	for axis := range 2 {
		previous := int32(0)
		for _, point := range points {
			value := int32(point[axis])
			data = binary.BigEndian.AppendUint16(data, uint16(int16(value-previous)))
			previous = value
		}
	}
	return data
}

func TestDecodeStartChoiceAndLargeMidpoints(t *testing.T) {
	data := rawGlyph([][2]int16{{10, 10}, {20, 0}}, []bool{false, true})
	want := commands([][]float64{{0, 20, 0}, {2, 10, 10, 20, 0}, {4}})
	path, err := DecodeGlyph(data)
	if err != nil || !reflect.DeepEqual(path, want) {
		t.Fatalf("last on-curve start: %+v, %v", path, err)
	}
	data = rawGlyph([][2]int16{{32000, 32000}, {32767, 32767}, {32003, 32766}}, []bool{false, false, false})
	want = commands([][]float64{{0, 32001.5, 32383}, {2, 32000, 32000, 32383.5, 32383.5}, {2, 32767, 32767, 32385, 32766.5}, {2, 32003, 32766, 32001.5, 32383}, {4}})
	path, err = DecodeGlyph(data)
	if err != nil || !reflect.DeepEqual(path, want) {
		t.Fatalf("large implied midpoint overflow/start choice: %+v, %v", path, err)
	}
	data = rawGlyph([][2]int16{{-32000, -32000}, {-32767, -32767}, {-32003, -32766}}, []bool{false, false, false})
	path, err = DecodeGlyph(data)
	if err != nil || path[0].Points[0] != -32001.5 || path[1].Points[2] != -32383.5 {
		t.Fatalf("negative implied midpoint overflow: %+v, %v", path, err)
	}
}

func TestDecodeMalformedAndEmptyGlyphs(t *testing.T) {
	for _, data := range [][]byte{nil, make([]byte, 10), make([]byte, 12), append(make([]byte, 11), 2, 1, 2)} {
		if path, err := DecodeGlyph(data); err != nil || len(path) != 0 {
			t.Fatalf("empty glyph: %v", err)
		}
	}
	good := rawGlyph([][2]int16{{32767, 0}, {32766, 0}}, []bool{true, true})
	for length := 1; length < len(good); length++ {
		if _, err := DecodeGlyph(good[:length]); err == nil {
			t.Errorf("accepted truncated glyph at byte %d", length)
		}
	}
	for _, mutate := range []func([]byte) []byte{
		func(p []byte) []byte { p[0], p[1] = 255, 255; return p },
		func(p []byte) []byte { p[12], p[13] = 255, 255; return p },
		func(p []byte) []byte { p[14] = 128; return p },
		func(p []byte) []byte { p[14], p[15] = 8, 255; return p },
		func(p []byte) []byte { binary.BigEndian.PutUint16(p[18:], 1); return p },
		func(p []byte) []byte {
			binary.BigEndian.PutUint16(p, 2)
			binary.BigEndian.PutUint16(p[10:], 1)
			binary.BigEndian.PutUint16(p[12:], 1)
			return p
		},
	} {
		if _, err := DecodeGlyph(mutate(bytes.Clone(good))); err == nil {
			t.Error("accepted malformed glyph")
		}
	}
	if _, err := DecodeGlyph(append(bytes.Clone(good), 0, 0, 0)); err != nil {
		t.Fatalf("rejected glyph padding: %v", err)
	}
}

func TestSameIDReplacementPreservesLayoutAndMappings(t *testing.T) {
	data := unhex(t, fixture(t).IntegrationFont)
	original, err := sfnt.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	before := make(map[string][]byte)
	for tag, table := range f.Tables {
		before[tag] = bytes.Clone(table)
	}
	var path geometry.Path
	path.MoveTo(0, 0)
	path.LineTo(150, 0)
	path.LineTo(150, 100)
	path.LineTo(0, 100)
	path.Close()
	replacement, err := EncodeGlyph(path)
	if err != nil {
		t.Fatal(err)
	}
	count := int(binary.BigEndian.Uint16(f.Tables["maxp"][4:]))
	loca := f.Tables["loca"]
	short := binary.BigEndian.Uint16(f.Tables["head"][50:]) == 0
	oldOffset := func(i int) int {
		if short {
			return int(binary.BigEndian.Uint16(loca[2*i:])) * 2
		}
		return int(binary.BigEndian.Uint32(loca[4*i:]))
	}
	var glyf, offsets []byte
	for id := 0; id < count; id++ {
		offsets = binary.BigEndian.AppendUint32(offsets, uint32(len(glyf)))
		if id == 1 {
			glyf = append(glyf, replacement.Data...)
		} else {
			glyf = append(glyf, f.Tables["glyf"][oldOffset(id):oldOffset(id+1)]...)
		}
		if len(glyf)%2 != 0 {
			glyf = append(glyf, 0)
		}
	}
	offsets = binary.BigEndian.AppendUint32(offsets, uint32(len(glyf)))
	f.Tables["glyf"], f.Tables["loca"] = glyf, offsets
	binary.BigEndian.PutUint16(f.Tables["head"][50:], 1)
	output, err := f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	updated, err := sfnt.Parse(output)
	if err != nil {
		t.Fatalf("independent SFNT parser rejected replacement: %v", err)
	}
	saved, err := Parse(output, 0)
	if err != nil {
		t.Fatal(err)
	}
	for tag, table := range before {
		if tag != "glyf" && tag != "loca" && tag != "head" && !bytes.Equal(saved.Tables[tag], table) {
			t.Errorf("untouched table %s changed", tag)
		}
	}
	if original.NumGlyphs() != updated.NumGlyphs() || updated.NumGlyphs() != 4 {
		t.Fatal("glyph IDs were subset or reordered")
	}
	var buffer sfnt.Buffer
	for _, code := range []rune{'A', 'B'} {
		oldID, err := original.GlyphIndex(&buffer, code)
		if err != nil {
			t.Fatal(err)
		}
		newID, err := updated.GlyphIndex(&buffer, code)
		if err != nil || oldID != newID {
			t.Fatalf("glyph mapping changed: %v", err)
		}
	}
	ppem := fixed.I(1000)
	beforeKern, err := original.Kern(&buffer, 1, 2, ppem, font.HintingNone)
	if err != nil {
		t.Fatal(err)
	}
	afterKern, err := updated.Kern(&buffer, 1, 2, ppem, font.HintingNone)
	if err != nil || beforeKern != afterKern || afterKern != fixed.I(-25) {
		t.Fatalf("GPOS kerning changed: %v/%v, %v", beforeKern, afterKern, err)
	}
	bounds, _, err := updated.GlyphBounds(&buffer, 1, ppem, font.HintingNone)
	if err != nil || bounds.Max.X != fixed.I(150) || bounds.Min.Y != fixed.I(-100) {
		t.Fatalf("independent glyph bounds: %v, %v", bounds, err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte{}, 0)
	f.Add([]byte("ttcf\x00\x01\x00\x00\x00\x00\x00\x00"), 0)
	f.Fuzz(func(t *testing.T, data []byte, index int) { _, _ = Parse(data, index) })
}

func FuzzDecodeGlyph(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 10))
	f.Add(rawGlyph([][2]int16{{1, 2}, {3, 4}}, []bool{false, true}))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = DecodeGlyph(data) })
}
