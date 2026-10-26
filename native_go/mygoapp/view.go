package mygoapp

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

var gameLanguages = []string{"sc", "tc", "latin", "jp", "ko"}

var fontExtensions = []string{".ttf", ".otf", ".ttc", ".otc"}

var languagePresets = []struct {
	key       string
	languages []string
}{
	{"chinese", []string{"sc", "tc"}}, {"all", gameLanguages}, {"none", nil},
}

// The stages the progress panel lists, in build order; scan and scope belong
// to preparing the source.
var progressStages = []string{"source", "weights", "merge", "outlines", "convert", "register", "atlas", "package"}

// The redesign's palette: a near-black ground with the game's yellow.
var (
	cYellow    = ui.RGB(255, 225, 26)
	cGround    = ui.RGB(10, 11, 12)
	cPanel     = ui.RGB(19, 20, 23)
	cPanelCur  = ui.RGB(21, 21, 15)
	cLine      = ui.RGB(38, 39, 44)
	cField     = ui.RGB(11, 12, 14)
	cFieldLine = ui.RGB(44, 45, 51)
	cChip      = ui.RGB(24, 25, 29)
	cText      = ui.RGB(237, 237, 230)
	cSoft      = ui.RGB(199, 200, 206)
	cMuted     = ui.RGB(154, 156, 163)
	cDim       = ui.RGB(110, 112, 119)
	cWarn      = ui.RGB(242, 199, 122)
	cWarnLine  = ui.RGB(242, 184, 75)
	cClear     = ui.RGBA(0, 0, 0, 0)
)

// numberFont draws step numbers and the progress percentage.
const numberFont = "Bahnschrift"

// applyTheme sets the fixed dark theme of the design.
func (a *application) applyTheme(c *ui.Context) {
	t := *ui.DarkTheme()
	t.Background, t.Surface, t.SurfaceHover, t.SurfacePressed = cGround, ui.RGB(28, 29, 33), ui.RGB(38, 39, 44), ui.RGB(44, 45, 51)
	t.Border, t.Text, t.TextMuted = ui.RGB(52, 53, 59), cText, cMuted
	t.Accent, t.AccentHover, t.AccentPressed, t.AccentText = cYellow, ui.RGB(255, 240, 122), ui.RGB(226, 199, 0), cGround
	t.Warning, t.Focus = cWarn, cYellow
	t.Radius = 0
	a.theme = &t
	c.SetTheme(a.theme)
}

func glyphFor(locale string) string {
	switch locale {
	case "zh-CN", "zh-TW", "ja":
		return "字"
	case "ko":
		return "글"
	case "ru":
		return "Ш"
	}
	return "A"
}

func (a *application) view(c *ui.Context) {
	a.drain()
	if a.gameFontFolder != "" && a.gameFontFolder != a.settings.GameFolder {
		a.gameFonts, a.gameFontFolder = nil, ""
		if a.gameFontID != "" {
			a.gameFontID = ""
			a.requestPreview()
		}
	}
	a.applyTheme(c)
	if a.notice != "" {
		c.Toast(a.t(a.notice))
		a.notice = ""
	}
	width, _ := c.Size()
	cols := 3
	if width < 1100 {
		cols = 2
	}
	if width < 760 {
		cols = 1
	}
	narrow := cols == 1
	pad := float32(28)
	if narrow {
		pad = 16
	}
	if a.busy || a.previewBusy || a.gamePreviewBusy || !a.dirtyAt.IsZero() || !a.previewDirtyAt.IsZero() {
		c.After(100 * time.Millisecond)
	}
	if !a.dirtyAt.IsZero() && time.Since(a.dirtyAt) > 350*time.Millisecond {
		a.save()
	}
	if !a.previewDirtyAt.IsZero() && time.Since(a.previewDirtyAt) > 400*time.Millisecond {
		a.requestPreview()
	}
	missing := a.missing()
	root := ui.Column(c.Key("app")).Fill().Font(uiFont(a.settings.Locale)).FontSize(14).Children(func() {
		a.header(c, narrow, pad)
		ui.Scroll(c.Key("form-scroll")).Grow(1).MinHeight(0).TrackScroll(&a.scroll).Padding(16, pad, 18, pad).Gap(14).Children(func() {
			if a.fontReady() {
				a.toolbar(c)
			}
			a.hero(c, width-2*pad, narrow)
			if a.fontReady() && !a.generating && !a.done {
				a.notes(c)
			}
			a.steps(c, cols)
		})
		haz := cYellow
		if len(missing) > 0 {
			haz = ui.RGB(58, 59, 65)
		}
		ui.Box(c.Key("hazard")).FillWidth().Height(4).Background(haz)
		a.footer(c, narrow, pad, missing)
	})
	a.dragOver = !a.busy && root.FileDragOver()
	if !a.busy {
		for _, path := range root.DroppedFiles() {
			if slices.Contains(fontExtensions, strings.ToLower(filepath.Ext(path))) {
				a.fontDraft = path
				a.inspectFont(path)
				break
			}
		}
	}
}

func (a *application) header(c *ui.Context, narrow bool, pad float32) {
	ui.Row(c.Key("header")).Height(58).AlignItems(ui.Center).Gap(14).Padding(0, pad).Children(func() {
		ui.Box(c).Size(30, 30).Shrink(0).Background(cYellow).Center().Children(func() {
			ui.Text(c, glyphFor(a.settings.Locale)).FontWeight(900).FontSize(15).TextColor(cGround)
		})
		ui.Text(c, a.t("app.title")).FontWeight(900).FontSize(16).SingleLine().Shrink(1).MinWidth(0)
		if !narrow {
			ui.Text(c, Version).FontSize(12).TextColor(cMuted).SingleLine()
		}
		ui.Spacer(c)
		var names []string
		for _, locale := range Locales {
			names = append(names, locale.Name)
		}
		if ui.Select(c.Key("locale"), &a.localeName, names).Width(160).Shrink(0).Label(a.t("ui.language")).Tooltip(a.t("ui.language")).Changed() {
			for _, locale := range Locales {
				if locale.Name == a.localeName {
					oldSample, oldAuto := a.t("preview.sample"), a.autoModName()
					a.settings.Locale = locale.Code
					if a.settings.PreviewText == oldSample {
						a.settings.PreviewText = a.t("preview.sample")
						a.sampleDraft = flatSample(a.settings.PreviewText)
						a.requestPreview()
					}
					if a.settings.ModName == oldAuto && a.sourceFace.Path != "" {
						a.settings.ModName, a.lastAutoMod = a.autoModName(), a.autoModName()
					}
					if a.window != nil {
						a.window.SetTitle(a.t("app.title") + " " + Version)
					}
					a.changed(false)
				}
			}
		}
	})
	ui.Box(c).FillWidth().Height(1).Background(ui.RGB(31, 32, 36))
	if !a.canGenerate {
		ui.Text(c, a.native(0)).FontSize(12).TextColor(cWarn).Padding(6, pad)
	}
	if a.demo {
		ui.Text(c, a.native(3)).FontSize(12).Bold().TextColor(cWarn).Padding(6, pad)
	}
}

func iconButton(c *ui.Context, key, label, icon string, hot bool) ui.Element {
	var b ui.Element
	if hot {
		b = ui.PrimaryButton(c.Key(key), "")
	} else {
		b = ui.Button(c.Key(key), "")
	}
	return b.Size(38, 38).Shrink(0).Padding(9).Label(label).Tooltip(label).Children(func() { ui.Icon(c, icons[icon]).Size(18, 18) })
}

// link draws a text button in the accent color, for secondary actions.
func link(c *ui.Context, key, label string) ui.Element {
	return ui.Button(c.Key(key), label).Background(cClear).Border(0, cClear).Padding(2, 0).TextColor(cYellow).FontSize(12)
}

func muted(c *ui.Context, text string, size float32) ui.Element {
	return ui.Text(c, text).FontSize(size).TextColor(cMuted)
}

func (a *application) toolbar(c *ui.Context) {
	ui.Row(c.Key("tools")).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
		if _, supported := a.backend.(GameFontBackend); supported {
			if len(a.gameFonts) == 0 {
				ui.Button(c.Key("read-game-fonts"), "").Label(a.t("preview.load_game")).Height(34).Padding(0, 12).Gap(8).Disabled(a.busy || a.settings.GameFolder == "").Children(func() {
					ui.Icon(c, icons["gamepad-2"]).Size(16, 16)
					ui.Text(c, a.t("preview.load_game")).FontSize(12).SingleLine()
				}).OnClick(a.loadGameFonts)
			} else {
				// The custom font alone, or one original game font to compare with.
				selected := a.t("preview.custom")
				names := []string{selected}
				for _, face := range a.gameFonts {
					names = append(names, face.Name)
					if face.ID == a.gameFontID {
						selected = face.Name
					}
				}
				if ui.Select(c.Key("preview-font"), &selected, names).Width(220).Shrink(1).MinWidth(0).Label(a.t("preview.font")).Disabled(a.busy).Changed() {
					id := ""
					for _, face := range a.gameFonts {
						if face.Name == selected {
							id = face.ID
						}
					}
					a.selectGamePreview(id)
				}
			}
		}
		compare := ui.Toggle(c.Key("compare"), &a.compare, "").Label(a.t("preview.compare")).Height(34).Padding(0, 12).Gap(8).Children(func() {
			ui.Icon(c, icons["columns-2"]).Size(14, 14)
			ui.Text(c, a.t("preview.compare")).FontSize(12).SingleLine()
		})
		if compare.Changed() {
			if a.compare && len(a.gameFonts) == 0 && a.settings.GameFolder != "" {
				a.loadGameFonts()
			}
			a.requestPreview()
		}
		if a.settings.MatchWeights {
			index := slices.Index(weightTargets, a.previewWeight)
			if ui.Segmented(c.Key("preview-weight"), &index, a.t("weight.regular"), a.t("weight.medium"), a.t("weight.bold")).Height(34).Changed() && index >= 0 {
				a.previewWeight = weightTargets[index]
				a.requestPreview()
			}
		}
		if ui.TextInput(c.Key("sample"), &a.sampleDraft).Label(a.t("preview.title")).Height(34).Grow(1).MinWidth(200).Disabled(a.busy).Changed() {
			a.settings.PreviewText = sampleText(a.sampleDraft)
			a.changed(false)
			a.previewDirtyAt = time.Now()
		}
		iconButton(c, "refresh-preview", a.t("preview.update"), "refresh-cw", false).Size(34, 34).Disabled(a.busy).OnClick(a.requestPreview)
	})
}

func (a *application) hero(c *ui.Context, guessWidth float32, narrow bool) {
	h := float32(280)
	if narrow {
		h = 230
	}
	border := cLine
	if a.dragOver {
		border = cYellow
	}
	box := ui.Box(c.Key("hero")).FillWidth().Height(h).Background(ui.RGB(13, 14, 16)).Border(1, border).Clip().Children(func() {
		switch {
		case a.generating:
			a.heroProgress(c, narrow)
		case a.done && a.settings.LastOutput != "":
			a.heroDone(c, narrow)
		case a.dragOver || (!a.fontReady() && a.gamePreview == nil):
			a.heroEmpty(c, narrow)
		default:
			w := a.heroWidth
			if w <= 0 {
				w = guessWidth
			}
			a.heroPreview(c, w-2, h-2)
		}
	})
	if w := box.LayoutBounds().W; w > 0 && w != a.heroWidth {
		a.heroWidth = w
		c.After(16 * time.Millisecond)
	}
}

func (a *application) previewImage(c *ui.Context, bitmap *ui.Bitmap, busy bool) {
	switch {
	case bitmap != nil:
		ui.Image(c, bitmap).Fill().Fit(ui.Contain).Label(a.t("preview.title"))
	case busy:
		ui.Row(c).Fill().Center().Gap(8).Children(func() {
			ui.Spinner(c)
			muted(c, a.t("preview.working"), 13)
		})
	}
}

func (a *application) heroPreview(c *ui.Context, w, h float32) {
	gameName := ""
	for _, face := range a.gameFonts {
		if face.ID == a.gameFontID {
			gameName = face.Name
		}
	}
	if !a.compare || !a.fontReady() {
		if a.fontReady() {
			a.previewImage(c, a.preview, a.previewBusy)
		} else {
			a.previewImage(c, a.gamePreview, a.gamePreviewBusy)
		}
		return
	}
	// Both images span the whole hero, anchored to its outer edges, so the
	// divider reveals one drawing of the sample in each font.
	ui.SplitPercent(c.Key("compare-split"), &a.split, func() {
		ui.Box(c.Key("compare-game")).Fill().Clip().Children(func() {
			if strings.TrimSpace(a.settings.GameFolder) == "" {
				ui.Column(c).Fill().Padding(0, 24).Gap(10).Justify(ui.Center).Children(func() {
					ui.Text(c, a.t("preview.game_missing")).FontSize(14).TextColor(cSoft)
					link(c, "choose-game-hero", a.t("dialog.game")).OnClick(a.chooseGame)
				})
				return
			}
			ui.Box(c).Absolute().Left(0).Top(0).Size(w, h).Children(func() { a.previewImage(c, a.gamePreview, a.gamePreviewBusy || a.busy) })
		})
	}, func() {
		ui.Box(c.Key("compare-custom")).Fill().Clip().Children(func() {
			ui.Box(c).Absolute().Right(0).Top(0).Size(w, h).Children(func() { a.previewImage(c, a.preview, a.previewBusy) })
		})
	}).Fill()
	if gameName != "" {
		ui.Text(c, gameName).Absolute().Top(12).Left(16).FontSize(11).TextColor(cMuted).SingleLine()
	}
	ui.Text(c, a.sourceFace.Style.Family).Absolute().Top(12).Right(16).FontSize(11).TextColor(cYellow).SingleLine()
}

func (a *application) heroEmpty(c *ui.Context, narrow bool) {
	ui.Row(c).Fill().Center().Gap(48).Padding(24, 32).Children(func() {
		if !narrow {
			ui.Text(c, glyphFor(a.settings.Locale)).FontSize(140).FontWeight(900).TextColor(ui.RGB(42, 43, 48))
		}
		ui.Column(c).MaxWidth(460).MinWidth(0).Gap(12).Children(func() {
			if a.dragOver {
				ui.Text(c, a.t("font.drop_here")).FontSize(22).FontWeight(900).TextColor(cYellow)
				return
			}
			ui.Text(c, a.t("preview.empty")).FontSize(20).FontWeight(700).TextColor(cSoft)
			muted(c, a.t("font.drop"), 13)
			ui.Row(c).Children(func() {
				ui.Button(c.Key("hero-add-font"), "").Label(a.t("font.add")).Height(44).Padding(0, 18).Gap(8).BorderColor(cText).Disabled(a.busy).Children(func() {
					ui.Icon(c, icons["file-type"]).Size(17, 17)
					ui.Text(c, a.t("font.add")).FontWeight(700)
				}).OnClick(a.chooseFont)
			})
		})
	})
}

func (a *application) heroProgress(c *ui.Context, narrow bool) {
	stage := a.progress.Stage
	if stage == "scan" || stage == "scope" {
		stage = "source"
	}
	current := slices.Index(progressStages, stage)
	if stage == "cache" || stage == "done" {
		current = len(progressStages)
	}
	cols := 2
	if narrow {
		cols = 1
	}
	ui.Grid(c).Columns(cols).Fill().Gap(28).Padding(28, 40).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(10).MinWidth(0).Children(func() {
			pct := "—"
			if a.progress.Fraction >= 0 {
				pct = fmt.Sprintf("%d", int(a.progress.Fraction*100+0.5))
			}
			ui.Row(c).AlignItems(ui.Baseline).Gap(4).Children(func() {
				size := float32(110)
				if narrow {
					size = 64
				}
				ui.Text(c, pct).Font(numberFont).FontWeight(800).FontSize(size).TextColor(cYellow)
				ui.Text(c, "%").Font(numberFont).FontWeight(800).FontSize(size * .45).TextColor(cText)
			})
			ui.Text(c, a.stageText()).FontSize(15).FontWeight(700).MaxLines(2)
			ui.Progress(c, a.progress.Fraction).Height(8).Label(a.stageText())
			muted(c, time.Since(a.started).Round(time.Second).String(), 12)
		})
		if !narrow {
			ui.Column(c).Gap(2).MinWidth(0).Children(func() {
				for i, s := range progressStages {
					color, weight := cMuted, 400
					switch {
					case i < current:
						color = cSoft
					case i == current:
						color, weight = cYellow, 700
					}
					ui.Row(c).AlignItems(ui.Center).Gap(10).MinHeight(24).Children(func() {
						mark := ui.Box(c).Size(8, 8).Shrink(0).Border(1.5, color)
						if i <= current {
							mark.Background(color)
						}
						ui.Text(c, a.t("stage."+s)).FontSize(12).FontWeight(weight).TextColor(color).MaxLines(2)
					})
				}
			})
		}
	})
}

func (a *application) heroDone(c *ui.Context, narrow bool) {
	ui.Row(c).Fill().AlignItems(ui.Center).Gap(32).Padding(24, 40).Children(func() {
		size := float32(88)
		if narrow {
			size = 56
		}
		ui.Box(c).Size(size, size).Shrink(0).Background(cYellow).Center().Children(func() {
			ui.Icon(c, icons["check"]).Size(size*.45, size*.45).TextColor(cGround)
		})
		ui.Column(c).Gap(10).MinWidth(0).MaxWidth(640).Children(func() {
			ui.Text(c, a.t("status.complete")).FontSize(20).FontWeight(900).MaxLines(3)
			ui.Text(c, a.settings.LastOutput).FontSize(12).TextColor(cSoft).SingleLine().Selectable().Padding(10, 12).Background(cField).Border(1, cFieldLine)
			muted(c, fmt.Sprintf("%s · %.2f GB · %s", a.settings.ModName, float64(a.lastBytes)/(1<<30), a.lastElapsed.Round(time.Second)), 12).SingleLine()
			ui.Row(c).Children(func() {
				ui.Button(c.Key("done-open"), "").Label(a.t("output.open")).Height(44).Padding(0, 18).Gap(8).BorderColor(cText).Children(func() {
					ui.Icon(c, icons["folder-open"]).Size(17, 17)
					ui.Text(c, a.t("output.open")).FontWeight(700)
				}).OnClick(a.openOutput)
			})
		})
	})
}

func (a *application) notes(c *ui.Context) {
	ui.Row(c.Key("notes")).Wrap().Gap(24).Children(func() {
		if a.previewMissing > 0 {
			ui.Row(c).Gap(8).Grow(1).MinWidth(0).Children(func() {
				ui.Box(c).Size(7, 7).Shrink(0).Background(cWarnLine).Margin(6, 0, 0, 0)
				ui.Text(c, a.t("preview.missing")).FontSize(12).TextColor(cWarn)
			})
		}
		if !a.canGenerate {
			muted(c, a.t("weight.original"), 12)
		}
		muted(c, a.t("preview.ready"), 12).Grow(1).MinWidth(0)
	})
}

// missing lists the section titles of the steps generation still needs.
func (a *application) missing() []string {
	var out []string
	if strings.TrimSpace(a.settings.GameFolder) == "" {
		out = append(out, a.t("game.path"))
	}
	if !a.fontReady() {
		out = append(out, a.t("section.font"))
	}
	if len(a.settings.Languages) == 0 {
		out = append(out, a.t("section.languages"))
	}
	if !strings.EqualFold(filepath.Ext(a.settings.OutputZIP), ".zip") {
		out = append(out, a.t("section.output"))
	}
	return out
}

func (a *application) fontReady() bool {
	return len(a.faces) > 0 && a.fontDraft == a.settings.Font.Path
}

type stepState int

const (
	stepTodo stepState = iota
	stepDefault
	stepDone
)

func (a *application) panel(c *ui.Context, key string, current bool, fn func()) ui.Element {
	fill, border := cPanel, cLine
	if current {
		fill, border = cPanelCur, cYellow
	}
	return ui.Column(c.Key(key)).Background(fill).Border(1, border).Padding(16, 18, 18, 18).Gap(18).MinWidth(0).Children(fn)
}

func stepHead(c *ui.Context, number, title string, state stepState) {
	ui.Row(c).AlignItems(ui.Center).Gap(10).MinWidth(0).Children(func() {
		color := cYellow
		if state == stepTodo {
			color = ui.RGB(90, 92, 99)
		}
		ui.Text(c, number).Font(numberFont).FontWeight(800).FontSize(24).TextColor(color).Width(30).Shrink(0)
		ui.Text(c, title).FontWeight(900).FontSize(15).Grow(1).MinWidth(0)
		switch state {
		case stepDone:
			ui.Icon(c, icons["check"]).Size(16, 16).TextColor(cYellow).Label("✓")
		case stepDefault:
			ui.Icon(c, icons["check"]).Size(16, 16).TextColor(cMuted).Label("✓")
		}
	})
}

func rule(c *ui.Context) { ui.Box(c).FillWidth().Height(1).Background(ui.RGB(36, 37, 42)) }

func (a *application) steps(c *ui.Context, cols int) {
	grid := ui.Grid(c.Key("steps")).Columns(cols).Gap(16).Children(func() {
		gameSet := strings.TrimSpace(a.settings.GameFolder) != ""
		a.panel(c, "panel-source", !gameSet || !a.fontReady(), func() {
			state := stepTodo
			if gameSet {
				state = stepDone
			}
			ui.Column(c.Key("game-section")).Gap(10).Children(func() {
				stepHead(c, "01", a.t("game.path"), state)
				a.pathField(c, "game", a.t("game.path"), &a.settings.GameFolder, "folder-open", a.chooseGame, !gameSet)
			})
			rule(c)
			a.fontStep(c)
		})
		a.panel(c, "panel-groups", false, func() { a.groupStep(c) })
		last := a.panel(c, "panel-output", false, func() {
			if cols == 2 {
				ui.Grid(c).Columns(2).Gap(28).Children(func() {
					ui.Column(c).Gap(10).MinWidth(0).Children(func() { a.styleStep(c) })
					ui.Column(c).Gap(10).MinWidth(0).Children(func() { a.outputStep(c) })
				})
				return
			}
			ui.Column(c).Gap(10).Children(func() { a.styleStep(c) })
			rule(c)
			ui.Column(c).Gap(10).Children(func() { a.outputStep(c) })
		})
		if cols == 2 {
			last.ColumnSpan(-1)
		}
	})
	if a.generating {
		grid.Opacity(.42)
	}
}

func (a *application) pathField(c *ui.Context, key, label string, value *string, icon string, action func(), hot bool) {
	ui.Row(c.Key(key + "-field")).Gap(6).Children(func() {
		if ui.TextInput(c.Key(key), value).Label(label).Grow(1).MinWidth(0).Height(38).Disabled(a.busy).Changed() {
			a.changed(false)
		}
		iconButton(c, key+"-browse", a.t("browse"), icon, hot).Disabled(a.busy).OnClick(action)
	})
}

func (a *application) fontStep(c *ui.Context) {
	ui.Column(c.Key("font-section")).Gap(10).Children(func() {
		state := stepTodo
		if a.fontReady() {
			state = stepDone
		}
		stepHead(c, "02", a.t("section.font"), state)
		if !a.fontReady() {
			ui.Row(c).Gap(6).Children(func() {
				if ui.TextInput(c.Key("font-path"), &a.fontDraft).Label(a.t("file.fonts")).Placeholder(a.t("font.supported")).Height(38).Grow(1).MinWidth(0).Disabled(a.busy).Submitted() {
					a.inspectFont(a.fontDraft)
				}
				iconButton(c, "choose-font", a.t("font.add"), "plus", false).Disabled(a.busy).OnClick(a.chooseFont)
			})
			muted(c, a.t("font.drop"), 12)
			return
		}
		ui.Row(c.Key("font-card")).AlignItems(ui.Center).Gap(12).Padding(8).Background(cField).Border(1, cFieldLine).MinWidth(0).Children(func() {
			ui.Box(c).Size(46, 46).Shrink(0).Background(cYellow).Center().Children(func() {
				ui.Text(c, glyphFor(a.settings.Locale)).FontSize(24).FontWeight(700).TextColor(cGround)
			})
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				name := a.sourceFace.Style.Family
				if name == "" {
					name = a.sourceFace.Name
				}
				ui.Text(c, name).FontSize(14).FontWeight(700).SingleLine()
				muted(c, a.familyLine(), 12).SingleLine()
			})
			iconButton(c, "choose-font", a.t("font.add"), "file-type", false).Disabled(a.busy).OnClick(a.chooseFont)
			iconButton(c, "load-font", a.t("font.reload"), "rotate-ccw", false).Disabled(a.busy).OnClick(func() { a.inspectFont(a.fontDraft) })
		})
		if a.hasOtherFaces() {
			faceNames := make([]string, len(a.faces))
			for i, face := range a.faces {
				faceNames[i] = faceLabel(face)
			}
			if ui.Select(c.Key("face"), &a.faceName, faceNames).FillWidth().Label(a.t("dialog.face")).Disabled(a.busy).Changed() {
				for _, face := range a.faces {
					if faceLabel(face) == a.faceName {
						a.selectFace(face)
					}
				}
			}
		}
		a.axesView(c)
		if a.settings.MatchWeights {
			a.weightSources(c)
		}
	})
}

func (a *application) weightSources(c *ui.Context) {
	rows := a.weightRows()
	members := a.members()
	ui.Column(c.Key("weight-sources")).Gap(4).MinWidth(0).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(10).Children(func() {
			muted(c, a.t("weight.source"), 12).Grow(1).MinWidth(0)
			if len(rows) > 0 && rows[0].Kind != "variable" {
				if link(c, "weight-manual", a.t("weight.manual")).Disabled(a.busy).Clicked() {
					a.manualWeights = !a.manualWeights
				}
			}
		})
		labels := map[int]string{400: a.t("weight.regular"), 500: a.t("weight.medium"), 700: a.t("weight.bold")}
		for _, row := range rows {
			ui.Row(c.Key(fmt.Sprint("weight-row-", row.Target))).AlignItems(ui.Center).Gap(8).MinHeight(32).Padding(0, 10).Background(cField).Border(1, ui.RGB(36, 37, 42)).MinWidth(0).Children(func() {
				ui.Text(c, labels[row.Target]).FontWeight(700).FontSize(13).SingleLine()
				ui.Text(c, "←").FontSize(13).TextColor(cDim)
				if a.manualWeights && row.Kind != "variable" {
					options := append([]string{a.t("weight.auto")}, a.memberLabels()...)
					options = append(options, a.t("weight.synthetic"))
					selected := options[0]
					if pin, ok := a.settings.Font.WeightFaces[row.Target]; ok {
						if pin == -1 {
							selected = options[len(options)-1]
						} else if pin >= 0 && pin < len(members) {
							selected = options[1+pin]
						}
					}
					if ui.Select(c.Key(fmt.Sprint("weight-pin-", row.Target)), &selected, options).Grow(1).MinWidth(0).Height(30).Label(labels[row.Target]).Disabled(a.busy).Changed() {
						a.pinWeight(row.Target, slices.Index(options, selected), len(options))
					}
					return
				}
				ui.Text(c, row.Source).FontSize(12).TextColor(ui.RGB(217, 217, 210)).SingleLine().Grow(1).MinWidth(0)
				border, ink := ui.RGB(74, 75, 82), cSoft
				if row.Kind == "synthetic" {
					border, ink = cWarnLine, cWarn
				}
				ui.Text(c, row.Badge).FontSize(11).TextColor(ink).Padding(3, 6).Border(1, border).SingleLine().Shrink(0)
			})
		}
	})
}

// pinWeight records a manual choice for a weight row: option 0 is automatic,
// the last is synthetic emphasis, and the rest index the family members.
func (a *application) pinWeight(target, option, count int) {
	if option == 0 {
		delete(a.settings.Font.WeightFaces, target)
	} else {
		if a.settings.Font.WeightFaces == nil {
			a.settings.Font.WeightFaces = map[int]int{}
		}
		if option == count-1 {
			a.settings.Font.WeightFaces[target] = -1
		} else {
			a.settings.Font.WeightFaces[target] = option - 1
		}
	}
	a.changed(true)
}

func (a *application) groupStep(c *ui.Context) {
	ui.Column(c.Key("languages-section")).Gap(10).Children(func() {
		state := stepDefault
		if len(a.settings.Languages) == 0 {
			state = stepTodo
		}
		stepHead(c, "03", a.t("section.languages"), state)
		ui.Grid(c).Columns(2).Gap(6).Children(func() {
			for _, lang := range gameLanguages {
				selected := slices.Contains(a.settings.Languages, lang)
				fill, line, ink, weight := cChip, cFieldLine, cSoft, 400
				if selected {
					fill, line, ink, weight = cYellow, cYellow, cGround, 700
				}
				toggle := ui.ToggleBase(c.Key(lang), &selected).MinHeight(44).Padding(6, 12).Gap(10).AlignItems(ui.Center).Background(fill).Border(1, line).MinWidth(0).Label(a.t("group." + lang)).Disabled(a.busy).Children(func() {
					ui.Box(c).Size(14, 14).Shrink(0).Border(1.5, ink).Center().Children(func() {
						if selected {
							ui.Icon(c, icons["check"]).Size(10, 10).TextColor(ink)
						}
					})
					ui.Text(c, a.t("group."+lang)).FontSize(13).FontWeight(weight).TextColor(ink).MinWidth(0)
				})
				if toggle.Changed() {
					if selected {
						a.settings.Languages = append(a.settings.Languages, lang)
					} else {
						a.settings.Languages = slices.DeleteFunc(a.settings.Languages, func(s string) bool { return s == lang })
					}
					a.changed(false)
				}
			}
		})
		ui.Row(c).Wrap().Gap(16).Children(func() {
			for _, preset := range languagePresets {
				if link(c, "preset-"+preset.key, a.t("preset."+preset.key)).Disabled(a.busy).Clicked() {
					a.settings.Languages = slices.Clone(preset.languages)
					a.changed(false)
				}
			}
		})
		rule(c)
		a.switchRow(c, "symbols", &a.settings.IncludeSymbols, a.t("symbols.toggle"), "", false)
		var groups []string
		for _, lang := range gameLanguages {
			if slices.Contains(a.settings.Languages, lang) {
				groups = append(groups, a.t("group."+lang))
			}
		}
		if len(groups) > 0 {
			muted(c, a.t("scope.summary", "groups", a.list(groups)), 12)
		} else {
			ui.Text(c, a.t("error.no_language")).FontSize(12).TextColor(cWarn)
		}
		muted(c, a.t("scope.mainGroups"), 11)
	})
}

func (a *application) styleStep(c *ui.Context) {
	stepHead(c, "04", a.t("section.style"), stepDefault)
	note := func(catalog string, on bool) string {
		if on {
			return a.t(catalog + ".on")
		}
		return a.t(catalog + ".off")
	}
	a.switchRow(c, "weight", &a.settings.MatchWeights, a.t("weight.toggle"), note("weight", a.settings.MatchWeights), true)
	a.switchRow(c, "outlines", &a.settings.ProcessOutlines, a.t("outline.toggle"), note("outline", a.settings.ProcessOutlines), true)
}

func (a *application) outputStep(c *ui.Context) {
	state := stepDefault
	if !strings.EqualFold(filepath.Ext(a.settings.OutputZIP), ".zip") {
		state = stepTodo
	}
	stepHead(c, "05", a.t("section.output"), state)
	ui.Column(c.Key("mod-name-field")).Gap(6).Children(func() {
		ui.Text(c, a.t("output.mod_name")).FontSize(13).FontWeight(700)
		if ui.TextInput(c.Key("mod-name"), &a.settings.ModName).Label(a.t("output.mod_name")).Height(38).FillWidth().MinWidth(0).Disabled(a.busy).Changed() {
			a.changed(false)
		}
		muted(c, a.t("output.mod_hint"), 12)
	})
	a.pathField(c, "output", a.t("dialog.export_zip"), &a.settings.OutputZIP, "folder", a.chooseOutput, false)
	muted(c, a.t("output.install"), 12)
}

// switchRow is a whole-row toggle drawn as a switch, with an optional note.
func (a *application) switchRow(c *ui.Context, key string, on *bool, title, note string, preview bool) {
	fill, line, knob, left := ui.RGB(28, 29, 33), ui.RGB(58, 59, 65), cDim, float32(3)
	if *on {
		fill, line, knob, left = cYellow, cYellow, cGround, 21
	}
	row := ui.ToggleBase(c.Key(key), on).MinHeight(44).Gap(14).AlignItems(ui.Center).Label(title).Disabled(a.busy).Children(func() {
		ui.Box(c).Size(40, 22).Shrink(0).Background(fill).Border(1, line).Children(func() {
			ui.Box(c).Absolute().Top(3).Left(left).Size(14, 14).Background(knob)
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, title).FontSize(14).FontWeight(500)
			if note != "" {
				muted(c, note, 12)
			}
		})
	})
	if row.Changed() {
		a.changed(preview)
	}
}

func (a *application) stageText() string {
	stageKey := "stage." + a.progress.Stage
	if catalogs["en"][stageKey] == "" {
		stageKey = "stage.working"
	}
	stage := a.t(stageKey)
	if a.progress.Total > 0 {
		stage = a.t("progress.count", "stage", stage, "done", fmt.Sprint(a.progress.Completed), "total", fmt.Sprint(a.progress.Total))
	}
	return stage
}

func (a *application) footer(c *ui.Context, narrow bool, pad float32, missing []string) {
	ui.Row(c.Key("footer")).Wrap().AlignItems(ui.Center).Gap(12).Padding(14, pad).Background(cPanel).Children(func() {
		s1, s2, color := a.t("scope.summary", "groups", a.groupNames()), a.t("output.install"), cText
		switch {
		case a.busy && a.cancelling:
			s1, s2 = a.native(2), ""
		case a.busy:
			s1 = a.t("progress.elapsed", "time", time.Since(a.started).Round(time.Second).String(), "stage", a.stageText())
		case a.errorKey != "":
			s1, s2, color = a.t(a.errorKey), a.errorDetail, c.Theme().Danger
		case len(missing) > 0 && a.canGenerate:
			s1, s2, color = a.t("ready.missing", "items", a.list(missing)), "", cWarn
		case a.statusKey == "native.cancelled":
			s1 = a.native(1)
		case a.done:
			s1, s2 = a.t("status.complete"), a.settings.ModName
		}
		ui.Column(c.Key("status")).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, s1).FontSize(14).FontWeight(700).MaxLines(2).TextColor(color).Selectable().Role(ui.RoleStatus)
			if s2 != "" {
				muted(c, s2, 12).MaxLines(2).Selectable()
			}
		})
		actions := ui.Row(c.Key("actions")).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
			if a.busy {
				ui.Button(c.Key("cancel"), "").Label(a.t("cancel")).Height(46).Padding(0, 18).Gap(8).Disabled(a.cancelling).Children(func() {
					ui.Icon(c, icons["x"]).Size(17, 17)
					ui.Text(c, a.t("cancel"))
				}).OnClick(a.cancelWork)
			}
			open := ui.Button(c.Key("open-output"), "").Label(a.t("output.open")).Height(46).Padding(0, 18).Gap(8).Disabled(a.busy || a.settings.LastOutput == "").Children(func() {
				ui.Icon(c, icons["folder-open"]).Size(17, 17)
				ui.Text(c, a.t("output.open")).SingleLine()
			}).OnClick(a.openOutput)
			if a.settings.LastOutput != "" {
				open.Tooltip(a.settings.LastOutput)
			}
			cta := ui.PrimaryButton(c.Key("generate"), "").Label(a.t("build")).Height(52).Padding(0, 34, 0, 24).Gap(12).Disabled(a.busy || !a.canGenerate || len(missing) > 0).Children(func() {
				ui.Icon(c, icons["archive"]).Size(19, 19)
				ui.Text(c, a.t("build")).FontSize(16).FontWeight(900).SingleLine()
			}).OnClick(a.generate)
			if narrow {
				cta.FillWidth()
			}
		})
		if narrow {
			actions.FillWidth()
		}
	})
}

func (a *application) groupNames() string {
	var groups []string
	for _, lang := range gameLanguages {
		if slices.Contains(a.settings.Languages, lang) {
			groups = append(groups, a.t("group."+lang))
		}
	}
	return a.list(groups)
}

// list joins items with the separator the interface language uses.
func (a *application) list(items []string) string {
	switch a.settings.Locale {
	case "zh-CN", "zh-TW", "ja":
		return strings.Join(items, "、")
	}
	return strings.Join(items, ", ")
}
