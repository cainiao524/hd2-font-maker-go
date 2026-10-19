package atlas

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image"
	"testing"
)

func textureFixture(width, height, format uint32) []byte {
	b := bytes.Repeat([]byte{0xa5}, 340)
	copy(b[192:], "DDS ")
	binary.LittleEndian.PutUint32(b[204:], height)
	binary.LittleEndian.PutUint32(b[208:], width)
	binary.LittleEndian.PutUint32(b[320:], format)
	return b
}

func TestPatchTextureHeader(t *testing.T) {
	template := textureFixture(64, 64, 28)
	before := bytes.Clone(template)
	header, err := PatchTextureHeader(template, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(template, before) {
		t.Fatal("mutated template")
	}
	for i, v := range header[:192] {
		want := byte(0)
		if i >= 8 && i < 12 {
			want = 255
		}
		if v != want {
			t.Errorf("wrapper byte %d=%d want %d", i, v, want)
		}
	}
	for off, want := range map[int]uint32{204: 4, 208: 8, 212: 32, 220: 4, 320: 28} {
		if got := binary.LittleEndian.Uint32(header[off:]); got != want {
			t.Errorf("DDS field %d=%d want %d", off, got, want)
		}
	}
	for i := 192; i < 340; i++ {
		if i >= 204 && i < 216 || i >= 220 && i < 224 {
			continue
		}
		if header[i] != before[i] {
			t.Errorf("unexpected DDS field change at %d", i)
		}
	}
	for _, bad := range [][]byte{nil, template[:339], textureFixture(2, 2, 71)} {
		if _, err := PatchTextureHeader(bad, 2, 2); err == nil {
			t.Fatal("accepted unsupported texture")
		}
	}
	if _, err := PatchTextureHeader(template, 0, 2); err == nil {
		t.Fatal("accepted empty dimensions")
	}
}

func TestTexturePixelsStraightChannelsAndRounding(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	copy(img.Pix, []byte{255, 0, 0, 0, 0, 1, 0, 255, 0, 0, 0, 0, 0, 0, 1, 0})
	got, err := TexturePixels(img)
	if err != nil {
		t.Fatal(err)
	}
	want := append(bytes.Clone(img.Pix), 64, 1, 1, 64)
	if !bytes.Equal(got, want) {
		t.Fatalf("mips=%v want %v", got, want)
	}
	// A four-way average would round green and blue to zero. Alpha-weighted
	// averaging would discard the red value from the fully transparent pixel.
	parent := image.NewNRGBA(image.Rect(10, 20, 14, 24))
	sub := parent.SubImage(image.Rect(11, 21, 13, 23)).(*image.NRGBA)
	for y := 0; y < 2; y++ {
		copy(sub.Pix[y*sub.Stride:y*sub.Stride+8], img.Pix[y*8:y*8+8])
	}
	stridePixels, err := TexturePixels(sub)
	if err != nil || !bytes.Equal(stridePixels, want) {
		t.Fatalf("stride/subimage handling: %v, %v", stridePixels, err)
	}
}

func TestTexturePartsAndSelection(t *testing.T) {
	template := textureFixture(16, 32, 28)
	img := image.NewNRGBA(image.Rect(0, 0, 8, 2))
	header, stream, gpu, err := TextureParts(template, img, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(header) != 340 || len(stream) != 0 || len(gpu) != (16+4+2+1)*4 {
		t.Fatal("incorrect texture parts sizes")
	}
	if _, _, cached, err := TextureParts(template, img, gpu); err != nil || !bytes.Equal(cached, gpu) {
		t.Fatal("precomputed pixels failed")
	}
	if _, _, _, err := TextureParts(template, img, gpu[:4]); err == nil {
		t.Fatal("accepted truncated mip chain")
	}
	if _, err := TexturePixels(nil); err == nil {
		t.Fatal("accepted nil image")
	}
	textures := []Texture{{ID: 1, Payload: textureFixture(1, 1, 71)}, {ID: 2, Payload: template}}
	got, err := AtlasTextures(textures)
	if err != nil || len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("selection=%v err=%v", got, err)
	}
	textures[1].Payload = textureFixture(2, 2, 71)
	if _, err := AtlasTextures(textures); err == nil {
		t.Fatal("accepted non-RGBA8 atlas")
	}
}

func TestTexturePixelsPillowGolden(t *testing.T) {
	// Independent Pillow 12.3.0 output from testdata/pillow_oracle.py. Normal Go
	// tests consume these hashes without launching or requiring Python.
	cases := []struct {
		w, h, n int
		hash    string
	}{
		{1, 1, 4, "420d3b26d8eddb5165288610d3d92660f8f7a0257667ccc3927293b1d07ef06b"},
		{2, 2, 20, "5890329c25c97d37bdf72afe9bacac8cb922cb4f8c0ed943347868661cd5b171"},
		{4, 8, 172, "0d11ddd6480de3b0b5cbe38ea9877e5f14e4b16e364fed9940af0d74321797ce"},
		{1, 9, 64, "29c83f9035848c0d6dd2baf0032ebc96fe0520526db28bb8e10b18d0de3a54cc"},
		{9, 1, 64, "29c83f9035848c0d6dd2baf0032ebc96fe0520526db28bb8e10b18d0de3a54cc"},
		{5, 3, 72, "ef8c324cd729628cbe4e67930f4416887caefb28ac91db6a0789547f9931016f"},
		{7, 5, 168, "16e12b1234d8a1e1c47b1b2b21b59c508e7c3a2bac04fbf818a6fcbd6d6cbd1b"},
		{257, 129, 176304, "478d1c3c2e9c36a4bf973fd562ed400b92682e329e139ddae811bb0b88effda8"},
		{512, 256, 699052, "921e0fa3bfced4a66bb7aa0c070c302418e691e34d0b1502d0c35d173499c4f1"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%dx%d", tc.w, tc.h), func(t *testing.T) {
			img := image.NewNRGBA(image.Rect(0, 0, tc.w, tc.h))
			for i := range img.Pix {
				img.Pix[i] = byte((i*73 + (i/7)*19 + 11) % 256)
			}
			got, err := TexturePixels(img)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.n || fmt.Sprintf("%x", sha256.Sum256(got)) != tc.hash {
				t.Fatalf("got %d bytes, hash %x; want %d bytes, hash %s", len(got), sha256.Sum256(got), tc.n, tc.hash)
			}
		})
	}
}
