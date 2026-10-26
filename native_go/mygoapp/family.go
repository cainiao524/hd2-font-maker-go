package mygoapp

import (
	"fmt"
	"path/filepath"
	"strings"

	"hd2font/native/internal/inventory"
)

// weightTargets are the rows shown for weight sources; game weights map onto
// them with inventory.WeightRow.
var weightTargets = []int{400, 500, 700}

func styleOf(s FaceStyle) inventory.Style {
	return inventory.Style{Family: s.Family, Subfamily: s.Subfamily, Weight: uint16(s.Weight), Width: uint16(s.Width), Italic: s.Italic}
}

// candidate returns face of the file at path as a family member.
func candidate(path string, face FontFace) FamilyFace {
	return FamilyFace{Path: path, FaceIndex: face.Index, Name: faceStyleName(face.Style, face.Name), Style: face.Style}
}

func faceStyleName(s FaceStyle, fallback string) string {
	if name := strings.TrimSpace(s.Family + " " + s.Subfamily); name != "" {
		return name
	}
	return fallback
}

// familyOf returns the selected face first and then every other face, of the
// same file or a stored sibling, that shares its family. A variable face has
// no family: its wght axis draws every weight.
func familyOf(path string, faces []FontFace, siblings []FamilyFace, selected FontFace) []FamilyFace {
	members := []FamilyFace{candidate(path, selected)}
	if len(selected.Axes) > 0 {
		return members
	}
	// Faces of equal weight all stay: they may cover different characters.
	id := func(m FamilyFace) string { return fmt.Sprintf("%s#%d", m.Path, m.FaceIndex) }
	seen := map[string]bool{id(members[0]): true}
	add := func(m FamilyFace) {
		if !seen[id(m)] && inventory.SameFamily(styleOf(selected.Style), styleOf(m.Style)) {
			seen[id(m)] = true
			members = append(members, m)
		}
	}
	for _, face := range faces {
		if face.Index != selected.Index && len(face.Axes) == 0 {
			add(candidate(path, face))
		}
	}
	for _, sibling := range siblings {
		add(sibling)
	}
	return members
}

// applyFamily anchors matched weights at the member closest to 400. Without
// weight matching, the explicitly selected first member stays the source.
func (a *application) applyFamily(members []FamilyFace) {
	if len(members) == 0 {
		return
	}
	primary := 0
	if a.settings.MatchWeights {
		weights := make([]uint16, len(members))
		for i, m := range members {
			weights[i] = uint16(m.Style.Weight)
		}
		primary, _ = inventory.PickWeight(weights, 400)
	}
	source := members[primary]
	a.settings.Font.Path, a.settings.Font.FaceIndex = source.Path, source.FaceIndex
	a.settings.Font.Family = nil
	for i, m := range members {
		if i != primary {
			a.settings.Font.Family = append(a.settings.Font.Family, m)
		}
	}
	a.settings.Font.WeightFaces = nil
	a.sourceFace = source
}

// members returns the source followed by its Family, as the pipeline indexes them.
func (a *application) members() []FamilyFace {
	return append([]FamilyFace{a.sourceFace}, a.settings.Font.Family...)
}

// remapWeightFaces keeps manual choices attached to the same stored face when
// rediscovery changes the member order. Removed faces return to automatic.
func remapWeightFaces(previous FontSelection, members []FamilyFace) map[int]int {
	old := append([]FamilyFace{{Path: previous.Path, FaceIndex: previous.FaceIndex}}, previous.Family...)
	var pins map[int]int
	for target, index := range previous.WeightFaces {
		mapped := -2
		if index == -1 {
			mapped = -1 // synthetic emphasis always refers to the current base
		} else if index >= 0 && index < len(old) {
			for i, member := range members {
				if member.Path == old[index].Path && member.FaceIndex == old[index].FaceIndex {
					mapped = i
					break
				}
			}
		}
		if mapped >= -1 {
			if pins == nil {
				pins = make(map[int]int)
			}
			pins[target] = mapped
		}
	}
	return pins
}

type weightRow struct {
	Target int
	Source string
	Badge  string
	Kind   string // native, synthetic or variable
}

// weightRows describes what each weight row will be drawn with, using the
// same choice the pipeline makes.
func (a *application) weightRows() []weightRow {
	var rows []weightRow
	members := a.members()
	variable := false
	for _, face := range a.faces {
		if a.isSource(face) {
			for _, axis := range face.Axes {
				if axis.Tag == "wght" {
					variable = true
					base, ok := a.settings.Font.Axes["wght"]
					if !ok {
						base = axis.Default
					}
					for _, target := range weightTargets {
						value := max(axis.Min, min(axis.Max, base+float32(target-400)))
						rows = append(rows, weightRow{target, members[0].Name, a.t("weight.variable", "value", fmt.Sprint(value)), "variable"})
					}
				}
			}
		}
	}
	if variable {
		return rows
	}
	weights := make([]uint16, len(members))
	for i, m := range members {
		weights[i] = uint16(m.Style.Weight)
	}
	for _, target := range weightTargets {
		index, emphasis := inventory.PickFace(weights, a.settings.Font.WeightFaces, target)
		if len(members) == 1 {
			// Without a family the build emboldens every weight above 400, as 2.0 did.
			index, emphasis = 0, 0
			if target > 400 {
				emphasis = target
			}
		}
		if index < 0 {
			continue
		}
		row := weightRow{target, members[index].Name, a.t("weight.native"), "native"}
		if emphasis > 0 {
			row.Badge, row.Kind = a.t("weight.synthetic"), "synthetic"
		}
		rows = append(rows, row)
	}
	return rows
}

// familyLine summarizes where the faces came from for the font card.
func (a *application) familyLine() string {
	members := a.members()
	if len(members) == 1 {
		return filepath.Base(members[0].Path)
	}
	files := map[string]bool{}
	var styles []string
	for _, m := range members {
		files[m.Path] = true
		styles = append(styles, strings.TrimSpace(m.Style.Subfamily))
	}
	return a.t("font.family_files", "count", fmt.Sprint(len(files)), "styles", a.list(styles))
}

// autoModName names the package after the font family unless the user named it.
func (a *application) autoModName() string {
	family := strings.TrimSpace(a.sourceFace.Style.Family)
	if family == "" {
		family = strings.TrimSuffix(filepath.Base(a.sourceFace.Path), filepath.Ext(a.sourceFace.Path))
	}
	return a.t("output.mod_default", "family", family)
}

// isSource reports whether face, of the imported file, is the selection's source.
func (a *application) isSource(face FontFace) bool {
	path := a.facesPath
	if path == "" {
		path = a.settings.Font.Path
	}
	return a.settings.Font.Path == path && a.settings.Font.FaceIndex == face.Index
}

// updateModName follows the font with the automatic name unless the user named
// the package.
func (a *application) updateModName() {
	auto := a.autoModName()
	if a.settings.ModName == "" || a.settings.ModName == a.lastAutoMod {
		a.settings.ModName = auto
	}
	a.lastAutoMod = auto
}

// memberLabels names each family member for the manual weight menu, adding the
// file and face when two members share a name.
func (a *application) memberLabels() []string {
	members := a.members()
	count := map[string]int{}
	for _, m := range members {
		count[m.Name]++
	}
	labels := make([]string, len(members))
	for i, m := range members {
		labels[i] = m.Name
		if count[m.Name] > 1 {
			labels[i] = fmt.Sprintf("%s (%s #%d)", m.Name, filepath.Base(m.Path), m.FaceIndex+1)
		}
	}
	return labels
}

// hasOtherFaces reports whether the imported file holds faces the family does
// not use, such as italics, other widths or other families, which only the
// face menu can select. Without weight matching, every collection face is
// selected through that menu.
func (a *application) hasOtherFaces() bool {
	if !a.settings.MatchWeights && len(a.faces) > 1 {
		return true
	}
	used := map[int]bool{}
	for _, m := range a.members() {
		if m.Path == a.facesPath {
			used[m.FaceIndex] = true
		}
	}
	for _, face := range a.faces {
		if !used[face.Index] {
			return true
		}
	}
	return false
}
