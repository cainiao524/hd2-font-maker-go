package mygoapp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type application struct {
	settings       Settings
	settingsPath   string
	backend        Backend
	canGenerate    bool
	demo           bool
	window         *mygo.Window
	wake           func()
	events         chan func()
	progressEvents chan Progress
	closed         chan struct{}
	faces          []FontFace
	faceName       string
	fontDraft      string
	localeName     string
	busy           bool
	cancelling     bool
	cancel         context.CancelFunc
	previewCancel  context.CancelFunc
	previewSerial  uint64
	previewBusy    bool
	preview        *ui.Bitmap
	gameFonts      []GameFont
	gameFontFolder string
	gameFontID     string
	previewMissing int
	progress       Progress
	statusKey      string
	errorKey       string
	errorDetail    string
	started        time.Time
	dirtyAt        time.Time
	previewDirtyAt time.Time
	scroll         ui.ScrollState
	theme          *ui.Theme
	// notice is a catalog key shown once as a toast by the next frame.
	notice   string
	dragOver bool
	// facesPath is the stored file faces describes; siblings are same-family
	// faces of other files found beside it at import, and sourceFace is the
	// family member the selection uses as its source.
	facesPath   string
	siblings    []FamilyFace
	sourceFace  FamilyFace
	lastAutoMod string
	// compare shows the game font beside the custom one, split at split (%);
	// previewWeight is the weight row the preview draws.
	compare           bool
	split             float32
	previewWeight     int
	sampleDraft       string
	gamePreview       *ui.Bitmap
	gamePreviewBusy   bool
	gamePreviewSerial uint64
	gamePreviewCancel context.CancelFunc
	manualWeights     bool
	// done keeps the completion panel until settings change; generating marks
	// a build, as distinct from imports and scans that also set busy.
	done        bool
	generating  bool
	lastBytes   int64
	lastElapsed time.Duration
	heroWidth   float32
}

func newApplication(opts Options) (*application, error) {
	path := opts.SettingsPath
	if path == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(filepath.Dir(exe), "UserData", "mygo-settings.json")
	}
	s, loadErr := LoadSettings(path)
	if opts.Locale != "" {
		s.Locale = ResolveLocale(opts.Locale)
	}
	if opts.InitialFont != "" {
		s.Font = FontSelection{Path: opts.InitialFont}
	}
	if s.PreviewText == "" {
		s.PreviewText = Translate(s.Locale, "preview.sample")
	}
	if s.OutputZIP == "" {
		s.OutputZIP = filepath.Join(filepath.Dir(path), "HD2-Custom-Font.zip")
	}
	a := &application{settings: s, settingsPath: path, backend: opts.Backend,
		canGenerate: opts.Backend != nil, demo: opts.Demo, fontDraft: s.Font.Path,
		events: make(chan func(), 32), progressEvents: make(chan Progress, 1), closed: make(chan struct{}),
		statusKey: "status.initial", theme: ui.DarkTheme(), split: 52, previewWeight: 400,
	}
	a.sampleDraft = flatSample(s.PreviewText)
	if a.backend == nil {
		a.backend = FontPreviewBackend{}
	}
	for _, loc := range Locales {
		if loc.Code == s.Locale {
			a.localeName = loc.Name
		}
	}
	if loadErr != nil {
		a.setError("error.failed", loadErr)
	}
	return a, nil
}

func (a *application) t(key string, pairs ...string) string {
	return Translate(a.settings.Locale, key, pairs...)
}
func (a *application) native(index int) string { return nativeMessages[a.settings.Locale][index] }

func (a *application) post(fn func()) {
	select {
	case a.events <- fn:
		if a.wake != nil {
			a.wake()
		}
	case <-a.closed:
	}
}

func (a *application) drain() {
	for {
		select {
		case fn := <-a.events:
			fn()
		default:
			goto progress
		}
	}
progress:
	select {
	case p := <-a.progressEvents:
		if a.busy {
			a.progress = p
		}
	default:
	}
}

func (a *application) changed(preview bool) {
	a.dirtyAt = time.Now()
	a.done = false
	if preview {
		a.requestPreview()
	}
}

func (a *application) save() {
	if err := SaveSettings(a.settingsPath, a.settings); err != nil {
		a.setError("error.failed", err)
	}
	a.dirtyAt = time.Time{}
}

func (a *application) setError(key string, err error) {
	a.errorKey = key
	if err != nil {
		a.errorDetail = err.Error()
	} else {
		a.errorDetail = ""
	}
	if key != "" {
		// Diagnostics are best-effort; keep the original error visible if writing fails.
		_ = appendErrorDiagnostic(a.settingsPath, key, a.errorDetail)
	}
}

func (a *application) inspectFont(path string) {
	if a.busy || path == "" {
		return
	}
	a.busy, a.cancelling = true, false
	a.errorKey, a.errorDetail = "", ""
	a.progress = Progress{Stage: "source", Fraction: -1}
	a.started = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	go func() {
		defer cancel()
		info, err := a.backend.InspectFont(ctx, path)
		if err == nil {
			err = ctx.Err()
		}
		a.post(func() {
			a.busy, a.cancelling, a.cancel = false, false, nil
			if errors.Is(err, context.Canceled) {
				a.statusKey = "native.cancelled"
				return
			}
			if err != nil {
				a.setError("error.font_read", err)
				return
			}
			if len(info.Faces) == 0 {
				a.setError("error.font_read", errors.New("font contains no faces"))
				return
			}
			previous := a.settings.Font
			selectedPath := info.Path
			if selectedPath == "" {
				selectedPath = path
			}
			a.faces, a.facesPath, a.siblings = info.Faces, selectedPath, info.Siblings
			reload := previous.Path == path || previous.Path == selectedPath
			related, selectedIndex := reload, previous.FaceIndex
			if !related {
				// Importing Bold may have anchored the source at a separate Regular
				// file. Recognize that original member when it is imported again.
				for _, member := range previous.Family {
					if member.Path == selectedPath {
						related, selectedIndex = true, member.FaceIndex
						break
					}
				}
			}
			selected := info.Faces[0]
			if related {
				for _, face := range info.Faces {
					if face.Index == selectedIndex {
						selected = face
					}
				}
			}
			restoringCache := related && path == selectedPath
			if restoringCache {
				// Cached members live in separate folders. Restore the known family
				// when loading any member, including its previous base. Importing an
				// original file still discovers the current directory's members.
				for _, member := range a.members() {
					if member.Path != "" && member.Path != selectedPath {
						a.siblings = append(a.siblings, member)
					}
				}
			}
			if reload && restoringCache && len(previous.Family) > 0 {
				// Restoring the saved base keeps its exact identity, including when
				// matching was enabled after explicitly selecting a heavier face.
				a.sourceFace = candidate(selectedPath, selected)
				a.settings.Font = FontSelection{Path: selectedPath, FaceIndex: selected.Index, Family: previous.Family, WeightFaces: previous.WeightFaces}
			} else {
				a.applyFamily(familyOf(selectedPath, info.Faces, a.siblings, selected))
				if related {
					a.settings.Font.WeightFaces = remapWeightFaces(previous, a.members())
				}
			}
			a.settings.Font.Axes = nil
			for _, face := range info.Faces {
				if a.settings.Font.Path == selectedPath && face.Index == a.settings.Font.FaceIndex {
					a.faceName = faceLabel(face)
					var saved map[string]float32
					if reload && previous.FaceIndex == face.Index {
						saved = previous.Axes
					}
					a.settings.Font.Axes = axisValues(face, saved)
				}
			}
			a.updateModName()
			selectedPath = a.settings.Font.Path
			a.fontDraft = selectedPath
			a.statusKey = "status.imported"
			a.gameFontID = ""
			a.changed(true)
		})
	}()
}

func faceLabel(face FontFace) string { return fmt.Sprintf("%d  %s", face.Index+1, face.Name) }

func (a *application) requestPreview() {
	a.previewDirtyAt = time.Time{}
	a.requestGamePreview()
	if a.previewCancel != nil {
		a.previewCancel()
	}
	a.previewSerial++
	serial := a.previewSerial
	a.preview = nil
	a.previewBusy, a.previewMissing = false, 0
	if a.settings.Font.Path == "" || len(a.faces) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.previewCancel, a.previewBusy = cancel, true
	req := PreviewRequest{Font: a.settings.Font, Text: a.settings.PreviewText,
		MatchWeights: a.settings.MatchWeights, ProcessOutlines: a.settings.ProcessOutlines, Width: 1000, Height: 480}
	if req.MatchWeights {
		req.Weight = a.previewWeight
	}
	req.Font.Axes = maps.Clone(req.Font.Axes)
	req.Font.Family = slices.Clone(req.Font.Family)
	req.Font.WeightFaces = maps.Clone(req.Font.WeightFaces)
	go func() {
		defer cancel()
		result, err := a.backend.Preview(ctx, req)
		a.post(func() {
			if serial != a.previewSerial {
				return
			}
			a.previewBusy, a.previewCancel = false, nil
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					a.setError("error.font_read", err)
				}
				return
			}
			if result.Image == nil {
				a.setError("error.font_read", errors.New("preview returned no image"))
				return
			}
			a.preview, a.previewMissing = ui.NewBitmap(result.Image), result.MissingCharacters
		})
	}()
}

// requestGamePreview renders the chosen original game font for comparison.
func (a *application) requestGamePreview() {
	if a.gamePreviewCancel != nil {
		a.gamePreviewCancel()
	}
	a.gamePreviewSerial++
	serial := a.gamePreviewSerial
	a.gamePreview, a.gamePreviewBusy = nil, false
	if a.compare && a.gameFontID == "" && len(a.gameFonts) > 0 {
		a.gameFontID = a.gameFonts[0].ID
	}
	if a.gameFontID == "" || (!a.compare && len(a.faces) > 0) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.gamePreviewCancel, a.gamePreviewBusy = cancel, true
	req := PreviewRequest{Text: a.settings.PreviewText, GameFolder: a.settings.GameFolder, GameFontID: a.gameFontID, Width: 1000, Height: 480}
	go func() {
		defer cancel()
		result, err := a.backend.Preview(ctx, req)
		a.post(func() {
			if serial != a.gamePreviewSerial {
				return
			}
			a.gamePreviewBusy, a.gamePreviewCancel = false, nil
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					a.setError("error.font_read", err)
				}
				return
			}
			if result.Image != nil {
				a.gamePreview = ui.NewBitmap(result.Image)
			}
		})
	}()
}

func (a *application) generate() {
	if a.busy || !a.canGenerate {
		return
	}
	if a.fontDraft != a.settings.Font.Path || len(a.faces) == 0 {
		a.setError("error.no_font", nil)
		return
	}
	req := a.settings.BuildRequest
	req.Languages = slices.Clone(req.Languages)
	req.Font.Axes = maps.Clone(req.Font.Axes)
	req.Font.Family = slices.Clone(req.Font.Family)
	req.Font.WeightFaces = maps.Clone(req.Font.WeightFaces)
	if key := ValidateRequest(req); key != "" {
		a.setError(key, nil)
		return
	}
	a.save()
	a.busy, a.cancelling, a.generating, a.done = true, false, true, false
	a.errorKey, a.errorDetail = "", ""
	a.progress = Progress{Stage: "scan", Fraction: 0}
	a.started = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	go func() {
		defer cancel()
		result, err := a.backend.Generate(ctx, req, func(p Progress) {
			select {
			case a.progressEvents <- p:
			default:
				select {
				case <-a.progressEvents:
				default:
				}
				select {
				case a.progressEvents <- p:
				default:
				}
			}
			if a.wake != nil {
				a.wake()
			}
		})
		a.post(func() {
			a.busy, a.cancelling, a.cancel, a.generating = false, false, nil, false
			if errors.Is(err, context.Canceled) {
				a.statusKey = "native.cancelled"
				return
			}
			if err != nil {
				a.setError("error.failed", err)
				a.statusKey = "error.failed"
				return
			}
			if result.OutputZIP == "" {
				a.setError("error.failed", errors.New("backend returned no output ZIP"))
				return
			}
			if _, err := os.Stat(result.OutputZIP); err != nil {
				a.setError("error.cache", err)
				return
			}
			a.settings.LastOutput = result.OutputZIP
			a.lastBytes, a.lastElapsed = result.Bytes, time.Since(a.started)
			a.progress = Progress{Stage: "done", Fraction: 1}
			a.statusKey = "status.complete"
			a.notice = "stage.done"
			a.save()
			a.done = true
		})
	}()
}

func (a *application) cancelWork() {
	if a.cancel != nil {
		a.cancelling = true
		a.cancel()
	}
}

func (a *application) chooseGame() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.window, Title: a.t("dialog.game"), DefaultPath: a.settings.GameFolder, Directory: true})
	if err != nil {
		a.setError("error.game", err)
		return
	}
	if len(paths) != 0 {
		a.settings.GameFolder = paths[0]
		a.changed(false)
	}
}

func (a *application) chooseFont() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.window, Title: a.t("dialog.import"), DefaultPath: a.fontDraft,
		Filters: []mygo.FileFilter{{Name: a.t("file.fonts"), Extensions: []string{"ttf", "otf", "ttc", "otc"}}}})
	if err != nil {
		a.setError("error.font_read", err)
		return
	}
	if len(paths) != 0 {
		a.inspectFont(paths[0])
	}
}

func (a *application) chooseOutput() {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: a.window, Title: a.t("dialog.export_zip"), DefaultPath: a.settings.OutputZIP,
		Filters: []mygo.FileFilter{{Name: a.t("file.zip"), Extensions: []string{"zip"}}}})
	if err != nil {
		a.setError("error.output_zip", err)
		return
	}
	if path != "" {
		if filepath.Ext(path) == "" {
			path += ".zip"
		}
		a.settings.OutputZIP = path
		a.changed(false)
	}
}

func (a *application) openOutput() {
	if a.settings.LastOutput == "" {
		return
	}
	if _, err := os.Stat(a.settings.LastOutput); err != nil {
		a.setError("error.cache", err)
		return
	}
	mygo.Shell.ShowItemInFolder(a.settings.LastOutput)
}

// Run opens a native MyGo window. Call it from the executable's main goroutine.
func Run(opts Options) error {
	a, err := newApplication(opts)
	if err != nil {
		return err
	}
	mygo.App.WhenReady(func() {
		w, h := opts.Width, opts.Height
		if w == 0 {
			w = 1140
		}
		if h == 0 {
			h = 860
		}
		a.window = mygo.NewWindow(mygo.WindowOptions{Title: a.t("app.title") + " " + Version, Width: w, Height: h,
			MinWidth: 540, MinHeight: 520, Content: ui.View(a.view)})
		a.wake = a.window.Invalidate
		// The window keeps the default icon if the PNG cannot be used.
		_ = a.window.SetIcon(appIcon)
		a.window.OnClosed(func() {
			close(a.closed)
			if a.cancel != nil {
				a.cancel()
			}
			if a.previewCancel != nil {
				a.previewCancel()
			}
			if a.gamePreviewCancel != nil {
				a.gamePreviewCancel()
			}
			a.save()
		})
		if a.settings.Font.Path != "" {
			a.inspectFont(a.settings.Font.Path)
		}
	})
	return mygo.App.Run()
}
