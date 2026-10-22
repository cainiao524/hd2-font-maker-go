package mygoapp

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
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
	{"sc", []string{"sc"}}, {"tc", []string{"tc"}}, {"latin", []string{"latin"}}, {"chinese", []string{"sc", "tc"}},
	{"all", gameLanguages}, {"none", nil},
}

// Preview images are drawn on this paper color in both appearances.
var previewPaper = ui.RGB(246, 247, 249)

// applyTheme follows the system's light or dark appearance with the app's accent.
func (a *application) applyTheme(c *ui.Context) {
	t := *c.Theme()
	if t.Dark {
		t.Accent, t.AccentHover, t.AccentPressed = ui.RGB(38, 140, 114), ui.RGB(52, 160, 132), ui.RGB(28, 116, 94)
	} else {
		t.Accent, t.AccentHover, t.AccentPressed = ui.RGB(25, 112, 91), ui.RGB(20, 96, 78), ui.RGB(16, 82, 66)
		t.Background = ui.RGB(250, 251, 252)
		t.Warning = ui.RGB(145, 83, 15)
	}
	t.Radius = 5
	a.theme = &t
	c.SetTheme(a.theme)
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
	compact := width < 960
	if a.busy || a.previewBusy || !a.dirtyAt.IsZero() || !a.previewDirtyAt.IsZero() {
		c.After(100 * time.Millisecond)
	}
	if !a.dirtyAt.IsZero() && time.Since(a.dirtyAt) > 350*time.Millisecond {
		a.save()
	}
	if !a.previewDirtyAt.IsZero() && time.Since(a.previewDirtyAt) > 400*time.Millisecond {
		a.requestPreview()
	}
	root := ui.Column(c.Key("app")).Fill().Font(uiFont(a.settings.Locale)).FontSize(15).Children(func() {
		ui.Column(c.Key("header")).Padding(14, 20).Gap(8).Children(func() {
			ui.Row(c).Wrap().AlignItems(ui.Center).Gap(12).Children(func() {
				ui.Row(c).Grow(1).Wrap().AlignItems(ui.Baseline).Gap(10).Children(func() {
					ui.Text(c, a.t("app.title")).FontSize(22).Bold().SingleLine()
					ui.Text(c, Version).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
				})
				var names []string
				for _, locale := range Locales {
					names = append(names, locale.Name)
				}
				if ui.Select(c.Key("locale"), &a.localeName, names).Width(220).Label(a.t("ui.language")).Tooltip(a.t("ui.language")).Changed() {
					for _, locale := range Locales {
						if locale.Name == a.localeName {
							oldSample := a.t("preview.sample")
							a.settings.Locale = locale.Code
							if a.settings.PreviewText == oldSample {
								a.settings.PreviewText = a.t("preview.sample")
								a.requestPreview()
							}
							if a.window != nil {
								a.window.SetTitle(a.t("app.title") + " " + Version)
							}
							a.changed(false)
						}
					}
				}
			})
			if !a.canGenerate {
				ui.Text(c, a.native(0)).TextColor(c.Theme().Warning)
			}
			if a.demo {
				ui.Text(c, a.native(3)).Bold().TextColor(c.Theme().Warning)
			}
		})
		ui.Divider(c)
		ui.Scroll(c.Key("form-scroll")).Grow(1).MinHeight(0).TrackScroll(&a.scroll).Padding(18, 20).Gap(20).Children(func() {
			if compact {
				ui.Column(c.Key("configuration")).Gap(24).Children(func() { a.configuration(c) })
				ui.Divider(c)
				a.previewView(c)
			} else {
				ui.Row(c.Key("workspace")).AlignItems(ui.Start).Gap(28).Children(func() {
					ui.Column(c.Key("configuration")).Width(460).Shrink(0).Gap(24).Children(func() { a.configuration(c) })
					ui.Column(c.Key("preview-column")).Grow(1).MinWidth(0).Gap(12).Children(func() { a.previewView(c) })
				})
			}
		})
		ui.Divider(c)
		a.footer(c, compact)
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

// section draws a numbered step whose number turns to the accent once done.
func (a *application) section(c *ui.Context, key string, step int, title string, done bool, fn func()) {
	ui.Column(c.Key(key+"-section")).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(10).Children(func() {
			fill, ink := c.Theme().Surface, c.Theme().TextMuted
			if done {
				fill, ink = c.Theme().Accent, c.Theme().AccentText
			}
			ui.Box(c).Size(24, 24).Shrink(0).Radius(12).Background(fill).Border(1, fill.Mix(c.Theme().Border, 0.5)).Center().Children(func() {
				ui.Text(c, strconv.Itoa(step)).FontSize(13).Bold().TextColor(ink)
			})
			ui.Text(c, title).Bold().FontSize(17).Grow(1).MinWidth(0)
		})
		fn()
	})
}

func (a *application) pathField(c *ui.Context, key, label string, value *string, icon string, action func()) {
	ui.Row(c.Key(key + "-field")).Gap(8).Children(func() {
		if ui.TextInput(c.Key(key), value).Label(label).Grow(1).MinWidth(0).Height(38).Disabled(a.busy).Changed() {
			a.changed(false)
		}
		iconButton(c, key+"-browse", a.t("browse"), a.t("browse"), icon).Disabled(a.busy).OnClick(action)
	})
}

func iconButton(c *ui.Context, key, label, tooltip, icon string) ui.Element {
	return ui.Button(c.Key(key), "").Size(38, 38).Shrink(0).Padding(8).Label(label).Tooltip(tooltip).Children(func() { ui.Icon(c, icons[icon]).Size(20, 20) })
}

func muted(c *ui.Context, text string, size float32) ui.Element {
	return ui.Text(c, text).FontSize(size).TextColor(c.Theme().TextMuted)
}

func (a *application) configuration(c *ui.Context) {
	a.section(c, "game", 1, a.t("game.path"), strings.TrimSpace(a.settings.GameFolder) != "", func() {
		a.pathField(c, "game", a.t("game.path"), &a.settings.GameFolder, "folder-open", a.chooseGame)
	})
	a.section(c, "font", 2, a.t("section.font"), a.fontReady(), func() {
		ui.Row(c).Gap(8).Children(func() {
			if ui.TextInput(c.Key("font-path"), &a.fontDraft).Label(a.t("file.fonts")).Placeholder(a.t("font.supported")).Height(38).Grow(1).MinWidth(0).Disabled(a.busy).Submitted() {
				a.inspectFont(a.fontDraft)
			}
			iconButton(c, "choose-font", a.t("font.add"), a.t("font.add"), "file-type").Disabled(a.busy).OnClick(a.chooseFont)
			iconButton(c, "load-font", a.t("font.reload"), a.t("font.reload"), "rotate-ccw").Disabled(a.busy || a.fontDraft == "").OnClick(func() { a.inspectFont(a.fontDraft) })
		})
		if len(a.faces) == 0 {
			muted(c, a.t("font.drop"), 13)
		}
		if len(a.faces) > 1 {
			faceNames := make([]string, len(a.faces))
			for i, face := range a.faces {
				faceNames[i] = faceLabel(face)
			}
			muted(c, a.t("dialog.face"), 13)
			if ui.Select(c.Key("face"), &a.faceName, faceNames).FillWidth().Label(a.t("dialog.face")).Disabled(a.busy).Changed() {
				for _, face := range a.faces {
					if faceLabel(face) == a.faceName {
						a.selectFace(face)
					}
				}
			}
		}
		a.axesView(c)
	})
	a.section(c, "languages", 3, a.t("section.languages"), len(a.settings.Languages) > 0, func() {
		ui.Row(c).Wrap().Gap(6).Children(func() {
			for _, preset := range languagePresets {
				on := sameGroups(a.settings.Languages, preset.languages)
				toggle := ui.Toggle(c.Key("preset-"+preset.key), &on, a.t("preset."+preset.key)).Padding(5, 12).Disabled(a.busy)
				if on {
					toggle.Background(c.Theme().Accent.Alpha(0.14)).BorderColor(c.Theme().Accent).TextColor(c.Theme().Accent)
				}
				if toggle.Changed() {
					a.settings.Languages = slices.Clone(preset.languages)
					a.changed(false)
				}
			}
		})
		ui.Column(c).Gap(7).Children(func() {
			for _, lang := range gameLanguages {
				selected := slices.Contains(a.settings.Languages, lang)
				if ui.Checkbox(c.Key(lang), &selected, a.t("group."+lang)).Disabled(a.busy).Changed() {
					if selected {
						a.settings.Languages = append(a.settings.Languages, lang)
					} else {
						a.settings.Languages = slices.DeleteFunc(a.settings.Languages, func(s string) bool { return s == lang })
					}
					a.changed(false)
				}
			}
		})
		if ui.Checkbox(c.Key("symbols"), &a.settings.IncludeSymbols, a.t("symbols.toggle")).Disabled(a.busy).Changed() {
			a.changed(false)
		}
		var groups []string
		for _, lang := range gameLanguages {
			if slices.Contains(a.settings.Languages, lang) {
				groups = append(groups, a.t("group."+lang))
			}
		}
		if len(groups) > 0 {
			muted(c, a.t("scope.summary", "groups", a.list(groups)), 13)
		} else {
			ui.Text(c, a.t("error.no_language")).FontSize(13).TextColor(c.Theme().Warning)
		}
		muted(c, a.t("scope.mainGroups"), 12)
	})
	a.section(c, "style", 4, a.t("section.style"), true, func() {
		a.optionWithNote(c, "weight", &a.settings.MatchWeights, "weight")
		a.optionWithNote(c, "outlines", &a.settings.ProcessOutlines, "outline")
	})
	a.section(c, "output", 5, a.t("section.output"), strings.EqualFold(filepath.Ext(a.settings.OutputZIP), ".zip"), func() {
		a.pathField(c, "output", a.t("dialog.export_zip"), &a.settings.OutputZIP, "folder", a.chooseOutput)
		muted(c, a.t("output.install"), 13)
	})
}

// optionWithNote shows a preview-affecting checkbox with the catalog's on/off explanation.
func (a *application) optionWithNote(c *ui.Context, key string, value *bool, catalog string) {
	ui.Column(c.Key(key + "-option")).Gap(4).Children(func() {
		if ui.Checkbox(c.Key(key), value, a.t(catalog+".toggle")).Disabled(a.busy).Changed() {
			a.changed(true)
		}
		note := catalog + ".off"
		if *value {
			note = catalog + ".on"
		}
		muted(c, a.t(note), 13).Padding(0, 0, 0, 28)
	})
}

func sameGroups(selected, preset []string) bool {
	if len(selected) != len(preset) {
		return false
	}
	for _, lang := range preset {
		if !slices.Contains(selected, lang) {
			return false
		}
	}
	return true
}

func (a *application) previewView(c *ui.Context) {
	ui.Column(c.Key("preview-section")).Gap(12).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, a.t("preview.title")).Bold().FontSize(17).Grow(1).MinWidth(0)
			iconButton(c, "refresh-preview", a.t("preview.update"), a.t("preview.update"), "refresh-cw").Disabled((len(a.faces) == 0 && a.gameFontID == "") || a.busy).OnClick(a.requestPreview)
		})
		if _, supported := a.backend.(GameFontBackend); supported {
			ui.Row(c).Gap(8).Children(func() {
				selected := a.t("preview.custom")
				names := []string{selected}
				for _, face := range a.gameFonts {
					names = append(names, face.Name)
					if face.ID == a.gameFontID {
						selected = face.Name
					}
				}
				if ui.Select(c.Key("preview-font"), &selected, names).Grow(1).MinWidth(0).Label(a.t("preview.font")).Disabled(a.busy).Changed() {
					id := ""
					for _, face := range a.gameFonts {
						if face.Name == selected {
							id = face.ID
							break
						}
					}
					a.selectGamePreview(id)
				}
				iconButton(c, "read-game-fonts", a.t("preview.load_game"), a.t("preview.load_game"), "gamepad-2").Disabled(a.busy || a.settings.GameFolder == "").OnClick(a.loadGameFonts)
			})
		}
		if ui.TextArea(c.Key("sample"), &a.settings.PreviewText).Label(a.t("preview.title")).Height(90).Disabled(a.busy).Changed() {
			a.changed(false)
			a.previewDirtyAt = time.Now()
		}
		border := c.Theme().Border
		if a.dragOver {
			border = c.Theme().Accent
		}
		ui.Box(c.Key("preview-image")).Height(260).Background(previewPaper).Border(1, border).Radius(4).Clip().Center().Children(func() {
			hint := ui.RGB(96, 104, 114)
			switch {
			case a.dragOver:
				ui.Column(c).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Icon(c, icons["file-type"]).Size(28, 28).TextColor(c.Theme().Accent)
					ui.Text(c, a.t("font.drop_here")).TextColor(hint)
				})
			case a.preview != nil:
				ui.Image(c, a.preview).Fill().Fit(ui.Contain).Label(a.t("preview.title"))
			case a.previewBusy:
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Spinner(c)
					ui.Text(c, a.t("preview.working")).TextColor(hint)
				})
			default:
				ui.Column(c).AlignItems(ui.Center).Padding(0, 24).Gap(8).Children(func() {
					ui.Icon(c, icons["file-type"]).Size(28, 28).TextColor(hint)
					ui.Text(c, a.t("preview.empty")).TextColor(hint)
					ui.Text(c, a.t("font.drop")).FontSize(13).TextColor(hint)
				})
			}
		})
		if !a.canGenerate && len(a.faces) > 0 {
			muted(c, a.t("weight.original"), 13)
		}
		if a.previewMissing > 0 && a.gameFontID == "" {
			muted(c, a.t("preview.missing"), 13)
		}
		muted(c, a.t("preview.initial"), 12)
	})
}

func (a *application) footer(c *ui.Context, compact bool) {
	missing := a.missing()
	ui.Column(c.Key("footer")).Padding(12, 20).Gap(10).Children(func() {
		status := a.t(a.statusKey)
		if a.statusKey == "native.cancelled" {
			status = a.native(1)
		}
		color := c.Theme().Text
		if a.busy {
			stageKey := "stage." + a.progress.Stage
			if catalogs["en"][stageKey] == "" {
				stageKey = "stage.working"
			}
			stage := a.t(stageKey)
			if a.progress.Total > 0 {
				stage = a.t("progress.count", "stage", stage, "done", fmt.Sprint(a.progress.Completed), "total", fmt.Sprint(a.progress.Total))
			}
			status = a.t("progress.elapsed", "time", time.Since(a.started).Round(time.Second).String(), "stage", stage)
			if a.cancelling {
				status = a.native(2)
			}
		} else if a.errorKey != "" {
			status, color = a.t(a.errorKey), c.Theme().Danger
		} else if len(missing) > 0 && a.canGenerate {
			status, color = a.t("ready.missing", "items", a.list(missing)), c.Theme().Warning
		}
		ui.Text(c, status).FontSize(13).MaxLines(2).TextColor(color).Selectable().Role(ui.RoleStatus)
		if a.errorKey != "" && a.errorDetail != "" && !a.busy {
			ui.Text(c, a.errorDetail).FontSize(12).MaxLines(3).TextColor(c.Theme().Danger).Selectable()
		}
		if a.busy || a.statusKey == "status.complete" {
			fraction := a.progress.Fraction
			ui.Progress(c, fraction).Height(5).Label(status)
		}
		ui.Row(c).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.PrimaryButton(c.Key("generate"), "").Label(a.t("build")).Disabled(a.busy || !a.canGenerate || len(missing) > 0).Padding(10, 16).Gap(8).Children(func() {
				ui.Icon(c, icons["archive"]).Size(18, 18)
				ui.Text(c, a.t("build"))
			}).OnClick(a.generate)
			ui.Button(c.Key("cancel"), "").Label(a.t("cancel")).Disabled(!a.busy || a.cancelling).Padding(10, 12).Gap(8).Children(func() {
				ui.Icon(c, icons["x"]).Size(18, 18)
				ui.Text(c, a.t("cancel"))
			}).OnClick(a.cancelWork)
			if !compact {
				ui.Spacer(c)
			}
			open := ui.Button(c.Key("open-output"), "").Label(a.t("output.open")).Disabled(a.busy || a.settings.LastOutput == "").Padding(10, 12).Gap(8).Children(func() {
				ui.Icon(c, icons["folder-open"]).Size(18, 18)
				ui.Text(c, a.t("output.open"))
			}).OnClick(a.openOutput)
			if a.settings.LastOutput != "" {
				open.Tooltip(a.settings.LastOutput)
			}
		})
	})
}

// list joins items with the separator the interface language uses.
func (a *application) list(items []string) string {
	switch a.settings.Locale {
	case "zh-CN", "zh-TW", "ja":
		return strings.Join(items, "、")
	}
	return strings.Join(items, ", ")
}
