package pipeline

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"hd2font/native/internal/inventory"
)

func (s *buildState) stage(label string) func(int, int) {
	return func(done, total int) { s.progress(fmt.Sprintf("%s %d/%d", label, done, total)) }
}

func mainTextGroup(group string) bool {
	switch group {
	case "sc", "tc", "latin", "jp", "ko":
		return true
	}
	return false
}

// fallbackKey names a fallback list: one per custom face, and in family mode
// one per target weight too, since family siblings are ordered by distance to it.
func (s *buildState) fallbackKey(original, custom *inventory.Face) string {
	if len(s.family) > 1 {
		return fmt.Sprintf("%s@%d", custom.Digest, original.Weight)
	}
	return custom.Digest
}

func (s *buildState) styles(raw []byte, axes []axis, pixelGrid bool) error {
	type request struct {
		key, method        string
		axes               map[string]float32
		weight             int
		prepare, emphasize bool
		// source is the family face used for this weight; emphasis is the
		// weight passed to Style when that face still needs emboldening.
		source   *inventory.Face
		emphasis int
	}
	var wght *axis
	for i := range axes {
		if axes[i].Tag == "wght" {
			wght = &axes[i]
		}
	}
	requests := make([]request, len(s.templates))
	indices := make(map[string]int)
	var jobs []func(context.Context) (*inventory.Face, error)
	for i, original := range s.templates {
		r := request{axes: cloneAxes(s.options.Axes), weight: int(original.Weight), method: "authored_static"}
		if len(axes) > 0 {
			r.method = "native_variable"
		}
		if s.options.MatchWeights && wght != nil {
			value, ok := r.axes["wght"]
			if !ok {
				value = wght.Default
			}
			r.axes["wght"] = max(wght.Min, min(wght.Max, value+float32(r.weight-400)))
			r.prepare = true
		} else if s.options.MatchWeights && len(s.family) > 1 {
			r.source, r.emphasis = s.pickFace(r.weight)
			r.method = "family_face"
			if r.emphasis > 0 {
				r.emphasize = true
				r.method = "family_synthetic_emphasis"
			}
		} else if s.options.MatchWeights && r.weight > 400 {
			r.emphasize = true
			r.method = "synthetic_emphasis"
		}
		r.key = jobKey(r.axes, r.prepare, r.emphasize, pixelGrid, s.options.ProcessOutlines)
		if r.emphasize {
			r.key = jobKey(r.key, r.weight)
		}
		if r.source != nil {
			r.key = jobKey(r.key, r.source.Digest, r.emphasis, s.familyPixel[r.source.Digest])
		}
		requests[i] = r
		if _, ok := indices[r.key]; ok {
			continue
		}
		indices[r.key] = len(jobs)
		jobs = append(jobs, func(ctx context.Context) (*inventory.Face, error) {
			var data []byte
			var err error
			switch {
			case r.prepare:
				data, err = s.ops.Prepare(ctx, raw, uint32(s.options.FaceIndex), r.axes, s.options.ProcessOutlines)
			case r.emphasize && r.source != nil:
				data, err = s.ops.Style(ctx, r.source.Data, r.emphasis, s.familyPixel[r.source.Digest], s.options.ProcessOutlines)
			case r.emphasize:
				data, err = s.ops.Style(ctx, s.base.Data, r.weight, pixelGrid, s.options.ProcessOutlines)
			case r.source != nil:
				return r.source, nil
			default:
				return s.base, nil
			}
			if err != nil {
				return nil, err
			}
			face, err := inventory.InspectFace(data)
			if err != nil {
				return nil, err
			}
			required := s.base.Chars
			if r.source != nil {
				required = r.source.Chars
			}
			if err = hasCoverage(face, required, "weight styling"); err != nil {
				return nil, err
			}
			return face, nil
		})
	}
	results, err := runJobs(s.ctx, jobs, s.options.Workers, s.stage("Preparing font weights"))
	if err != nil {
		return err
	}
	s.report.Performance["unique_styles"] = len(jobs)
	styleWeight := make(map[string]int)
	mode := "uniform"
	if s.options.MatchWeights {
		mode = "game"
	}
	for i, original := range s.templates {
		r := requests[i]
		face := results[indices[r.key]]
		s.styled[original.Digest] = face
		styleWeight[s.fallbackKey(original, face)] = r.weight
		s.report.StyleAdaptation = append(s.report.StyleAdaptation, StyleReport{r.weight, r.method, r.axes, mode, s.options.ProcessOutlines, pixelGrid, face.Digest, face.Name, original.Name, original.Digest, original.Group})
	}
	chinese := append(append([]*inventory.Face{}, s.inv.Groups["sc"]...), s.inv.Groups["tc"]...)
	seen := make(map[string]map[string]bool)
	for _, original := range s.templates {
		digest := s.fallbackKey(original, s.styled[original.Digest])
		if seen[digest] == nil {
			seen[digest] = make(map[string]bool)
		}
		sources := append([]*inventory.Face{}, s.inv.Groups[original.Group]...)
		if original.Group == "sc" || original.Group == "tc" {
			sources = append(sources, chinese...)
		}
		for _, f := range sources {
			if !seen[digest][f.Digest] {
				seen[digest][f.Digest] = true
				s.fallbacks[digest] = append(s.fallbacks[digest], f)
			}
		}
	}
	for digest, sources := range s.fallbacks {
		sort.SliceStable(sources, func(i, j int) bool {
			return distance(int(sources[i].Weight), styleWeight[digest]) < distance(int(sources[j].Weight), styleWeight[digest])
		})
	}
	// Glyphs a family face lacks come from its siblings before the game's.
	prefixed := make(map[string]bool)
	for i, original := range s.templates {
		r := requests[i]
		digest := s.fallbackKey(original, s.styled[original.Digest])
		if r.source == nil || prefixed[digest] {
			continue
		}
		prefixed[digest] = true
		s.fallbacks[digest] = append(s.familyFallbacks(r.source, r.weight), s.fallbacks[digest]...)
	}
	return nil
}

func distance(a, b int) int {
	if a < b {
		return b - a
	}
	return a - b
}

func (s *buildState) fonts() error {
	started := time.Now()
	type mergeResult struct {
		data     []byte
		required map[rune]struct{}
		face     *inventory.Face
	}
	mergeIDs := make(map[string]int)
	templateMerge := make(map[string]int)
	var mergeJobs []func(context.Context) (mergeResult, error)
	for _, original := range s.templates {
		custom := s.styled[original.Digest]
		fallbacks := s.fallbacks[s.fallbackKey(original, custom)]
		ids := make([]string, len(fallbacks))
		for i, f := range fallbacks {
			ids[i] = f.Digest
		}
		key := jobKey(custom.Digest, ids)
		i, ok := mergeIDs[key]
		if !ok {
			i = len(mergeJobs)
			mergeIDs[key] = i
			mergeJobs = append(mergeJobs, func(ctx context.Context) (mergeResult, error) {
				parts := make([][]byte, len(fallbacks))
				for i, f := range fallbacks {
					parts[i] = f.Data
				}
				required := unionFaces(append([]*inventory.Face{custom}, fallbacks...)...)
				data, err := s.ops.Merge(ctx, custom.Data, parts)
				if err != nil {
					return mergeResult{}, err
				}
				face, err := inventory.InspectFace(data)
				if err != nil {
					return mergeResult{}, err
				}
				if err = hasCoverage(face, required, "fallback merge"); err != nil {
					return mergeResult{}, err
				}
				return mergeResult{data, required, face}, nil
			})
		}
		templateMerge[original.Digest] = i
	}
	merged, err := runJobs(s.ctx, mergeJobs, s.options.Workers, s.stage("Merging full font repertoires"))
	if err != nil {
		return err
	}
	s.merged = make(map[string]*inventory.Face, len(s.templates))
	for _, original := range s.templates {
		s.merged[original.Digest] = merged[templateMerge[original.Digest]].face
	}
	s.report.Performance["unique_merges"] = len(mergeJobs)
	s.report.Performance["merge_seconds"] = time.Since(started).Seconds()
	started = time.Now()
	conversionIDs := make(map[string]int)
	templateConversion := make(map[string]int)
	var conversionJobs []func(context.Context) (replacement, error)
	for _, original := range s.templates {
		m := merged[templateMerge[original.Digest]]
		layout := s.styled[original.Digest]
		key := jobKey(m.face.Digest, original.Format, original.UPM, layout.Digest, s.options.ProcessOutlines)
		i, ok := conversionIDs[key]
		if !ok {
			i = len(conversionJobs)
			conversionIDs[key] = i
			conversionJobs = append(conversionJobs, func(ctx context.Context) (replacement, error) {
				data, metrics, err := s.ops.Conform(ctx, m.data, original.Format == "ttf", original.UPM, layout.Data, s.options.ProcessOutlines)
				if err != nil {
					return replacement{}, err
				}
				for _, value := range metrics {
					if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
						return replacement{}, fmt.Errorf("non-finite runtime metrics")
					}
				}
				face, err := inventory.InspectFace(data)
				if err != nil {
					return replacement{}, err
				}
				if face.Format != original.Format || face.UPM != original.UPM {
					return replacement{}, fmt.Errorf("converted font profile differs from original %q", original.Name)
				}
				if err = hasCoverage(face, m.required, "format conversion"); err != nil {
					return replacement{}, err
				}
				return replacement{data, metrics, m.required}, nil
			})
		}
		templateConversion[original.Digest] = i
	}
	converted, err := runJobs(s.ctx, conversionJobs, s.options.Workers, s.stage("Conforming font formats"))
	if err != nil {
		return err
	}
	s.report.Performance["unique_conversions"] = len(conversionJobs)
	var cffProfiles []map[string]any
	for _, result := range converted {
		if len(result.data) >= 4 && string(result.data[:4]) == "OTTO" {
			digest := inventory.Digest(result.data)
			cffProfiles = append(cffProfiles, map[string]any{"output_sha256": digest, "after_bytes": len(result.data), "method": "v13-absolute-integer-grid-cff", "post_encoding_compression": false})
		}
	}
	sort.Slice(cffProfiles, func(i, j int) bool {
		return cffProfiles[i]["output_sha256"].(string) < cffProfiles[j]["output_sha256"].(string)
	})
	if len(cffProfiles) > 0 {
		s.report.LosslessCompression["cff_profiles"] = cffProfiles
	}
	s.report.Performance["format_compression_seconds"] = time.Since(started).Seconds()
	started = time.Now()
	for _, original := range s.templates {
		if err = s.ctx.Err(); err != nil {
			return err
		}
		converted := converted[templateConversion[original.Digest]]
		data, err := s.ops.Register(converted.data, original.Data)
		if err != nil {
			return err
		}
		face, err := inventory.InspectFace(data)
		if err != nil {
			return err
		}
		if err = hasCoverage(face, converted.required, "font registration"); err != nil {
			return err
		}
		if face.Name != original.Name || face.Weight != original.Weight || face.Format != original.Format || face.UPM != original.UPM {
			return fmt.Errorf("registration changed original identity/profile %q", original.Name)
		}
		converted.data = data
		s.replacements[original.Digest] = converted
		s.report.FontReadback = append(s.report.FontReadback, FontVerification{original.Digest, inventory.Digest(data), len(s.base.Chars), len(converted.required), len(face.Chars), true, true, face.Format, face.UPM})
	}
	s.report.Performance["registration_seconds"] = time.Since(started).Seconds()
	s.report.Performance["unique_registrations"] = len(s.replacements)
	return nil
}
