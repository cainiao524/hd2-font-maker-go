package fontcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"hd2font/native/internal/inventory"
	"hd2font/native/internal/sfntio"
)

func TestLocalPreparedFont(t *testing.T) {
	source, dir := os.Getenv("HD2_LOCAL_FONT"), os.Getenv("HD2_LOCAL_DEBUG_DIR")
	if source == "" || dir == "" {
		t.Skip("private preparation diagnostic disabled")
	}
	p := processor(t)
	data := readTest(t, source)
	result, err := p.Prepare(context.Background(), data, 0, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "prepared.ttf"), result, 0600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("HD2_LOCAL_STYLE_PROOF") == "1" {
		f, err := sfntio.Parse(data, 0)
		if err != nil {
			t.Fatal(err)
		}
		m, err := fontMetrics(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range []int{500, 700} {
			out, err := p.Style(context.Background(), result, w, m.upm < 1000, true)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, fmt.Sprintf("style-%d.ttf", w)), out, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestLocalRemapCase(t *testing.T) {
	dir := os.Getenv("HD2_LOCAL_DEBUG_DIR")
	if dir == "" {
		t.Skip("private diagnostic disabled")
	}
	data := readTest(t, filepath.Join(dir, "remap-input.ttf"))
	offset, err := strconv.Atoi(strings.TrimSpace(string(readTest(t, filepath.Join(dir, "remap-offset.txt")))))
	if err != nil {
		t.Fatal(err)
	}
	p := processor(t)
	f, _ := sfntio.Parse(data, 0)
	m, _ := fontMetrics(f)
	mapping := map[uint32]uint32{0: 0}
	for i := 1; i < m.count; i++ {
		mapping[uint32(i)] = uint32(offset + i - 1)
	}
	for _, tag := range []string{"none", "GPOS", "GSUB", "GDEF", "vhea/vmtx", "post", "kern", "BASE", "GPOS/GSUB/GDEF/BASE/kern/vhea/vmtx"} {
		ff, _ := sfntio.Parse(data, 0)
		for _, name := range strings.Split(tag, "/") {
			delete(ff.Tables, name)
		}
		raw, _ := ff.Bytes()
		_, _, err := p.HB.Remap(raw, mapping)
		t.Log(tag, err)
	}
	for _, offset := range []int{1, 100, 30000, 40000, 50000, 55377, 60000} {
		mapping := map[uint32]uint32{0: 0}
		for i := 1; i < m.count; i++ {
			mapping[uint32(i)] = uint32(offset + i - 1)
		}
		_, _, err := p.HB.Remap(data, mapping)
		t.Log("offset", offset, err)
	}
}

// Opt-in private snapshot diagnostics. No local assets are shipped as fixtures.
func TestLocalRemap(t *testing.T) {
	source := os.Getenv("HD2_LOCAL_FONT")
	snapshot := os.Getenv("HD2_LOCAL_SNAPSHOT")
	if source == "" || snapshot == "" {
		t.Skip("private snapshot diagnostics disabled")
	}
	p := processor(t)
	ctx := context.Background()
	data := readTest(t, source)
	prepared, err := p.Prepare(ctx, data, 0, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	custom, err := inventory.InspectFace(prepared)
	if err != nil {
		t.Fatal(err)
	}
	primary, err := p.subset(ctx, prepared, codepoints(custom.Cmap))
	if err != nil {
		t.Fatal(err)
	}
	f, err := sfntio.Parse(primary, 0)
	if err != nil {
		t.Fatal(err)
	}
	base, err := fontMetrics(f)
	if err != nil {
		t.Fatal(err)
	}
	total := base.count
	inv, err := inventory.Load(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var fallbacks []*inventory.Face
	seen := map[string]bool{}
	for _, original := range inv.OrderedFaces() {
		if original.Weight != 700 || !(original.Group == "sc" || original.Group == "tc" || original.Group == "latin") {
			continue
		}
		sources := append([]*inventory.Face{}, inv.Groups[original.Group]...)
		if original.Group == "sc" || original.Group == "tc" {
			sources = append(sources, inv.Groups["sc"]...)
			sources = append(sources, inv.Groups["tc"]...)
		}
		for _, face := range sources {
			if !seen[face.Digest] {
				fallbacks = append(fallbacks, face)
				seen[face.Digest] = true
			}
		}
	}
	distance := func(w uint16) int {
		if w > 700 {
			return int(w) - 700
		}
		return 700 - int(w)
	}
	sort.SliceStable(fallbacks, func(i, j int) bool { return distance(fallbacks[i].Weight) < distance(fallbacks[j].Weight) })
	covered := map[rune]bool{}
	for cp := range custom.Cmap {
		covered[cp] = true
	}
	for _, face := range fallbacks {
		var cps []uint32
		for cp := range face.Cmap {
			if !covered[cp] {
				cps = append(cps, uint32(cp))
			}
		}
		if len(cps) == 0 {
			continue
		}
		sort.Slice(cps, func(i, j int) bool { return cps[i] < cps[j] })
		raw, err := p.subset(ctx, face.Data, cps)
		if err != nil {
			t.Fatal(face.Name, err)
		}
		ff, err := sfntio.Parse(raw, 0)
		if err != nil {
			t.Fatal(err)
		}
		m, err := fontMetrics(ff)
		if err != nil {
			t.Fatal(err)
		}
		if !m.ttf {
			raw, err = p.transform(ctx, raw, transformOptions{ttf: true, upm: m.upm})
			if err != nil {
				t.Fatal(err)
			}
			ff, _ = sfntio.Parse(raw, 0)
			m, _ = fontMetrics(ff)
		}
		if err = rawScale(ff, m, base.upm); err != nil {
			t.Fatal(err)
		}
		if err = stripForeignHints(ff); err != nil {
			t.Fatal(err)
		}
		raw, err = ff.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		mapping := map[uint32]uint32{0: 0}
		for id := 1; id < m.count; id++ {
			mapping[uint32(id)] = uint32(total + id - 1)
		}
		result, _, err := p.HB.Remap(raw, mapping)
		t.Logf("%s: %d glyphs, target offset %d", face.Name, m.count, total)
		if err != nil {
			if dir := os.Getenv("HD2_LOCAL_DEBUG_DIR"); dir != "" {
				if e := os.MkdirAll(dir, 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(filepath.Join(dir, "remap-input.ttf"), raw, 0600); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(filepath.Join(dir, "remap-offset.txt"), []byte(fmt.Sprint(total)), 0600); e != nil {
					t.Fatal(e)
				}
			}
			t.Fatal(err)
		}
		ff, _ = sfntio.Parse(result, 0)
		m, _ = fontMetrics(ff)
		total = m.count
		for _, cp := range cps {
			covered[rune(cp)] = true
		}
	}
}
