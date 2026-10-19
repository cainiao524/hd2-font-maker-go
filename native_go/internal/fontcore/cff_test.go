package fontcore

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/tdewolff/font"
	"hd2font/native/internal/sfntio"
)

func TestToCFFPreservesLayoutAndGlyphIDs(t *testing.T) {
	data, err := os.ReadFile("testdata/layout.ttf")
	if err != nil {
		t.Fatal(err)
	}
	one, r, err := ToCFF(context.Background(), data, 1)
	if err != nil {
		t.Fatal(err)
	}
	many, _, err := ToCFF(context.Background(), data, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, many) {
		t.Fatal("worker count affects CFF")
	}
	before, err := sfntio.Parse(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	after, err := sfntio.Parse(one, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"cmap", "GSUB", "GPOS", "GDEF", "name", "hmtx", "hhea", "OS/2"} {
		if !bytes.Equal(before.Tables[tag], after.Tables[tag]) {
			t.Fatalf("%s changed", tag)
		}
	}
	f, err := font.ParseSFNT(one, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !f.IsCFF || int(f.NumGlyphs()) != r.Glyphs || f.UnitsPerEm() != r.UnitsPerEm {
		t.Fatal("CFF format mismatch")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = ToCFF(ctx, data, 1); err == nil {
		t.Fatal("canceled conversion succeeded")
	}
}
