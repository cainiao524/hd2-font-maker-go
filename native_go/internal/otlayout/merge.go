package otlayout

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Part tables must already use disjoint final glyph IDs. HarfBuzz supplies the
// layout-aware glyph remapping; this layer composes lookup/feature namespaces.
type Part struct{ GSUB, GPOS, GDEF []byte }
type feature struct {
	tag     string
	lookups []int
}
type language struct {
	features map[string][]int
	required map[string][]int
}
type script struct{ languages map[string]*language }
type lookup struct {
	flags, filter int
	subs          []subtable
}
type subtable struct{ kind, source, offset int }
type extensionPatch struct{ at, base, source, target int }

func newLanguage() *language {
	return &language{features: map[string][]int{}, required: map[string][]int{}}
}
func readFeatures(b *buffer, lookupBase int) []feature {
	p := b.u16(6)
	n := b.u16(p)
	if !b.check(p+2, n*6) {
		return nil
	}
	result := make([]feature, n)
	for i := range result {
		q := p + 2 + i*6
		f := p + b.u16(q+4)
		count := b.u16(f + 2)
		if !b.check(f+4, count*2) {
			return nil
		}
		result[i].tag = b.tag(q)
		for j := 0; j < count; j++ {
			result[i].lookups = append(result[i].lookups, b.u16(f+4+2*j)+lookupBase)
		}
	}
	return result
}
func readLanguage(b *buffer, p int, features []feature, dest *language) {
	add := func(index int, required bool) {
		if index < 0 || index >= len(features) {
			b.err = fmt.Errorf("feature index out of range")
			return
		}
		f := features[index]
		target := dest.features
		if required {
			target = dest.required
		}
		target[f.tag] = append(target[f.tag], f.lookups...)
	}
	if req := b.u16(p + 2); req != 65535 {
		add(req, true)
	}
	n := b.u16(p + 4)
	if !b.check(p+6, n*2) {
		return
	}
	for i := 0; i < n; i++ {
		add(b.u16(p+6+2*i), false)
	}
}
func readScripts(b *buffer, features []feature, scripts map[string]*script) {
	p := b.u16(4)
	n := b.u16(p)
	if !b.check(p+2, n*6) {
		return
	}
	for i := 0; i < n; i++ {
		q := p + 2 + i*6
		tag := b.tag(q)
		s := p + b.u16(q+4)
		target := scripts[tag]
		if target == nil {
			target = &script{languages: map[string]*language{}}
			scripts[tag] = target
		}
		read := func(tag string, pos int) {
			lang := target.languages[tag]
			if lang == nil {
				lang = newLanguage()
				target.languages[tag] = lang
			}
			readLanguage(b, pos, features, lang)
		}
		if offset := b.u16(s); offset != 0 {
			read("", s+offset)
		}
		count := b.u16(s + 2)
		if !b.check(s+4, count*6) {
			return
		}
		for j := 0; j < count; j++ {
			q := s + 4 + 6*j
			read(b.tag(q), s+b.u16(q+4))
		}
	}
}
func lookupRecords(b *buffer, p, count, base, limit int, seen map[int]bool) {
	if !b.check(p, count*4) {
		return
	}
	for i := 0; i < count; i++ {
		at := p + i*4 + 2
		if seen[at] {
			continue
		}
		old := b.u16(at)
		if old >= limit {
			b.err = fmt.Errorf("context lookup index exceeds source list")
			return
		}
		b.put16(at, old+base)
		seen[at] = true
	}
}
func fixContext(b *buffer, p int, chain bool, base, limit int, seen map[int]bool) {
	format := b.u16(p)
	if format == 3 {
		if !chain {
			n, count := b.u16(p+2), b.u16(p+4)
			lookupRecords(b, p+6+2*n, count, base, limit, seen)
			return
		}
		q := p + 2
		for i := 0; i < 3; i++ {
			n := b.u16(q)
			q += 2 + 2*n
		}
		count := b.u16(q)
		lookupRecords(b, q+2, count, base, limit, seen)
		return
	}
	if format != 1 && format != 2 {
		b.err = fmt.Errorf("context format %d", format)
		return
	}
	countPos := p + 4
	if format == 2 {
		countPos = p + 6
		if chain {
			countPos = p + 10
		}
	}
	n := b.u16(countPos)
	if !b.check(countPos+2, n*2) {
		return
	}
	for i := 0; i < n; i++ {
		off := b.u16(countPos + 2 + 2*i)
		if off == 0 {
			continue
		}
		set := p + off
		count := b.u16(set)
		if !b.check(set+2, count*2) {
			return
		}
		for j := 0; j < count; j++ {
			rule := set + b.u16(set+2+2*j)
			if !chain {
				glyphs, records := b.u16(rule), b.u16(rule+2)
				if glyphs == 0 {
					b.err = fmt.Errorf("empty contextual input")
					return
				}
				lookupRecords(b, rule+4+2*(glyphs-1), records, base, limit, seen)
			} else {
				q := rule
				n := b.u16(q)
				q += 2 + 2*n
				n = b.u16(q)
				if n == 0 {
					b.err = fmt.Errorf("empty chained input")
					return
				}
				q += 2 + 2*(n-1)
				n = b.u16(q)
				q += 2 + 2*n
				count := b.u16(q)
				lookupRecords(b, q+2, count, base, limit, seen)
			}
		}
	}
}
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func featureKey(f feature) string {
	var b strings.Builder
	b.WriteString(f.tag)
	for _, v := range f.lookups {
		b.WriteByte('/')
		b.WriteString(strconv.Itoa(v))
	}
	return b.String()
}

func mergeLayout(tag string, parts []Part, markBases, classBases []int) ([]byte, error) {
	scripts := map[string]*script{}
	var lookups []lookup
	var blobs [][]byte
	for source, part := range parts {
		data := part.GSUB
		if tag == "GPOS" {
			data = part.GPOS
		}
		if len(data) == 0 {
			blobs = append(blobs, nil)
			continue
		}
		b := buffer{data: append([]byte(nil), data...)}
		version := b.u32(0)
		if version != 0x10000 && version != 0x10001 {
			return nil, fmt.Errorf("unsupported %s version", tag)
		}
		if version == 0x10001 && b.u32(10) != 0 {
			return nil, fmt.Errorf("instance feature variations before layout merge")
		}
		base := len(lookups)
		list := b.u16(8)
		count := b.u16(list)
		if !b.check(list+2, count*2) {
			return nil, b.err
		}
		readScripts(&b, readFeatures(&b, base), scripts)
		visited := map[int]bool{}
		// HarfBuzz may share rule sets or lookup records between subtables.
		// Their stored lookup index must be rebased exactly once per source.
		seenRecords := map[int]bool{}
		for i := 0; i < count; i++ {
			p := list + b.u16(list+2+2*i)
			kind, flags, n := b.u16(p), b.u16(p+2), b.u16(p+4)
			if !b.check(p+6, n*2) {
				return nil, b.err
			}
			item := lookup{flags: flags}
			if flags>>8 != 0 {
				class := (flags >> 8) + classBases[source]
				if class > 255 {
					return nil, fmt.Errorf("mark attachment classes exceed 255")
				}
				item.flags = (flags & 255) | (class << 8)
			}
			if flags&16 != 0 {
				item.filter = b.u16(p+6+2*n) + markBases[source]
			}
			for j := 0; j < n; j++ {
				q := p + b.u16(p+6+2*j)
				actual := kind
				extension := 7
				if tag == "GPOS" {
					extension = 9
				}
				if kind == extension {
					if b.u16(q) != 1 {
						return nil, fmt.Errorf("invalid extension")
					}
					actual = b.u16(q + 2)
					q += b.u32(q + 4)
					if actual == extension {
						return nil, fmt.Errorf("nested extension")
					}
				}
				contextKind, chainKind := 5, 6
				if tag == "GPOS" {
					contextKind, chainKind = 7, 8
				}
				if !visited[q] && (actual == contextKind || actual == chainKind) {
					fixContext(&b, q, actual == chainKind, base, count, seenRecords)
					visited[q] = true
				}
				item.subs = append(item.subs, subtable{actual, source, q})
			}
			lookups = append(lookups, item)
		}
		if b.err != nil {
			return nil, b.err
		}
		blobs = append(blobs, b.data)
	}
	if len(blobs) == 0 || len(lookups) == 0 {
		return nil, nil
	}
	if len(lookups) > 65535 {
		return nil, fmt.Errorf("too many merged lookups")
	}
	features := []feature{}
	indices := map[string]int{}
	addFeature := func(f feature) {
		key := featureKey(f)
		if _, ok := indices[key]; !ok {
			indices[key] = len(features)
			features = append(features, f)
		}
	}
	for _, stag := range sortedKeys(scripts) {
		for _, ltag := range sortedKeys(scripts[stag].languages) {
			l := scripts[stag].languages[ltag]
			for _, tag := range sortedKeys(l.features) {
				addFeature(feature{tag, l.features[tag]})
			}
			if len(l.required) > 0 {
				r := feature{tag: "rqd0"}
				tags := sortedKeys(l.required)
				if len(tags) == 1 {
					r.tag = tags[0]
				}
				for _, tag := range tags {
					r.lookups = append(r.lookups, l.required[tag]...)
				}
				addFeature(r)
			}
		}
	}
	sort.SliceStable(features, func(i, j int) bool { return features[i].tag < features[j].tag })
	for i, f := range features {
		indices[featureKey(f)] = i
	}
	featureBytes := make([]byte, 2+6*len(features))
	put16(featureBytes, 0, len(features))
	for i, f := range features {
		if len(featureBytes) > 65535 {
			return nil, fmt.Errorf("merged feature offsets exceed 16 bits")
		}
		copy(featureBytes[2+6*i:], f.tag)
		put16(featureBytes, 6+6*i, len(featureBytes))
		featureBytes = append16(featureBytes, 0)
		featureBytes = append16(featureBytes, len(f.lookups))
		for _, index := range f.lookups {
			featureBytes = append16(featureBytes, index)
		}
	}
	encodeLang := func(l *language) []byte {
		req := 65535
		if len(l.required) > 0 {
			r := feature{tag: "rqd0"}
			tags := sortedKeys(l.required)
			if len(tags) == 1 {
				r.tag = tags[0]
			}
			for _, tag := range tags {
				r.lookups = append(r.lookups, l.required[tag]...)
			}
			req = indices[featureKey(r)]
		}
		data := []byte{0, 0}
		data = append16(data, req)
		data = append16(data, len(l.features))
		for _, tag := range sortedKeys(l.features) {
			data = append16(data, indices[featureKey(feature{tag, l.features[tag]})])
		}
		return data
	}
	scriptTags := sortedKeys(scripts)
	scriptBytes := make([]byte, 2+6*len(scriptTags))
	put16(scriptBytes, 0, len(scriptTags))
	for i, tag := range scriptTags {
		base := len(scriptBytes)
		if base > 65535 {
			return nil, fmt.Errorf("script offset overflow")
		}
		copy(scriptBytes[2+6*i:], tag)
		put16(scriptBytes, 6+6*i, base)
		langs := scripts[tag].languages
		tags := []string{}
		for _, tag := range sortedKeys(langs) {
			if tag != "" {
				tags = append(tags, tag)
			}
		}
		block := make([]byte, 4+6*len(tags))
		put16(block, 2, len(tags))
		if def := langs[""]; def != nil {
			put16(block, 0, len(block))
			block = append(block, encodeLang(def)...)
		}
		for j, tag := range tags {
			if len(block) > 65535 {
				return nil, fmt.Errorf("language offset overflow")
			}
			copy(block[4+j*6:], tag)
			put16(block, 8+j*6, len(block))
			block = append(block, encodeLang(langs[tag])...)
		}
		scriptBytes = append(scriptBytes, block...)
	}
	result := make([]byte, 10)
	put32(result, 0, 0x10000)
	put16(result, 4, len(result))
	result = append(result, scriptBytes...)
	if len(result) > 65535 {
		return nil, fmt.Errorf("layout feature offset overflow")
	}
	put16(result, 6, len(result))
	result = append(result, featureBytes...)
	if len(result) > 65535 {
		return nil, fmt.Errorf("layout lookup offset overflow")
	}
	listBase := len(result)
	put16(result, 8, listBase)
	result = append(result, make([]byte, 2+2*len(lookups))...)
	put16(result, listBase, len(lookups))
	var patches []extensionPatch
	for i, l := range lookups {
		p := len(result)
		if p-listBase > 65535 {
			return nil, fmt.Errorf("lookup list offset overflow")
		}
		put16(result, listBase+2+2*i, p-listBase)
		extension := 7
		if tag == "GPOS" {
			extension = 9
		}
		result = append16(result, extension)
		result = append16(result, l.flags)
		result = append16(result, len(l.subs))
		result = append(result, make([]byte, 2*len(l.subs))...)
		if l.flags&16 != 0 {
			result = append16(result, l.filter)
		}
		for j, sub := range l.subs {
			q := len(result)
			if q-p > 65535 {
				return nil, fmt.Errorf("extension offset overflow")
			}
			put16(result, p+6+2*j, q-p)
			result = append16(result, 1)
			result = append16(result, sub.kind)
			result = append(result, 0, 0, 0, 0)
			patches = append(patches, extensionPatch{q + 4, q, sub.source, sub.offset})
		}
	}
	bases := make([]int, len(blobs))
	for i, b := range blobs {
		bases[i] = len(result)
		result = append(result, b...)
	}
	for _, p := range patches {
		put32(result, p.at, bases[p.source]+p.target-p.base)
	}
	return result, nil
}

func Merge(parts []Part) (map[string][]byte, error) {
	gdef, markBases, classBases, err := mergeGDEF(parts)
	if err != nil {
		return nil, err
	}
	result := map[string][]byte{}
	if len(gdef) > 0 {
		result["GDEF"] = gdef
	}
	for _, tag := range []string{"GSUB", "GPOS"} {
		data, err := mergeLayout(tag, parts, markBases, classBases)
		if err != nil {
			return nil, fmt.Errorf("%s merge: %w", tag, err)
		}
		if len(data) > 0 {
			result[tag] = data
		}
	}
	return result, nil
}
