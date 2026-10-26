package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"unicode/utf8"

	"hd2font/native/internal/atlas"
	"hd2font/native/internal/inventory"
)

type effectiveSource struct {
	face   *inventory.Face
	chars  []rune
	custom bool
}

type atlasJob struct {
	custom    *inventory.Face
	originals []*inventory.Face
	keys      []uint32
	sources   []effectiveSource
}

type atlasOutput struct {
	result *atlas.Result
	pixels []byte
}

type atlasSourceKey struct {
	Digest     string `json:"digest"`
	Characters []rune `json:"characters"`
}

func sortedKeys(set map[uint32]bool) []uint32 {
	out := make([]uint32, 0, len(set))
	for cp := range set {
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// effectiveSources mirrors atlas.py: the styled custom face wins, then each
// fallback supplies only characters not already assigned by an earlier face.
func effectiveSources(custom *inventory.Face, originals []*inventory.Face, keys []uint32) []effectiveSource {
	remaining := map[rune]bool{'?': true}
	for _, cp := range keys {
		if cp != 0 && cp <= utf8.MaxRune && utf8.ValidRune(rune(cp)) {
			remaining[rune(cp)] = true
		}
	}
	seen := make(map[string]bool)
	var result []effectiveSource
	for _, face := range append([]*inventory.Face{custom}, originals...) {
		if seen[face.Digest] {
			continue
		}
		seen[face.Digest] = true
		var chars []rune
		for cp := range remaining {
			if _, ok := face.Chars[cp]; ok {
				chars = append(chars, cp)
				delete(remaining, cp)
			}
		}
		if len(chars) == 0 {
			continue
		}
		sort.Slice(chars, func(i, j int) bool { return chars[i] < chars[j] })
		result = append(result, effectiveSource{face, chars, face.Digest == custom.Digest})
	}
	return result
}

func archiveKeys(baked []*inventory.Resource, extra []uint32) ([]uint32, error) {
	keys := make(map[uint32]bool)
	for _, r := range baked {
		row, err := atlas.GlyphKeys(r.Payload)
		if err != nil {
			return nil, err
		}
		for _, cp := range row {
			keys[cp] = true
		}
	}
	for _, cp := range extra {
		keys[cp] = true
	}
	return sortedKeys(keys), nil
}

func chineseUIKit(inv *inventory.Inventory) ([]uint32, error) {
	keys := make(map[uint32]bool)
	for _, r := range inv.Resources {
		if r.Kind != "font" {
			continue
		}
		group, known := inventory.AtlasGroup(r.Archive)
		if !known || (group != "sc" && group != "tc") {
			continue
		}
		row, err := atlas.GlyphKeys(r.Payload)
		if err != nil {
			return nil, err
		}
		for _, cp := range row {
			keys[cp] = true
		}
	}
	return sortedKeys(keys), nil
}

func atlasExtraKeys(group string, chinese []uint32) []uint32 {
	if group == "sc" || group == "tc" || group == "latin" {
		return append([]uint32(nil), chinese...)
	}
	return nil
}

func atlasOriginals(inv *inventory.Inventory, archive, group string, withChinese bool) []*inventory.Face {
	seen := make(map[string]bool)
	var original []*inventory.Face
	add := func(f *inventory.Face) {
		if f != nil && !seen[f.Digest] {
			seen[f.Digest] = true
			original = append(original, f)
		}
	}
	for _, r := range inv.Resources {
		if r.Archive != archive {
			continue
		}
		for _, f := range r.Faces {
			if f.Group == group {
				add(f)
			}
		}
	}
	for _, f := range inv.Groups[group] {
		add(f)
	}
	if withChinese {
		for _, group := range []string{"sc", "tc"} {
			for _, f := range inv.Groups[group] {
				add(f)
			}
		}
	}
	return original
}

func atlasJobID(custom *inventory.Face, keys []uint32, sources []effectiveSource) string {
	sourceKeys := make([]atlasSourceKey, len(sources))
	for i, source := range sources {
		sourceKeys[i] = atlasSourceKey{source.face.Digest, source.chars}
	}
	return jobKey(custom.Digest, keys, sourceKeys)
}

func (s *buildState) prepareAtlasSources(job atlasJob) ([]atlas.Source, error) {
	prepared := make([]atlas.Source, len(job.sources))
	for i, source := range job.sources {
		if err := s.ctx.Err(); err != nil {
			return nil, err
		}
		data := source.face.Data
		if !source.custom {
			var err error
			data, err = s.ops.Subset(s.ctx, data, source.chars)
			if err != nil {
				return nil, err
			}
			data, err = s.ops.PrepareAtlasFallback(s.ctx, data, s.options.ProcessOutlines)
			if err != nil {
				return nil, err
			}
			face, err := inventory.InspectFace(data)
			if err != nil {
				return nil, err
			}
			required := make(map[rune]struct{}, len(source.chars))
			for _, cp := range source.chars {
				required[cp] = struct{}{}
			}
			if err = hasCoverage(face, required, "atlas fallback preparation"); err != nil {
				return nil, err
			}
		}
		path := filepath.Join(s.work, fmt.Sprintf("atlas-source-%s-%d.font", jobKey(job.custom.Digest, source.face.Digest, source.chars), i))
		if err := os.WriteFile(path, data, 0600); err != nil {
			return nil, err
		}
		prepared[i] = atlas.Source{FontPath: path, Charset: source.chars, Name: source.face.Name, Custom: source.custom}
	}
	return prepared, nil
}

func (s *buildState) atlases(generate atlasGenerator) error {
	chinese, err := chineseUIKit(s.inv)
	if err != nil {
		return err
	}
	s.report.MixedChineseUICharacters = len(chinese)

	bakedByArchive := make(map[string][]*inventory.Resource)
	for _, use := range s.atlasUses {
		bakedByArchive[use.Archive] = append(bakedByArchive[use.Archive], use.Baked)
	}
	keysByArchive := make(map[string][]uint32, len(bakedByArchive))
	for archive, baked := range bakedByArchive {
		group, _ := inventory.AtlasGroup(archive)
		keysByArchive[archive], err = archiveKeys(baked, atlasExtraKeys(group, chinese))
		if err != nil {
			return err
		}
	}

	jobIDs := make(map[string]int)
	var jobs []atlasJob
	useJobs := make([]int, len(s.atlasUses))
	extraKeys := make([][]uint32, len(s.atlasUses))
	for i, use := range s.atlasUses {
		custom := s.styled[use.Template.Digest]
		if custom == nil {
			return fmt.Errorf("missing styled atlas template %q", use.Template.Name)
		}
		withChinese := use.Group == "sc" || use.Group == "tc" || use.Group == "latin"
		original := atlasOriginals(s.inv, use.Archive, use.Group, withChinese)
		sort.SliceStable(original, func(a, b int) bool {
			return distance(int(original[a].Weight), int(use.Template.Weight)) < distance(int(original[b].Weight), int(use.Template.Weight))
		})
		// Match runtime-font fallback order: siblings fill gaps before the game.
		original = append(s.familyFallbacks(custom, int(use.Template.Weight)), original...)
		keys := keysByArchive[use.Archive]
		sources := effectiveSources(custom, original, keys)
		for i := range sources {
			if _, prepared := s.familyPixel[sources[i].face.Digest]; prepared {
				// Family faces already went through custom preparation; preserve
				// their authored spacing and avoid original-font cleanup a second time.
				sources[i].custom = true
			}
		}
		id := atlasJobID(custom, keys, sources)
		j, ok := jobIDs[id]
		if !ok {
			j = len(jobs)
			jobIDs[id] = j
			jobs = append(jobs, atlasJob{custom: custom, originals: original, keys: keys, sources: sources})
		}
		useJobs[i] = j
		extraKeys[i] = atlasExtraKeys(use.Group, chinese)
	}

	s.report.Performance["unique_atlases"] = len(jobs)
	s.report.Performance["atlas_resources"] = len(s.atlasUses)
	if len(jobs) == 0 {
		return nil
	}

	executable := s.options.AtlasExecutable
	if executable == "" && s.options.BinDir != "" {
		for _, candidate := range []string{filepath.Join(s.options.BinDir, "msdf-atlas-gen.exe"), filepath.Join(s.options.BinDir, "vendor", "msdf-atlas-gen", "msdf-atlas-gen.exe"), filepath.Join(s.options.BinDir, "..", "..", "vendor", "msdf-atlas-gen", "msdf-atlas-gen.exe")} {
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				executable = candidate
				break
			}
		}
	}
	fontSources := make([][]atlas.Source, len(jobs))
	for i, job := range jobs {
		fontSources[i], err = s.prepareAtlasSources(job)
		if err != nil {
			return err
		}
	}
	s.report.Performance["unique_atlas_sources"] = len(jobs)

	// Concurrent atlas jobs share all but two logical CPUs; msdf-atlas-gen output
	// does not depend on its thread count.
	threads := max(1, max(1, runtime.NumCPU()-2)/min(s.options.Workers, len(jobs)))
	var generators []func(context.Context) (atlasOutput, error)
	for index, job := range jobs {
		generators = append(generators, func(ctx context.Context) (atlasOutput, error) {
			keys := make([]rune, 0, len(job.keys))
			for _, cp := range job.keys {
				if cp <= utf8.MaxRune && utf8.ValidRune(rune(cp)) {
					keys = append(keys, rune(cp))
				}
			}
			result, err := generate(ctx, fontSources[index], atlas.Options{Executable: executable, WorkDir: s.work, Keys: keys, Threads: threads, RawPixels: true})
			if err != nil {
				return atlasOutput{}, err
			}
			pixels, err := atlas.TexturePixels(result.Image)
			if err != nil {
				return atlasOutput{}, err
			}
			result.PNG = nil
			result.Metadata = nil
			return atlasOutput{result, pixels}, nil
		})
	}
	outputs, err := runJobs(s.ctx, generators, s.options.Workers, s.stage("Generating MTSDF atlases"))
	if err != nil {
		return err
	}
	for i, use := range s.atlasUses {
		out := outputs[useJobs[i]]
		result := out.result
		w, h := result.Image.Bounds().Dx(), result.Image.Bounds().Dy()
		glyphs, err := atlas.ReplaceGlyphTable(use.Baked.Payload, w, h, result.Glyphs, extraKeys[i]...)
		if err != nil {
			return err
		}
		if err = s.put(use.Baked, glyphs, nil, nil); err != nil {
			return err
		}
		main, stream, gpu, err := atlas.TextureParts(use.Texture.Payload, result.Image, out.pixels)
		if err != nil {
			return err
		}
		if err = s.put(use.Texture, main, stream, gpu); err != nil {
			return err
		}
		s.report.Atlases = append(s.report.Atlases, AtlasReport{result.Report, use.Archive, use.Group, w, h, len(result.Glyphs), fmt.Sprintf("%016x", use.Baked.Entry.NameID), fmt.Sprintf("%016x", use.Texture.Entry.NameID), use.Template.Name, int(use.Template.Weight), s.styled[use.Template.Digest].Digest})
	}
	return nil
}
