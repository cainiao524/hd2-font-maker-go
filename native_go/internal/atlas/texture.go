package atlas

import (
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"math/bits"
)

const TextureHeaderSize = 340

type Texture struct {
	ID      uint64
	Payload []byte
}

// AtlasTextures selects nontrivial RGBA8 atlas textures from texture resources.
// Pass only texture resources; other resource kinds are owned by the caller.
func AtlasTextures(textures []Texture) ([]Texture, error) {
	var out []Texture
	for _, texture := range textures {
		p := texture.Payload
		if len(p) < TextureHeaderSize || string(p[192:196]) != "DDS " {
			return nil, fmt.Errorf("texture %016x has an unsupported DDS header", texture.ID)
		}
		h, w := binary.LittleEndian.Uint32(p[204:]), binary.LittleEndian.Uint32(p[208:])
		if w > 1 && h > 1 {
			if binary.LittleEndian.Uint32(p[320:]) != 28 {
				return nil, fmt.Errorf("texture %016x is not RGBA8", texture.ID)
			}
			out = append(out, texture)
		}
	}
	return out, nil
}

// PatchTextureHeader retains the template's DDS/DX10 fields while moving all mip
// levels to the GPU companion. It does not retain any streaming metadata.
func PatchTextureHeader(template []byte, width, height int) ([]byte, error) {
	if len(template) < TextureHeaderSize || string(template[192:196]) != "DDS " || binary.LittleEndian.Uint32(template[320:]) != 28 {
		return nil, fmt.Errorf("unsupported texture format: RGBA8 DDS required")
	}
	if _, err := mipByteCount(width, height); err != nil {
		return nil, err
	}
	header := make([]byte, TextureHeaderSize)
	copy(header[192:], template[192:TextureHeaderSize])
	binary.LittleEndian.PutUint32(header[8:], math.MaxUint32)
	binary.LittleEndian.PutUint32(header[204:], uint32(height))
	binary.LittleEndian.PutUint32(header[208:], uint32(width))
	binary.LittleEndian.PutUint32(header[212:], uint32(width*4))
	binary.LittleEndian.PutUint32(header[220:], uint32(bits.Len(uint(max(width, height)))))
	return header, nil
}

// TextureParts returns main/header, empty stream, and complete GPU mip bytes.
// A nil pixels argument generates the mip chain; a non-nil one reuses a chain
// previously returned by TexturePixels, with its length checked.
func TextureParts(template []byte, img *image.NRGBA, pixels []byte) (header, stream, gpu []byte, err error) {
	if err = validateImage(img); err != nil {
		return
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	header, err = PatchTextureHeader(template, w, h)
	if err != nil {
		return
	}
	if pixels == nil {
		pixels, err = TexturePixels(img)
	} else {
		n, _ := mipByteCount(w, h)
		if len(pixels) != n {
			err = fmt.Errorf("mip byte count is %d, expected %d", len(pixels), n)
		}
	}
	if err != nil {
		return nil, nil, nil, err
	}
	return header, []byte{}, pixels, nil
}

// TexturePixels returns tightly packed, unpremultiplied RGBA8 mip levels from
// the base image through 1x1. Each level uses horizontal then vertical BOX
// filtering with byte rounding after each pass, as in Pillow's split-channel
// resize. Ordinary alpha-aware RGBA downsampling corrupts distance channels.
func TexturePixels(img *image.NRGBA) ([]byte, error) {
	if err := validateImage(img); err != nil {
		return nil, err
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	n, err := mipByteCount(w, h)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, n)
	for y := 0; y < h; y++ {
		out = append(out, img.Pix[y*img.Stride:y*img.Stride+4*w]...)
	}
	current := out[:w*h*4]
	for w != 1 || h != 1 {
		nw, nh := max(1, w/2), max(1, h/2)
		next := resizeBox(current, w, h, nw, nh)
		start := len(out)
		out = append(out, next...)
		current = out[start:]
		w, h = nw, nh
	}
	return out, nil
}

func mipByteCount(width, height int) (int, error) {
	if width < 1 || height < 1 || uint64(width) > math.MaxUint32/4 || uint64(height) > math.MaxUint32 {
		return 0, fmt.Errorf("invalid texture dimensions %dx%d", width, height)
	}
	var total uint64
	for {
		n := uint64(width) * uint64(height) * 4
		if n > uint64(math.MaxInt)-total {
			return 0, fmt.Errorf("texture dimensions overflow mip storage")
		}
		total += n
		if width == 1 && height == 1 {
			return int(total), nil
		}
		width, height = max(1, width/2), max(1, height/2)
	}
}

func validateImage(img *image.NRGBA) error {
	if img == nil {
		return fmt.Errorf("nil atlas image")
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if _, err := mipByteCount(w, h); err != nil {
		return err
	}
	if img.Stride < 4*w || uint64(h-1)*uint64(img.Stride)+uint64(4*w) > uint64(len(img.Pix)) {
		return fmt.Errorf("invalid NRGBA image stride or pixel buffer")
	}
	return nil
}

type boxSpan struct{ first, end, weight int }

func boxSpans(input, output int) []boxSpan {
	spans := make([]boxSpan, output)
	scale := float64(input) / float64(output)
	for i := range spans {
		center := (float64(i) + 0.5) * scale
		first, end := max(0, int(center-scale/2+0.5)), min(input, int(center+scale/2+0.5))
		// BOX has an exclusive left edge and inclusive right edge. The exact
		// boundary matters when an odd dimension is halved.
		for first < end && (float64(first)-center+0.5)*(1/scale) <= -0.5 {
			first++
		}
		for end > first && (float64(end-1)-center+0.5)*(1/scale) > 0.5 {
			end--
		}
		spans[i] = boxSpan{first, end, int(float64(1<<22)/float64(end-first) + 0.5)}
	}
	return spans
}

func resizeBox(src []byte, w, h, nw, nh int) []byte {
	horizontal := src
	if nw != w {
		horizontal = make([]byte, nw*h*4)
		spans := boxSpans(w, nw)
		for y := 0; y < h; y++ {
			for x, span := range spans {
				for c := 0; c < 4; c++ {
					sum := 1 << 21
					for sx := span.first; sx < span.end; sx++ {
						sum += int(src[(y*w+sx)*4+c]) * span.weight
					}
					horizontal[(y*nw+x)*4+c] = byte(min(255, sum>>22))
				}
			}
		}
	}
	if nh == h {
		return horizontal
	}
	out := make([]byte, nw*nh*4)
	for y, span := range boxSpans(h, nh) {
		for x := 0; x < nw*4; x++ {
			sum := 1 << 21
			for sy := span.first; sy < span.end; sy++ {
				sum += int(horizontal[sy*nw*4+x]) * span.weight
			}
			out[y*nw*4+x] = byte(min(255, sum>>22))
		}
	}
	return out
}
