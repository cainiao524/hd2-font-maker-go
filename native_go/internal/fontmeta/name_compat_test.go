package fontmeta

import (
	"encoding/binary"
	"os"
	"reflect"
	"testing"

	"hd2font/native/internal/inventory"
)

func TestCIDNameLanguageSentinel(t *testing.T) {
	for _, withTags := range []bool{false, true} {
		records := []nameRecord{{platform: 1, encoding: 1, language: 0xffff, id: 20, value: []byte("Example-Regular-83pv-RKSJ-H")}}
		if withTags {
			records = append(records, nameRecord{platform: 0, encoding: 4, language: 0x8000, id: 1, value: encodeUnicode("Family"), languageTag: encodeUnicode("ja")})
		}
		data, err := writeNames(records)
		if err != nil {
			t.Fatal(err)
		}
		got, err := readNames(data)
		if err != nil || !reflect.DeepEqual(got, records) {
			t.Fatalf("CID sentinel, tags=%v: %v %v", withTags, got, err)
		}
	}
}

func TestFormatOneStillRejectsMissingLanguageTag(t *testing.T) {
	records := []nameRecord{{platform: 0, encoding: 4, language: 0x8000, id: 1, value: encodeUnicode("Family"), languageTag: encodeUnicode("ja")}}
	data, err := writeNames(records)
	if err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint16(data[10:], 0x8001)
	if _, err := readNames(data); err == nil {
		t.Fatal("accepted out-of-range language-tag reference")
	}
}

func TestRegisterLocalGameTemplates(t *testing.T) {
	path := os.Getenv("HD2_LOCAL_INVENTORY")
	if path == "" {
		t.Skip("set HD2_LOCAL_INVENTORY for the local game-name compatibility check")
	}
	inv, err := inventory.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	source := fixture(t, "donor.ttf")
	for _, face := range inv.OrderedFaces() {
		t.Run(face.Name, func(t *testing.T) {
			f := parse(t, face.Data)
			b := f.Tables["name"]
			for i := 0; i < int(binary.BigEndian.Uint16(b[2:])); i++ {
				at := 6 + 12*i
				if language := binary.BigEndian.Uint16(b[at+4:]); language >= 0x8000 {
					t.Logf("format=%d platform=%d language=0x%04x nameID=%d", binary.BigEndian.Uint16(b), binary.BigEndian.Uint16(b[at:]), language, binary.BigEndian.Uint16(b[at+6:]))
				}
			}
			if _, err := Register(source, face.Data); err != nil {
				t.Fatal(err)
			}
		})
	}
}
