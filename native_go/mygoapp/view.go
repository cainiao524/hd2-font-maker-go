package mygoapp

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

var gameLanguages = []string{"sc", "tc", "latin", "jp", "ko"}

func (a *application) view(c *ui.Context) {
	a.drain()
	if a.gameFontFolder != "" && a.gameFontFolder != a.settings.GameFolder {
		a.gameFonts, a.gameFontFolder = nil, ""
		if a.gameFontID != "" {
			a.gameFontID = ""
			a.requestPreview()
		}
	}
	c.SetTheme(a.theme)
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
	ui.Column(c.Key("app")).Fill().Font(uiFont(a.settings.Locale)).FontSize(15).Children(func() {
		ui.Column(c.Key("header")).Padding(16, 20).Gap(10).Children(func() {
			ui.Row(c).Wrap().Gap(12).Children(func() {
				ui.Text(c, a.t("app.title")).FontSize(23).Bold().Grow(1)
				ui.Text(c, Version).FontSize(12).TextColor(c.Theme().TextMuted)
				var names []string
				for _, locale := range Locales {
					names = append(names, locale.Name)
				}
				if ui.Select(c.Key("locale"), &a.localeName, names).Width(250).Label(a.t("ui.language")).Tooltip(a.t("ui.language")).Changed() {
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
				ui.Text(c, a.native(0)).TextColor(ui.RGB(145, 83, 15))
			}
			if a.demo {
				ui.Text(c, a.native(3)).Bold().TextColor(ui.RGB(145, 83, 15))
			}
		})
		ui.Divider(c)
		ui.Scroll(c.Key("form-scroll")).Grow(1).MinHeight(0).TrackScroll(&a.scroll).Padding(18, 20).Gap(20).Children(func() {
			a.pathField(c, "game", a.t("game.path"), &a.settings.GameFolder, "folder-open", a.chooseGame)
			if compact {
				ui.Column(c.Key("configuration")).Gap(22).Children(func() { a.configuration(c) })
				ui.Divider(c)
				a.previewView(c)
			} else {
				ui.Row(c.Key("workspace")).AlignItems(ui.Start).Gap(28).Children(func() {
					ui.Column(c.Key("configuration")).Width(440).Shrink(0).Gap(22).Children(func() { a.configuration(c) })
					ui.Column(c.Key("preview-column")).Grow(1).MinWidth(0).Gap(12).Children(func() { a.previewView(c) })
				})
			}
			ui.Divider(c)
			a.pathField(c, "output", a.t("dialog.export_zip"), &a.settings.OutputZIP, "folder", a.chooseOutput)
			if a.settings.LastOutput != "" {
				ui.Text(c, a.settings.LastOutput).FontSize(13).TextColor(c.Theme().TextMuted).Selectable()
			}
			if a.errorKey != "" {
				ui.Column(c.Key("error")).Gap(6).Children(func() {
					ui.Text(c, a.t(a.errorKey)).TextColor(c.Theme().Danger).Bold().Selectable()
					if a.errorDetail != "" {
						ui.Text(c, a.errorDetail).FontSize(13).TextColor(c.Theme().Danger).Selectable()
					}
				})
			}
		})
		ui.Divider(c)
		a.footer(c, compact)
	})
}

func (a *application) pathField(c *ui.Context, key, label string, value *string, icon string, action func()) {
	ui.Column(c.Key(key + "-field")).Gap(6).Children(func() {
		ui.Text(c, label).Bold()
		ui.Row(c).Gap(8).Children(func() {
			if ui.TextInput(c.Key(key), value).Label(label).Grow(1).MinWidth(0).Height(38).Disabled(a.busy).Changed() {
				a.changed(false)
			}
			iconButton(c, key+"-browse", a.t("browse"), a.t("browse"), icon).Disabled(a.busy).OnClick(action)
		})
	})
}

func iconButton(c *ui.Context, key, label, tooltip, icon string) ui.Element {
	return ui.Button(c.Key(key), "").Size(38, 38).Padding(8).Label(label).Tooltip(tooltip).Children(func() { ui.Icon(c, icons[icon]).Size(20, 20) })
}

func (a *application) configuration(c *ui.Context) {
	ui.Column(c.Key("font-section")).Gap(8).Children(func() {
		ui.Text(c, a.t("section.font")).Bold().FontSize(17)
		ui.Row(c).Gap(8).Children(func() {
			if ui.TextInput(c.Key("font-path"), &a.fontDraft).Label(a.t("file.fonts")).Placeholder(a.t("font.supported")).Height(38).Grow(1).MinWidth(0).Disabled(a.busy).Submitted() {
				a.inspectFont(a.fontDraft)
			}
			iconButton(c, "choose-font", a.t("font.add"), a.t("font.add"), "file-type").Disabled(a.busy).OnClick(a.chooseFont)
			iconButton(c, "load-font", a.t("preview.update"), a.t("preview.update"), "refresh-cw").Disabled(a.busy || a.fontDraft == "").OnClick(func() { a.inspectFont(a.fontDraft) })
		})
		faceNames := make([]string, len(a.faces))
		for i, face := range a.faces {
			faceNames[i] = faceLabel(face)
		}
		ui.Text(c, a.t("dialog.face")).FontSize(13).TextColor(c.Theme().TextMuted)
		if ui.Select(c.Key("face"), &a.faceName, faceNames).FillWidth().Label(a.t("dialog.face")).Disabled(a.busy || len(a.faces) == 0).Changed() {
			for _, face := range a.faces {
				if faceLabel(face) == a.faceName {
					a.selectFace(face)
				}
			}
		}
		a.axesView(c)
	})
	ui.Column(c.Key("languages-section")).Gap(10).Children(func() {
		ui.Text(c, a.t("section.languages")).Bold().FontSize(17)
		ui.Row(c).Wrap().Gap(6).Children(func() {
			for _, preset := range []struct {
				key       string
				languages []string
			}{
				{"sc", []string{"sc"}}, {"tc", []string{"tc"}}, {"latin", []string{"latin"}}, {"chinese", []string{"sc", "tc"}},
				{"all", gameLanguages}, {"none", nil},
			} {
				if ui.Button(c.Key("preset-"+preset.key), a.t("preset."+preset.key)).Padding(5, 10).Disabled(a.busy).Clicked() {
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
		ui.Text(c, a.t("scope.summary", "groups", strings.Join(groups, ", "))).TextColor(c.Theme().TextMuted).FontSize(13)
		ui.Text(c, a.t("scope.mainGroups")).TextColor(c.Theme().TextMuted).FontSize(12)
	})
	ui.Column(c.Key("style-section")).Gap(12).Children(func() {
		ui.Text(c, a.t("section.style")).Bold().FontSize(17)
		if ui.Checkbox(c.Key("weight"), &a.settings.MatchWeights, a.t("weight.toggle")).Disabled(a.busy).Changed() {
			a.changed(true)
		}
		if ui.Checkbox(c.Key("outlines"), &a.settings.ProcessOutlines, a.t("outline.toggle")).Disabled(a.busy).Changed() {
			a.changed(true)
		}
	})
}

func (a *application) previewView(c *ui.Context) {
	ui.Column(c.Key("preview-section")).Gap(12).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, a.t("preview.title")).Bold().FontSize(17).Grow(1)
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
				iconButton(c, "read-game-fonts", a.t("preview.load_game"), a.t("preview.load_game"), "folder-open").Disabled(a.busy).OnClick(a.loadGameFonts)
			})
		}
		if ui.TextArea(c.Key("sample"), &a.settings.PreviewText).Label(a.t("preview.title")).Height(90).Disabled(a.busy).Changed() {
			a.changed(false)
			a.previewDirtyAt = time.Now()
		}
		ui.Box(c.Key("preview-image")).Height(260).Background(ui.RGB(246, 247, 249)).Border(1, ui.RGB(221, 225, 230)).Radius(4).Clip().Center().Children(func() {
			if a.preview != nil {
				ui.Image(c, a.preview).Fill().Fit(ui.Contain).Label(a.t("preview.title"))
			} else if a.previewBusy {
				ui.Text(c, a.t("preview.working")).TextColor(c.Theme().TextMuted)
			} else {
				ui.Text(c, a.t("preview.empty")).TextColor(c.Theme().TextMuted)
			}
		})
		if !a.canGenerate && len(a.faces) > 0 {
			ui.Text(c, a.t("weight.original")).FontSize(13).TextColor(c.Theme().TextMuted)
		}
		if a.previewMissing > 0 && a.gameFontID == "" {
			ui.Text(c, a.t("preview.missing")).FontSize(13).TextColor(c.Theme().TextMuted)
		}
	})
}

func (a *application) footer(c *ui.Context, compact bool) {
	ui.Column(c.Key("footer")).Padding(12, 20).Gap(10).Children(func() {
		status := a.t(a.statusKey)
		if a.statusKey == "native.cancelled" {
			status = a.native(1)
		}
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
		}
		if a.errorKey != "" {
			status = a.t(a.errorKey)
		}
		ui.Text(c, status).FontSize(13).MaxLines(2).Label("status")
		fraction := a.progress.Fraction
		if !a.busy && a.statusKey != "status.complete" {
			fraction = 0
		}
		ui.Progress(c, fraction).Height(5).Label(status)
		ui.Row(c).Wrap().Gap(8).Children(func() {
			ui.PrimaryButton(c.Key("generate"), "").Label(a.t("build")).Disabled(a.busy || !a.canGenerate).Padding(10, 16).Gap(8).Children(func() {
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
			ui.Button(c.Key("open-output"), "").Label(a.t("output.open")).Disabled(a.busy || a.settings.LastOutput == "").Padding(10, 12).Gap(8).Children(func() {
				ui.Icon(c, icons["folder-open"]).Size(18, 18)
				ui.Text(c, a.t("output.open"))
			}).OnClick(a.openOutput)
		})
	})
}
