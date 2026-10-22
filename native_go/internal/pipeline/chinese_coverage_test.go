package pipeline

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"hd2font/native/internal/archiveio"
	"hd2font/native/internal/atlas"
	"hd2font/native/internal/inventory"
)

func v13ScopeFixture(t *testing.T) (*inventory.Inventory, Options) {
	t.Helper()
	inv, options := pipelineFixture(t)
	// Use the complete original cmap for SC/TC baked tables. This is the only
	// Chinese UI source used by 1.3; custom-only and full-Han characters remain
	// outside the atlas key set.
	for _, r := range inv.Resources {
		group, known := inventory.AtlasGroup(r.Archive)
		if !known || r.Kind != "font" || (group != "sc" && group != "tc") {
			continue
		}
		keys := map[uint32]bool{0: true}
		for _, face := range inv.Groups[group] {
			for cp := range face.Chars {
				keys[uint32(cp)] = true
			}
		}
		r.Payload = bakedFixture(sortedKeys(keys))
		r.Entry.MainSize = uint32(len(r.Payload))
	}
	for _, item := range []struct {
		archive, name, group string
		cp                   rune
		baked                bool
	}{
		{"f7b9c03fff72ffd3", "FolkPro Synthetic", "jp", 0x3042, true},
		{"9212d7034dc5d55a", "Yoon Synthetic", "ko", 0xac00, true},
		{"a000000000000001", "Noto Sans Arabic Synthetic", "arabic", 0x0645, false},
		{"a000000000000002", "Noto Sans Bengali Synthetic", "bengali", 0x09ac, false},
		{"a000000000000003", "Noto Sans Devanagari Synthetic", "devanagari", 0x0915, false},
		{"a000000000000004", "Noto Sans Thai Synthetic", "thai", 0x0e01, false},
		{"1111111111111111", "Emoji Synthetic", "emoji", 0x1f601, false},
		{"2222222222222222", "Steelworks Synthetic", "special", 0xe001, false},
	} {
		face := faceFixture(t, item.name, 400, '?', 'A', item.cp)
		inv.Faces[face.Digest] = face
		inv.FaceOrder = append(inv.FaceOrder, face.Digest)
		inv.Groups[face.Group] = append(inv.Groups[face.Group], face)
		inv.Resources = append(inv.Resources, resourceFixture(item.archive, "ttf", 4, wrappedFont(face), face))
		if item.baked {
			inv.Resources = append(inv.Resources,
				resourceFixture(item.archive, "font", 5, bakedFixture([]uint32{0, '?', 'A', uint32(item.cp)})),
				resourceFixture(item.archive, "texture", 6, textureFixture()))
		}
	}

	var faces []*inventory.Face
	var parts []archiveio.RuntimePart
	for _, group := range []string{"latin", "sc", "tc", "jp", "ko", "arabic", "bengali", "devanagari", "thai", "emoji", "special"} {
		face := inv.Groups[group][0]
		faces = append(faces, face)
		parts = append(parts, archiveio.RuntimePart{Data: face.Data, Metrics: [3]float32{1, 2, 3}})
	}
	runtime, err := archiveio.MakeRuntime(make([]byte, 40), parts)
	if err != nil {
		t.Fatal(err)
	}
	inv.Resources = append(inv.Resources, resourceFixture("3333333333333333", "runtime_font", 9, runtime, faces...))
	return inv, options
}

func coverageByGroup(report *Report) map[string]inventory.Coverage {
	rows := make(map[string]inventory.Coverage, len(report.Coverage))
	for _, row := range report.Coverage {
		rows[row.Group] = row
	}
	return rows
}

func TestV13LanguagePlanDoesNotExpandSupplementalGroups(t *testing.T) {
	inv, options := v13ScopeFixture(t)
	raw, err := inventory.InspectFace(mustReadFile(t, options.FontPath))
	if err != nil {
		t.Fatal(err)
	}
	selected, coverage, err := inventory.LanguagePlan(inv, raw, options.FontPath, 0, []string{"sc"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected["latin"].Path == "" || selected["sc"].Path == "" {
		t.Fatalf("SC scope = %v, want latin+sc", selected)
	}
	for _, group := range []string{"tc", "jp", "ko", "arabic", "bengali", "devanagari", "thai", "emoji", "special"} {
		row := coverageIndex(coverage, group)
		if row.Selected || row.Reason != "outside_language_scope" {
			t.Errorf("unselected %s expanded scope: %+v", group, row)
		}
	}
}

func coverageIndex(rows []inventory.Coverage, group string) inventory.Coverage {
	for _, row := range rows {
		if row.Group == group {
			return row
		}
	}
	return inventory.Coverage{Group: group}
}

func TestV13BuildUsesExactScopeAndFallbackSources(t *testing.T) {
	inv, options := v13ScopeFixture(t)
	options.Languages = []string{"sc"}
	generator := &fakeAtlas{}
	report, err := build(context.Background(), inv, &fakeOps{}, options, nil, generator.generate)
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != "2.0.0" || report.EngineProfile != "python-1.3.7-parity-v1" {
		t.Fatalf("wrong product profile: %s / %s", report.Version, report.EngineProfile)
	}
	coverage := coverageByGroup(report)
	for _, group := range []string{"latin", "sc"} {
		if !coverage[group].Selected {
			t.Errorf("required group %s was not selected", group)
		}
	}
	for _, group := range []string{"tc", "jp", "ko", "arabic", "bengali", "devanagari", "thai", "emoji", "special"} {
		if coverage[group].Selected {
			t.Errorf("outside-scope group %s was selected", group)
		}
	}

	atlasGroups := make(map[string]bool)
	for _, row := range report.Atlases {
		atlasGroups[row.Group] = true
	}
	if !slices.Equal(func() []string {
		var groups []string
		for group := range atlasGroups {
			groups = append(groups, group)
		}
		slices.Sort(groups)
		return groups
	}(), []string{"latin", "sc"}) {
		t.Fatalf("atlas groups = %v, want latin+sc", atlasGroups)
	}

	files := zipContents(t, options.OutputZIP)
	for _, archive := range []string{"c0b7644cc5c4aaa8", "f7b9c03fff72ffd3", "9212d7034dc5d55a", "a000000000000001"} {
		if _, ok := files["Fonts/"+archive+".patch_0"]; ok {
			t.Errorf("outside-scope archive %s was emitted", archive)
		}
	}
	_, latinPayload, _, _ := savedResource(t, files, "0de4b81e19a79aa8", 4)
	_, scPayload, _, _ := savedResource(t, files, "09caa46bc556e38f", 4)
	latin, err := inventory.InspectFace(latinPayload[16:])
	if err != nil {
		t.Fatal(err)
	}
	sc, err := inventory.InspectFace(scPayload[16:])
	if err != nil {
		t.Fatal(err)
	}
	for _, cp := range []rune{'?', 'A', 0x4e2d, 0x4e8c, 0x1f600} {
		if _, ok := latin.Chars[cp]; !ok {
			t.Errorf("Latin output lost complete custom source U+%04X", cp)
		}
		if _, ok := sc.Chars[cp]; !ok {
			t.Errorf("SC output lost complete custom source U+%04X", cp)
		}
	}
	for _, cp := range []rune{0x4e09, 0x570b, 0x5708} {
		if _, ok := sc.Chars[cp]; !ok {
			t.Errorf("SC output lost its SC/TC fallback U+%04X", cp)
		}
		if _, ok := latin.Chars[cp]; ok {
			t.Errorf("Latin output received unrelated SC/TC fallback U+%04X", cp)
		}
	}
}

func TestV13FallbackSetsGroupOnlyByStyledDigest(t *testing.T) {
	inv, options := v13ScopeFixture(t)
	options.Languages = []string{"latin", "sc", "tc", "jp", "ko"}
	options.MatchWeights = false
	generator := &fakeAtlas{}
	if _, err := build(context.Background(), inv, &fakeOps{}, options, nil, generator.generate); err != nil {
		t.Fatal(err)
	}
	files := zipContents(t, options.OutputZIP)
	_, payload, _, _ := savedResource(t, files, "0de4b81e19a79aa8", 4)
	face, err := inventory.InspectFace(payload[16:])
	if err != nil {
		t.Fatal(err)
	}
	// 1.3 keys fallback_sets by styled face digest. These source weights all
	// resolve to the same styled digest, so even Latin receives the selected
	// SC/TC/JP/KO original repertoires.
	for _, cp := range []rune{'?', 'A', 0x4e2d, 0x4e8c, 0x4e09, 0x570b, 0x5708, 0x3042, 0xac00, 0x1f600} {
		if _, ok := face.Chars[cp]; !ok {
			t.Errorf("shared styled fallback set lost U+%04X", cp)
		}
	}
	for _, cp := range []rune{0x0645, 0x09ac, 0x0915, 0x0e01, 0x1f601} {
		if _, ok := face.Chars[cp]; ok {
			t.Errorf("out-of-scope auxiliary glyph U+%04X entered shared font", cp)
		}
	}
}

func TestV13AtlasKeysAndSourcesFollowReference(t *testing.T) {
	inv, options := v13ScopeFixture(t)
	options.Languages = []string{"latin", "sc", "tc", "jp", "ko"}
	generator := &fakeAtlas{}
	report, err := build(context.Background(), inv, &fakeOps{}, options, nil, generator.generate)
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != "2.0.0" || report.EngineProfile != "python-1.3.7-parity-v1" {
		t.Fatalf("wrong 1.3 product profile: %s / %s", report.Version, report.EngineProfile)
	}
	chinese, err := chineseUIKit(inv)
	if err != nil {
		t.Fatal(err)
	}
	files := zipContents(t, options.OutputZIP)
	expectations := map[string]struct {
		group string
		extra bool
	}{
		"0de4b81e19a79aa8": {"latin", true},
		"41c8eff8188e4862": {"latin", true},
		"09caa46bc556e38f": {"sc", true},
		"c0b7644cc5c4aaa8": {"tc", true},
		"f7b9c03fff72ffd3": {"jp", false},
		"9212d7034dc5d55a": {"ko", false},
	}
	for archive, expectation := range expectations {
		var baked []*inventory.Resource
		for _, r := range inv.Resources {
			if r.Archive == archive && r.Kind == "font" {
				baked = append(baked, r)
			}
		}
		var extra []uint32
		if expectation.extra {
			extra = chinese
		}
		want, err := archiveKeys(baked, extra)
		if err != nil {
			t.Fatal(err)
		}
		_, payload, _, _ := savedResource(t, files, archive, 5)
		got, err := atlas.GlyphKeys(payload)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s keys = %U, want %U", expectation.group, got, want)
		}
		if expectation.group == "jp" && (slices.Contains(got, uint32(0x4e2d)) || slices.Contains(got, uint32(0x570b))) {
			t.Error("JP atlas received the Chinese UI union")
		}
		if expectation.group == "ko" && (slices.Contains(got, uint32(0x4e2d)) || slices.Contains(got, uint32(0x570b))) {
			t.Error("KO atlas received the Chinese UI union")
		}
	}
	for _, cp := range []uint32{0x4e8c, 0x1f600, 0x3400, 0x20000} {
		for archive := range expectations {
			_, payload, _, _ := savedResource(t, files, archive, 5)
			keys, err := atlas.GlyphKeys(payload)
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(keys, cp) {
				t.Errorf("%s fabricated full-font key U+%04X", archive, cp)
			}
		}
	}

	// SC keys must resolve U+4E2D from the styled custom face and U+570B from
	// the original TC companion. No source may claim a key twice.
	scKeys := func() []uint32 {
		_, payload, _, _ := savedResource(t, files, "09caa46bc556e38f", 5)
		keys, _ := atlas.GlyphKeys(payload)
		return keys
	}()
	scRunes := make([]rune, len(scKeys))
	for i, cp := range scKeys {
		scRunes[i] = rune(cp)
	}
	matched := map[string]bool{}
	for callIndex, sources := range generator.calls {
		if !slices.Equal(generator.keys[callIndex], scRunes) {
			continue
		}
		seen := make(map[rune]bool)
		if len(sources) == 0 || !sources[0].Custom {
			t.Fatal("SC atlas did not put the styled custom face first")
		}
		for _, source := range sources {
			for _, cp := range source.Charset {
				if seen[cp] {
					t.Errorf("duplicate atlas source owner for U+%04X", cp)
				}
				seen[cp] = true
				if cp == 0x4e2d && !source.Custom {
					t.Error("SC original shadowed the custom U+4E2D glyph")
				}
				if cp == 0x570b && source.Custom {
					t.Error("TC original U+570B was reported as a custom glyph")
				}
			}
		}
		if !seen[0x570b] || !seen[0x5708] {
			t.Fatal("SC atlas did not request the TC original for U+570B")
		}
		if len(sources) < 3 {
			t.Fatalf("SC/TC atlas sources = %v, want custom plus both Chinese fallbacks", sources)
		}
		if strings.Contains(sources[1].Name, "Ying Hei") {
			if !slices.Contains(sources[1].Charset, rune(0x4e09)) || !slices.Contains(sources[2].Charset, rune(0x570b)) || !slices.Contains(sources[2].Charset, rune(0x5708)) {
				t.Fatalf("SC weighted fallback order = %v, want custom, SC, TC", sources)
			}
			matched["sc"] = true
		} else if strings.Contains(sources[1].Name, "MJNgai") {
			if !slices.Contains(sources[1].Charset, rune(0x570b)) || !slices.Contains(sources[1].Charset, rune(0x5708)) || !slices.Contains(sources[2].Charset, rune(0x4e09)) {
				t.Fatalf("TC weighted fallback order = %v, want custom, TC, SC", sources)
			}
			matched["tc"] = true
		} else {
			t.Fatalf("unexpected Chinese atlas source order: %v", sources)
		}
	}
	if !matched["sc"] || !matched["tc"] {
		t.Fatalf("exact reference Chinese atlas calls missing: %v", matched)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
