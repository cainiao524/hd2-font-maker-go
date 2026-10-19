//go:build windows && amd64

// Package production connects the native UI to the native font and ZIP pipeline.
package production

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"

	"hd2font/native/internal/fontcore"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/pipeline"
	"hd2font/native/mygoapp"
)

var ErrClosed = errors.New("native font backend is closed")

// Backend stores configuration only. Native processors and their conversion
// caches belong to one preview/build and are closed before that job returns.
type Backend struct {
	binDir, userData  string
	workers           int
	mu                sync.Mutex
	closed            bool
	nextJob           uint64
	jobs              map[uint64]context.CancelFunc
	wg                sync.WaitGroup
	deps              dependencies
	previewGameFolder string
	previewGameFonts  map[string][]byte
}

type dependencies struct {
	inspect func(context.Context, []byte, string) (mygoapp.FontInfo, error)
	scan    func(context.Context, string, func(string)) (*inventory.Inventory, error)
	build   func(context.Context, *inventory.Inventory, pipeline.Options, func(string)) (*pipeline.Report, error)
	preview func(context.Context, []byte, mygoapp.PreviewRequest) (mygoapp.PreviewResult, error)
}

var _ mygoapp.Backend = (*Backend)(nil)

// New does not load DLLs, scan a game, or retain font data. binDir must be the
// executable directory or its explicit bin directory; it never searches PATH.
func New(binDir, userData string) (*Backend, error) {
	if binDir == "" || userData == "" {
		return nil, errors.New("native binary and portable UserData directories are required")
	}
	binDir, err := filepath.Abs(binDir)
	if err != nil {
		return nil, err
	}
	userData, err = filepath.Abs(userData)
	if err != nil {
		return nil, err
	}
	b := &Backend{binDir: binDir, userData: userData, workers: max(1, min(12, runtime.NumCPU()-2)), jobs: make(map[uint64]context.CancelFunc)}
	b.deps = dependencies{inspect: inspectData, scan: scanInventory, build: b.build, preview: b.preview}
	return b, nil
}

func (b *Backend) begin(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, nil, ErrClosed
	}
	ctx, cancel := context.WithCancel(ctx)
	b.nextJob++
	id := b.nextJob
	b.jobs[id] = cancel
	b.wg.Add(1)
	return ctx, func() { cancel(); b.mu.Lock(); delete(b.jobs, id); b.mu.Unlock(); b.wg.Done() }, nil
}

// Close cancels outstanding jobs, then waits for each job's native resources
// and temporary files to be released. It is safe to call more than once.
func (b *Backend) Close() error {
	b.mu.Lock()
	b.closed = true
	for _, cancel := range b.jobs {
		cancel()
	}
	b.mu.Unlock()
	b.wg.Wait()
	return nil
}

func (b *Backend) InspectFont(ctx context.Context, path string) (mygoapp.FontInfo, error) {
	ctx, done, err := b.begin(ctx)
	if err != nil {
		return mygoapp.FontInfo{}, err
	}
	defer done()
	data, err := readSource(ctx, path)
	if err != nil {
		return mygoapp.FontInfo{}, err
	}
	info, err := b.deps.inspect(ctx, data, path)
	if err != nil {
		return mygoapp.FontInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return mygoapp.FontInfo{}, err
	}
	stored, err := storeFont(ctx, b.userData, path, data)
	if err != nil {
		return mygoapp.FontInfo{}, err
	}
	info.Path = stored
	return info, nil
}

func (b *Backend) Preview(ctx context.Context, req mygoapp.PreviewRequest) (mygoapp.PreviewResult, error) {
	ctx, done, err := b.begin(ctx)
	if err != nil {
		return mygoapp.PreviewResult{}, err
	}
	defer done()
	if req.GameFontID != "" {
		return b.originalGamePreview(ctx, req)
	}
	if req.Font.FaceIndex < 0 {
		return mygoapp.PreviewResult{}, errors.New("font face index must be nonnegative")
	}
	req.Font.Axes = maps.Clone(req.Font.Axes)
	data, err := readSource(ctx, req.Font.Path)
	if err != nil {
		return mygoapp.PreviewResult{}, err
	}
	if err := verifyStoredSource(b.userData, req.Font.Path, data); err != nil {
		return mygoapp.PreviewResult{}, err
	}
	return b.deps.preview(ctx, data, req)
}

func (b *Backend) Generate(ctx context.Context, req mygoapp.BuildRequest, progress func(mygoapp.Progress)) (mygoapp.BuildResult, error) {
	ctx, done, err := b.begin(ctx)
	if err != nil {
		return mygoapp.BuildResult{}, err
	}
	defer done()
	if key := mygoapp.ValidateRequest(req); key != "" {
		return mygoapp.BuildResult{}, fmt.Errorf("%s", mygoapp.Translate("en", key))
	}
	if err := outsideGame(req.GameFolder, b.userData, req.OutputZIP); err != nil {
		return mygoapp.BuildResult{}, err
	}
	data, err := readSource(ctx, req.Font.Path)
	if err != nil {
		return mygoapp.BuildResult{}, err
	}
	if err := verifyStoredSource(b.userData, req.Font.Path, data); err != nil {
		return mygoapp.BuildResult{}, err
	}
	// Publish an immutable, byte-identical source path. Only Preview subsets;
	// the pipeline always reads this complete source, including every glyph.
	stored, err := storeFont(ctx, b.userData, req.Font.Path, data)
	if err != nil {
		return mygoapp.BuildResult{}, err
	}
	data = nil
	if progress == nil {
		progress = func(mygoapp.Progress) {}
	}
	progress(mygoapp.Progress{Stage: "scan", Fraction: -1})
	inv, err := b.deps.scan(ctx, req.GameFolder, func(s string) { progress(progressMessage(s, "scan")) })
	if err != nil {
		return mygoapp.BuildResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return mygoapp.BuildResult{}, err
	}
	if inv.Catalog != nil {
		if err := outsideGame(filepath.Dir(inv.Catalog.Data), b.userData, req.OutputZIP); err != nil {
			return mygoapp.BuildResult{}, err
		}
	}
	opts := pipeline.DefaultOptions()
	opts.FontPath = stored
	opts.FaceIndex = req.Font.FaceIndex
	opts.Axes = maps.Clone(req.Font.Axes)
	opts.Languages = slices.Clone(req.Languages)
	opts.MatchWeights = req.MatchWeights
	opts.ProcessOutlines = req.ProcessOutlines
	opts.IncludeSymbols = req.IncludeSymbols
	opts.OutputZIP = req.OutputZIP
	opts.BinDir = b.binDir
	opts.WorkDir = filepath.Join(b.userData, "Work")
	opts.AtlasExecutable = filepath.Join(b.binDir, "vendor", "msdf-atlas-gen", "msdf-atlas-gen.exe")
	opts.Name = "HD2 Custom Fonts"
	report, err := b.deps.build(ctx, inv, opts, func(s string) { progress(progressMessage(s, "working")) })
	if err != nil {
		return mygoapp.BuildResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return mygoapp.BuildResult{}, err
	}
	if report == nil || !report.OfflineReadbackVerified {
		return mygoapp.BuildResult{}, errors.New("native pipeline did not verify the output package")
	}
	if report.Output == "" {
		return mygoapp.BuildResult{}, errors.New("native pipeline returned no output ZIP")
	}
	stat, err := os.Stat(report.Output)
	if err != nil {
		return mygoapp.BuildResult{}, err
	}
	var groups []string
	for _, selection := range report.Selections {
		if !slices.Contains(groups, selection.Group) {
			groups = append(groups, selection.Group)
		}
	}
	progress(mygoapp.Progress{Stage: "done", Fraction: 1})
	return mygoapp.BuildResult{OutputZIP: report.Output, Bytes: stat.Size(), ResourceGroups: groups}, nil
}

func (b *Backend) build(ctx context.Context, inv *inventory.Inventory, opts pipeline.Options, progress func(string)) (*pipeline.Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	processor, err := fontcore.NewProcessor(b.binDir, b.workers)
	if err != nil {
		return nil, err
	}
	defer processor.Close()
	return pipeline.Build(ctx, inv, processor, opts, progress)
}
