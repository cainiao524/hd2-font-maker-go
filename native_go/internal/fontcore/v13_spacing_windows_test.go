//go:build windows && amd64

package fontcore

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"

	"hd2font/native/internal/sfntio"
)

// Version 1.3 keeps authored advances, kerning and line metrics during weight
// synthesis. This regression checks actual shaping rather than a stroke-width
// formula, so a port cannot silently squeeze or expand layout cells.
func TestV13WeightKeepsAuthoredSpacingAndShaping(t *testing.T) {
	p := processor(t)
	ctx := context.Background()
	for _, outlines := range []bool{false, true} {
		base, err := p.Prepare(ctx, readTest(t, "testdata/layout.ttf"), 0, nil, outlines)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := sfntio.Parse(base, 0)
		for _, weight := range []int{500, 700} {
			t.Run(fmt.Sprintf("outlines=%v/weight=%d", outlines, weight), func(t *testing.T) {
				styled, err := p.Style(ctx, base, weight, false, outlines)
				if err != nil {
					t.Fatal(err)
				}
				after, err := sfntio.Parse(styled, 0)
				if err != nil {
					t.Fatal(err)
				}
				for _, tag := range []string{"GSUB", "GPOS", "GDEF", "cmap"} {
					if !bytes.Equal(before.Tables[tag], after.Tables[tag]) {
						t.Fatalf("weight changed %s", tag)
					}
				}
				bm, _ := fontMetrics(before)
				am, _ := fontMetrics(after)
				if !reflect.DeepEqual(bm.advances, am.advances) {
					t.Fatal("weight changed authored advances")
				}
				if !bytes.Equal(before.Tables["hhea"][4:10], after.Tables["hhea"][4:10]) || !bytes.Equal(before.Tables["OS/2"][68:74], after.Tables["OS/2"][68:74]) {
					t.Fatal("weight changed authored line metrics")
				}
				for _, features := range [][]string{{"liga=0", "kern=0"}, {"liga=0", "kern=1"}, {"liga=1", "kern=1"}} {
					want := shape(t, p, base, "AV", features...)
					got := shape(t, p, styled, "AV", features...)
					if !reflect.DeepEqual(want, got) {
						t.Fatalf("weight changed shaping: %v != %v", got, want)
					}
				}
			})
		}
	}
}

func TestV13AtlasFallbackRetainsSmallOriginalUPM(t *testing.T) {
	p := processor(t)
	data := maxpTestFont(t, 'A', 1, 256, 4)
	for _, outlines := range []bool{false, true} {
		out, err := p.PrepareAtlasFallback(context.Background(), data, outlines)
		if err != nil {
			t.Fatal(err)
		}
		font, err := sfntio.Parse(out, 0)
		if err != nil {
			t.Fatal(err)
		}
		metrics, err := fontMetrics(font)
		if err != nil {
			t.Fatal(err)
		}
		if metrics.upm != 256 || metrics.advances[1] != 20 {
			t.Fatal("atlas donor was scaled to the custom-font grid")
		}
		if !outlines && !bytes.Equal(data, out) {
			t.Fatal("raw donor outlines changed")
		}
	}
}
