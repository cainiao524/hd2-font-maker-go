package cffconv

import (
	"slices"
	"testing"

	"hd2font/native/internal/geometry"
)

func TestFractionalNormalizationKeepsGrid(t *testing.T) {
	var p geometry.Path
	p.MoveTo(.125, .375)
	p.LineTo(12.5, 7.25)
	p.CubeTo(15.125, 30.25, 40.375, -5.5, 50.625, 2.25)
	p.Close()
	p.MoveTo(-.5, -.25)
	p.LineTo(5.5, 8.75)
	p.Close()
	encoded, err := EncodeFractional(p, 600)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeLiteral(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p, decoded) {
		t.Fatalf("fractional coordinates changed: %v", decoded)
	}
}
