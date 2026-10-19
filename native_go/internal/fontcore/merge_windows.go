//go:build windows && amd64

package fontcore

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"

	"hd2font/native/internal/inventory"
	"hd2font/native/internal/otlayout"
	"hd2font/native/internal/sfntio"
)

func codepoints(mapping map[rune]uint16) []uint32 {
	values := make([]uint32, 0, len(mapping))
	for cp := range mapping {
		values = append(values, uint32(cp))
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values
}
func (p *Processor) subset(ctx context.Context, data []byte, chars []uint32) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, raw, err := canonicalFont(data, 0)
	if err != nil {
		return nil, err
	}
	return p.HB.Subset(raw, chars)
}
func (p *Processor) Subset(ctx context.Context, data []byte, chars []rune) ([]byte, error) {
	values := make([]uint32, len(chars))
	for i, cp := range chars {
		values[i] = uint32(cp)
	}
	return p.subset(ctx, data, values)
}

func (p *Processor) Merge(ctx context.Context, custom []byte, fallbacks [][]byte) ([]byte, error) {
	face, err := inventory.InspectFace(custom)
	if err != nil {
		return nil, err
	}
	required := map[rune]bool{}
	for cp := range face.Cmap {
		required[cp] = true
	}
	sources := make([]*inventory.Face, len(fallbacks))
	for i, data := range fallbacks {
		sources[i], err = inventory.InspectFace(data)
		if err != nil {
			return nil, err
		}
		for cp := range sources[i].Cmap {
			required[cp] = true
		}
	}
	primary, err := p.subset(ctx, custom, codepoints(face.Cmap))
	if err != nil {
		return nil, err
	}
	baseFace, err := inventory.InspectFace(primary)
	if err != nil {
		return nil, err
	}
	missing := map[rune]bool{}
	for cp := range required {
		if _, ok := baseFace.Cmap[cp]; !ok {
			missing[cp] = true
		}
	}
	if len(missing) == 0 {
		return primary, nil
	}
	if baseFace.Format != "ttf" {
		primary, err = p.transform(ctx, primary, transformOptions{ttf: true, upm: baseFace.UPM})
		if err != nil {
			return nil, err
		}
	}
	base, err := sfntio.Parse(primary, 0)
	if err != nil {
		return nil, err
	}
	bm, err := fontMetrics(base)
	if err != nil {
		return nil, err
	}
	parts := []*sfntio.Font{base}
	metas := []*metrics{bm}
	starts := []int{0}
	total := bm.count
	cmap := map[rune]uint16{}
	for cp, gid := range baseFace.Cmap {
		cmap[cp] = gid
	}
	for _, source := range sources {
		chars := []uint32{}
		for cp := range source.Cmap {
			if missing[cp] {
				chars = append(chars, uint32(cp))
			}
		}
		if len(chars) == 0 {
			continue
		}
		sort.Slice(chars, func(i, j int) bool { return chars[i] < chars[j] })
		data, err := p.subset(ctx, source.Data, chars)
		if err != nil {
			return nil, err
		}
		f, err := sfntio.Parse(data, 0)
		if err != nil {
			return nil, err
		}
		m, err := fontMetrics(f)
		if err != nil {
			return nil, err
		}
		if !m.ttf {
			data, err = p.transform(ctx, data, transformOptions{ttf: true, upm: m.upm})
			if err != nil {
				return nil, err
			}
			f, err = sfntio.Parse(data, 0)
			if err != nil {
				return nil, err
			}
			m, err = fontMetrics(f)
			if err != nil {
				return nil, err
			}
		}
		if m.upm != bm.upm {
			if err = rawScale(f, m, bm.upm); err != nil {
				return nil, err
			}
		}
		if err = stripForeignHints(f); err != nil {
			return nil, err
		}
		data, err = f.Bytes()
		if err != nil {
			return nil, err
		}
		if total+m.count-1 > 65535 {
			return nil, fmt.Errorf("full repertoire exceeds OpenType's 65535 glyph limit")
		}
		mapping := map[uint32]uint32{0: 0}
		for i := 1; i < m.count; i++ {
			mapping[uint32(i)] = uint32(total + i - 1)
		}
		remapped, actual, err := p.HB.Remap(data, mapping)
		if err != nil {
			return nil, fmt.Errorf("remap fallback %s (%d glyphs, offset %d): %w", source.Name, m.count, total, err)
		}
		for old, next := range mapping {
			if actual[old] != next {
				return nil, fmt.Errorf("native glyph remapping changed requested IDs")
			}
		}
		f, err = sfntio.Parse(remapped, 0)
		if err != nil {
			return nil, err
		}
		m, err = fontMetrics(f)
		if err != nil {
			return nil, err
		}
		partFace, err := inventory.InspectFace(remapped)
		if err != nil {
			return nil, err
		}
		for _, cp := range chars {
			gid, ok := partFace.Cmap[rune(cp)]
			if !ok {
				return nil, fmt.Errorf("fallback subset lost U+%04X", cp)
			}
			cmap[rune(cp)] = gid
			delete(missing, rune(cp))
		}
		parts = append(parts, f)
		metas = append(metas, m)
		starts = append(starts, total)
		total = m.count
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("fallback merge left %d unsupported characters", len(missing))
	}
	advances, bearings := make([]uint16, total), make([]int16, total)
	var glyf []byte
	loca := make([]byte, 4*(total+1))
	bounds := make([][4]int16, total)
	nonempty := make([]bool, total)
	layout := make([]otlayout.Part, len(parts))
	rawGlyphs := make([][]byte, total)
	for index, part := range parts {
		m := metas[index]
		layout[index] = otlayout.Part{GSUB: part.Tables["GSUB"], GPOS: part.Tables["GPOS"], GDEF: part.Tables["GDEF"]}
		for id := starts[index]; id < m.count; id++ {
			raw := part.Tables["glyf"][m.offsets[id]:m.offsets[id+1]]
			rawGlyphs[id] = raw
			binary.BigEndian.PutUint32(loca[4*id:], uint32(len(glyf)))
			glyf = append(glyf, raw...)
			if len(glyf)%2 != 0 {
				glyf = append(glyf, 0)
			}
			advances[id], bearings[id] = m.advances[id], m.bearings[id]
			if len(raw) >= 10 {
				contours := int16(binary.BigEndian.Uint16(raw))
				nonempty[id] = contours != 0
				for j := 0; j < 4; j++ {
					bounds[id][j] = int16(binary.BigEndian.Uint16(raw[2+2*j:]))
				}
			}
		}
		if index > 0 {
			for _, tag := range []string{"BASE", "MATH"} {
				if base.Tables[tag] == nil && part.Tables[tag] != nil {
					base.Tables[tag] = append([]byte(nil), part.Tables[tag]...)
				}
			}
			for _, off := range []int{74, 76} {
				if len(base.Tables["OS/2"]) >= off+2 && len(part.Tables["OS/2"]) >= off+2 {
					a, b := binary.BigEndian.Uint16(base.Tables["OS/2"][off:]), binary.BigEndian.Uint16(part.Tables["OS/2"][off:])
					binary.BigEndian.PutUint16(base.Tables["OS/2"][off:], max(a, b))
				}
			}
			for off := 42; off < 58; off += 4 {
				if len(base.Tables["OS/2"]) >= off+4 && len(part.Tables["OS/2"]) >= off+4 {
					a, b := binary.BigEndian.Uint32(base.Tables["OS/2"][off:]), binary.BigEndian.Uint32(part.Tables["OS/2"][off:])
					binary.BigEndian.PutUint32(base.Tables["OS/2"][off:], a|b)
				}
			}
		}
	}
	binary.BigEndian.PutUint32(loca[4*total:], uint32(len(glyf)))
	base.Tables["glyf"], base.Tables["loca"] = glyf, loca
	binary.BigEndian.PutUint16(base.Tables["head"][50:], 1)
	if len(base.Tables["maxp"]) < 32 {
		return nil, fmt.Errorf("missing TrueType maximum profile")
	}
	base.Tables["maxp"], err = ttfMaximumProfile(base.Tables["maxp"], rawGlyphs)
	if err != nil {
		return nil, err
	}
	mergedLayout, err := otlayout.Merge(layout)
	if err != nil {
		return nil, err
	}
	for _, tag := range []string{"GSUB", "GPOS", "GDEF"} {
		delete(base.Tables, tag)
	}
	for tag, data := range mergedLayout {
		base.Tables[tag] = data
	}
	var kern []byte
	subtables := 0
	for _, part := range parts {
		data := part.Tables["kern"]
		if len(data) == 0 {
			continue
		}
		if len(data) < 4 || binary.BigEndian.Uint16(data) != 0 {
			return nil, fmt.Errorf("unsupported merged legacy kerning")
		}
		subtables += int(binary.BigEndian.Uint16(data[2:]))
		kern = append(kern, data[4:]...)
	}
	if subtables > 0 {
		base.Tables["kern"] = append([]byte{0, 0, byte(subtables >> 8), byte(subtables)}, kern...)
	}
	base.Tables["cmap"] = sfntio.UnicodeCmap(cmap)
	post := make([]byte, 32)
	copy(post, base.Tables["post"])
	binary.BigEndian.PutUint32(post, 0x30000)
	base.Tables["post"] = post
	for _, tag := range []string{"DSIG", "FFTM", "meta", "vhea", "vmtx", "VORG", "LTSH", "VDMX", "hdmx"} {
		delete(base.Tables, tag)
	}
	setMetrics(base, advances, bearings, bounds, nonempty)
	result, err := base.Bytes()
	if err != nil {
		return nil, err
	}
	output, err := inventory.InspectFace(result)
	if err != nil {
		return nil, err
	}
	for cp := range required {
		if _, ok := output.Cmap[cp]; !ok {
			return nil, fmt.Errorf("merged font lost U+%04X", cp)
		}
	}
	return result, nil
}

func stripForeignHints(f *sfntio.Font) error {
	m, err := fontMetrics(f)
	if err != nil {
		return err
	}
	var data []byte
	loca := make([]byte, 4*(m.count+1))
	for i := 0; i < m.count; i++ {
		binary.BigEndian.PutUint32(loca[4*i:], uint32(len(data)))
		raw, err := sfntio.StripGlyphHints(f.Tables["glyf"][m.offsets[i]:m.offsets[i+1]])
		if err != nil {
			return err
		}
		data = append(data, raw...)
		if len(data)%2 != 0 {
			data = append(data, 0)
		}
	}
	binary.BigEndian.PutUint32(loca[4*m.count:], uint32(len(data)))
	f.Tables["glyf"], f.Tables["loca"] = data, loca
	binary.BigEndian.PutUint16(f.Tables["head"][50:], 1)
	for _, tag := range []string{"cvt ", "fpgm", "prep", "LTSH", "VDMX", "hdmx"} {
		delete(f.Tables, tag)
	}
	return nil
}
