package pipeline

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"hd2font/native/internal/archiveio"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/sfntio"
)

type replacement struct {
	data     []byte
	metrics  [3]float32
	required map[rune]struct{}
}
type buildState struct {
	ctx          context.Context
	inv          *inventory.Inventory
	ops          fontOps
	options      Options
	progress     func(string)
	work         string
	report       *Report
	base         *inventory.Face
	selected     map[string]inventory.Selection
	templates    []*inventory.Face
	styled       map[string]*inventory.Face
	merged       map[string]*inventory.Face
	fallbacks    map[string][]*inventory.Face
	replacements map[string]replacement
	atlasUses    []inventory.AtlasSelection
	changes      map[string][]archiveio.Resource
	changed      map[string]*inventory.Resource
}

func build(ctx context.Context, inv *inventory.Inventory, ops fontOps, options Options, progress func(string), generate atlasGenerator) (*Report, error) {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if inv == nil || len(inv.Faces) == 0 || ops == nil {
		return nil, fmt.Errorf("font inventory and native processor are required")
	}
	if options.FaceIndex < 0 || uint64(options.FaceIndex) > math.MaxUint32 {
		return nil, fmt.Errorf("invalid font face index")
	}
	if options.ZIPLevel < 0 || options.ZIPLevel > 9 {
		return nil, fmt.Errorf("ZIP level must be between 0 and 9")
	}
	if options.FontPath == "" || options.OutputZIP == "" {
		return nil, fmt.Errorf("font path and output ZIP are required")
	}
	var err error
	options.FontPath, err = filepath.Abs(options.FontPath)
	if err != nil {
		return nil, err
	}
	options.OutputZIP, err = filepath.Abs(options.OutputZIP)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(options.OutputZIP), ".zip") {
		return nil, fmt.Errorf("output must use a .zip extension")
	}
	if strings.EqualFold(options.FontPath, options.OutputZIP) {
		return nil, fmt.Errorf("output cannot replace the input font")
	}
	if inv.Catalog != nil {
		root := inv.Catalog.Data
		if strings.EqualFold(filepath.Base(root), "data") {
			root = filepath.Dir(root)
		}
		for _, path := range []string{options.OutputZIP, options.WorkDir} {
			if path != "" && within(root, path) {
				return nil, fmt.Errorf("output and work directories must be outside the game directory")
			}
		}
	}
	options.Workers = max(1, min(2, options.Workers))
	if options.Name = strings.TrimSpace(options.Name); options.Name == "" {
		options.Name = "HD2 Custom Fonts"
	}
	if progress == nil {
		progress = func(string) {}
	}
	if options.WorkDir != "" {
		if err = os.MkdirAll(options.WorkDir, 0700); err != nil {
			return nil, err
		}
	}
	work, err := os.MkdirTemp(options.WorkDir, "hd2-font-build-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	s := &buildState{ctx: ctx, inv: inv, ops: ops, options: options, progress: progress, work: work, styled: make(map[string]*inventory.Face), fallbacks: make(map[string][]*inventory.Face), replacements: make(map[string]replacement), changes: make(map[string][]archiveio.Resource), changed: make(map[string]*inventory.Resource)}
	s.report = &Report{Tool: "HD2 Font Mod Generator", Version: "1.3.0-go.3", EngineProfile: "python-1.3.7-parity-v1", CreatedAt: time.Now().UTC().Format(time.RFC3339), Name: options.Name, Selections: []SelectionReport{}, Resources: []ResourceReport{}, Atlases: []AtlasReport{}, RuntimeSlots: []RuntimeReport{}, StyleAdaptation: []StyleReport{}, FontReadback: []FontVerification{}, FullReplacement: true, NoesisFamilyStylePreserved: true, Files: make(map[string]FileReport), Performance: map[string]any{"package_cache_hit": false, "cache_hits": 0, "cache_misses": 0, "style_workers": options.Workers, "merge_workers": options.Workers, "format_workers": options.Workers, "atlas_workers": options.Workers}, LosslessCompression: map[string]any{"cff_profiles": []any{}, "zip_deflate_level": options.ZIPLevel, "texture_bytes_unchanged": true, "resource_offset_aliasing": false}, Note: "Font resources and packaging verified offline; in-game rendering has not been verified. Fallback glyphs do not add game translations."}
	s.report.Performance["glyph_pipeline"] = "python-1.3.7-parity-v1"
	progress("Preparing selected font")
	raw, err := os.ReadFile(options.FontPath)
	if err != nil {
		return nil, err
	}
	f, err := sfntio.Parse(raw, options.FaceIndex)
	if err != nil {
		return nil, err
	}
	if len(f.Tables["head"]) < 20 {
		return nil, fmt.Errorf("input font has no units-per-em field")
	}
	pixelGrid := binary.BigEndian.Uint16(f.Tables["head"][18:]) < 1000
	sourceData, err := f.Bytes()
	if err != nil {
		return nil, err
	}
	sourceFace, err := inventory.InspectFace(sourceData)
	if err != nil {
		return nil, err
	}
	axes, err := ops.Axes(raw, options.FaceIndex)
	if err != nil {
		return nil, err
	}
	prepared, err := ops.Prepare(ctx, raw, uint32(options.FaceIndex), cloneAxes(options.Axes), options.ProcessOutlines)
	if err != nil {
		return nil, err
	}
	s.base, err = inventory.InspectFace(prepared)
	if err != nil {
		return nil, err
	}
	if err = hasCoverage(s.base, sourceFace.Chars, "preparation"); err != nil {
		return nil, err
	}
	s.selected, s.report.Coverage, err = inventory.LanguagePlan(inv, s.base, options.FontPath, options.FaceIndex, options.Languages, options.IncludeSymbols)
	if err != nil {
		return nil, err
	}
	var groups []string
	for _, row := range s.report.Coverage {
		if row.Selected {
			groups = append(groups, row.Group)
			s.report.Selections = append(s.report.Selections, SelectionReport{row.Group, row.Label, s.base.Name, s.base.Digest, options.FaceIndex, options.ProcessOutlines, row.OriginalCharacters, row.CustomCharacters, len(s.base.Chars), row.OriginalFallbackCharacters})
		}
	}
	s.atlasUses, err = inv.SelectAtlases(groups)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	add := func(face *inventory.Face) {
		if _, ok := s.selected[face.Group]; ok && !seen[face.Digest] {
			seen[face.Digest] = true
			s.templates = append(s.templates, face)
		}
	}
	for _, r := range inv.Resources {
		for _, face := range r.Faces {
			add(face)
		}
	}
	for _, use := range s.atlasUses {
		add(use.Template)
	}
	if len(s.templates) == 0 {
		return nil, fmt.Errorf("language selection has no replaceable font resources")
	}
	stage := time.Now()
	s.report.Performance["preparation_seconds"] = stage.Sub(started).Seconds()
	if err = s.styles(raw, axes, pixelGrid); err != nil {
		return nil, err
	}
	s.report.Performance["style_seconds"] = time.Since(stage).Seconds()
	stage = time.Now()
	if err = s.fonts(); err != nil {
		return nil, err
	}
	s.report.Performance["font_merge_seconds"] = time.Since(stage).Seconds()
	stage = time.Now()
	if err = s.fontResources(); err != nil {
		return nil, err
	}
	s.report.Performance["runtime_font_seconds"] = time.Since(stage).Seconds()
	stage = time.Now()
	if err = s.atlases(generate); err != nil {
		return nil, err
	}
	s.report.Performance["atlas_seconds"] = time.Since(stage).Seconds()
	if len(s.changes) == 0 {
		return nil, fmt.Errorf("selected plan produced no font replacements")
	}
	if err = s.packageOutput(); err != nil {
		return nil, err
	}
	s.report.Performance["total_seconds"] = time.Since(started).Seconds()
	progress("Font package complete: " + filepath.Base(options.OutputZIP))
	return s.report, nil
}

func within(root, path string) bool {
	root, _ = filepath.Abs(root)
	path, _ = filepath.Abs(path)
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func cloneAxes(source map[string]float32) map[string]float32 {
	m := make(map[string]float32, len(source))
	for k, v := range source {
		m[k] = v
	}
	return m
}
func jobKey(values ...any) string { data, _ := json.Marshal(values); return inventory.Digest(data) }

func hasCoverage(face *inventory.Face, required map[rune]struct{}, stage string) error {
	var missing []rune
	for cp := range required {
		if _, ok := face.Chars[cp]; !ok {
			missing = append(missing, cp)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	return fmt.Errorf("%s lost %d required codepoints (first: %U)", stage, len(missing), missing[:min(8, len(missing))])
}

func unionFaces(faces ...*inventory.Face) map[rune]struct{} {
	result := make(map[rune]struct{})
	for _, f := range faces {
		for cp := range f.Chars {
			result[cp] = struct{}{}
		}
	}
	return result
}

// Results retain job order even when independent native jobs finish out of order.
func runJobs[T any](ctx context.Context, jobs []func(context.Context) (T, error), workers int, progress func(int, int)) ([]T, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]T, len(jobs))
	if len(jobs) == 0 {
		return results, ctx.Err()
	}
	queue := make(chan int, len(jobs))
	for i := range jobs {
		queue <- i
	}
	close(queue)
	type event struct {
		index int
		value T
		err   error
	}
	events := make(chan event, max(1, workers))
	var wg sync.WaitGroup
	for range min(max(1, workers), len(jobs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				if ctx.Err() != nil {
					return
				}
				value, err := jobs[i](ctx)
				events <- event{i, value, err}
				if err != nil {
					return
				}
			}
		}()
	}
	go func() { wg.Wait(); close(events) }()
	var first error
	done := 0
	for event := range events {
		if event.err != nil {
			if first == nil {
				first = event.err
				cancel()
			}
			continue
		}
		results[event.index] = event.value
		done++
		if progress != nil {
			progress(done, len(jobs))
		}
	}
	if first != nil {
		return nil, first
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
