//go:build windows && amd64

package fontcore

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/tdewolff/font"
	"hd2font/native/internal/geometry"
	"hd2font/native/internal/sfntio"
)

func TestWeightPreservesLayoutAndCoverage(t *testing.T) {
	data, err := os.ReadFile("testdata/layout.ttf")
	if err != nil {
		t.Fatal(err)
	}
	engine, err := geometry.Open(filepath.Join("..", "..", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	before, err := sfntio.Parse(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	one, report, err := Weight(context.Background(), engine, data, WeightOptions{Weight: 700, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	many, _, err := Weight(context.Background(), engine, data, WeightOptions{Weight: 700, Workers: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, many) {
		t.Fatal("worker count changes output")
	}
	after, err := sfntio.Parse(one, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"cmap", "GSUB", "GPOS", "GDEF", "name", "post"} {
		if !bytes.Equal(before.Tables[tag], after.Tables[tag]) {
			t.Errorf("%s changed", tag)
		}
	}
	f, err := font.ParseSFNT(one, 0)
	if err != nil {
		t.Fatal(err)
	}
	if int(f.NumGlyphs()) != report.Glyphs || binary.BigEndian.Uint16(after.Tables["OS/2"][4:]) != 700 {
		t.Fatal("font metrics mismatch")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Weight(ctx, engine, data, WeightOptions{Weight: 700}); err == nil {
		t.Fatal("canceled job succeeded")
	}
	for _, invalid := range []WeightOptions{{Weight: 0}, {Weight: 1001}} {
		if _, _, err := Weight(context.Background(), engine, data, invalid); err == nil {
			t.Fatalf("accepted unsupported options %+v", invalid)
		}
	}
	if _, _, err := Weight(context.Background(), engine, data, WeightOptions{Weight: 700, ProcessOutlines: true}); err != nil {
		t.Fatal(err)
	}
	cleanOnly, _, err := Weight(context.Background(), engine, data, WeightOptions{Weight: 400, ProcessOutlines: true})
	if err != nil {
		t.Fatal(err)
	}
	cleanFont, err := sfntio.Parse(cleanOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before.Tables["OS/2"][4:6], cleanFont.Tables["OS/2"][4:6]) {
		t.Fatal("cleanup-only changed authored weight")
	}
	identity, _, err := Weight(context.Background(), engine, data, WeightOptions{Weight: 400})
	if err != nil || !bytes.Equal(data, identity) {
		t.Fatal("normal weight changed authored font")
	}
}
