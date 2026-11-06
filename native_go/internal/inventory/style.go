package inventory

import (
	"encoding/binary"
	"math"
	"strings"

	"hd2font/native/internal/sfntio"
)

// Style is what weight matching needs to know about one face.
type Style struct {
	Family    string
	Subfamily string
	Weight    uint16
	Width     uint16
	Italic    bool
}

// DescribeStyle reads face index of a font or collection: the typographic
// family (name 16, else 1), subfamily (17, else 2), OS/2 weight and width
// classes, and whether fsSelection or macStyle marks it italic.
func DescribeStyle(raw []byte, index int) (Style, error) {
	f, err := sfntio.Parse(raw, index)
	if err != nil {
		return Style{}, err
	}
	style := Style{Weight: 400, Width: 5}
	if style.Family, err = nameRecord(f.Tables["name"], 16, 1); err != nil {
		return Style{}, err
	}
	style.Subfamily, _ = nameRecord(f.Tables["name"], 17, 2)
	if os2 := f.Tables["OS/2"]; len(os2) >= 64 {
		style.Weight = binary.BigEndian.Uint16(os2[4:])
		style.Width = binary.BigEndian.Uint16(os2[6:])
		style.Italic = binary.BigEndian.Uint16(os2[62:])&1 != 0
	} else if len(os2) >= 8 {
		style.Weight = binary.BigEndian.Uint16(os2[4:])
		style.Width = binary.BigEndian.Uint16(os2[6:])
	}
	if head := f.Tables["head"]; len(head) >= 46 && binary.BigEndian.Uint16(head[44:])&2 != 0 {
		style.Italic = true
	}
	return style, nil
}

// SameFamily reports whether b belongs beside a when choosing weights: the
// same family name, the same width, and upright like a.
func SameFamily(a, b Style) bool {
	return strings.EqualFold(strings.TrimSpace(a.Family), strings.TrimSpace(b.Family)) && a.Width == b.Width && !b.Italic && !a.Italic
}

// PickWeight chooses among weights the one a target weight uses, following
// CSS font matching: 400 tries 500 next, 500 tries 400 next, then lighter
// weights for targets up to 500 and heavier ones above it. Synthetic is true
// when the chosen weight is 100 or more lighter than the target, so the face
// still needs emphasis of target-chosen.
func PickWeight(weights []uint16, target int) (index int, synthetic bool) {
	if len(weights) == 0 {
		return -1, false
	}
	best := -1
	better := func(i int) bool {
		if best < 0 {
			return true
		}
		w, b := int(weights[i]), int(weights[best])
		return rank(w, target) < rank(b, target) || (rank(w, target) == rank(b, target) && distance(w, target) < distance(b, target))
	}
	for i := range weights {
		if better(i) {
			best = i
		}
	}
	chosen := int(weights[best])
	return best, target-chosen >= 100
}

// rank orders candidate weights for target into CSS preference bands.
func rank(w, target int) int {
	switch {
	case w == target:
		return 0
	case target >= 400 && target <= 500:
		switch {
		case w > target && w <= 500:
			return 1
		case w < target:
			return 2
		default:
			return 3
		}
	case target < 400:
		if w < target {
			return 1
		}
		return 2
	default:
		if w > target {
			return 1
		}
		return 2
	}
}

func distance(a, b int) int {
	if a < b {
		return b - a
	}
	return a - b
}

// WeightRow maps a game weight to the 400, 500 or 700 row that pins name.
func WeightRow(weight int) int {
	switch {
	case weight <= 450:
		return 400
	case weight <= 600:
		return 500
	}
	return 700
}

// SingleFontAnchor is the design weight a lone static face stands in for.
// The user can assign it to Regular (400), Medium (500), or Bold (700).
// Unset or invalid choices default to Medium, regardless of font metadata.
func SingleFontAnchor(selected int) int {
	if selected == 400 || selected == 500 || selected == 700 {
		return selected
	}
	return 500
}

// SingleFontEmphasis decides how one game weight is drawn from a lone static
// face. pins may be nil. A pin of 0 draws that only face unchanged; a pin of
// -1 forces synthesis; any other pin is ignored and the anchor is used.
// emphasis is the value to pass to synthetic styling, measured from 400 so the
// existing bold curve is unchanged, or 0 when the outline is used as drawn.
// method is authored_static, synthetic_emphasis, or synthetic_reduction.
func SingleFontEmphasis(selected, target int, pins map[int]int) (emphasis int, method string) {
	anchor := SingleFontAnchor(selected)
	if pins != nil {
		if pin, ok := pins[WeightRow(target)]; ok && pin == 0 {
			return 0, "authored_static"
		}
	}
	if target == anchor {
		return 0, "authored_static"
	}
	emphasis = 400 + target - anchor
	if target < anchor {
		return emphasis, "synthetic_reduction"
	}
	return emphasis, "synthetic_emphasis"
}

// EmphasisRadius is the signed outline offset, in font units, for a synthetic
// weight measured from 400. Positive values expand at 0.375% of UPM per 100
// classes, capped at 1.125%, to retain small text's counters and stroke gaps.
// Negative values erode at 0.50% of UPM per 100 classes and
// are capped at 0.50% of UPM, so the medium-to-regular step is half a percent
// of the em and a larger gap is not cut any deeper.
func EmphasisRadius(upm float64, weight int, pixelGrid bool) float64 {
	if upm <= 0 {
		return 0
	}
	delta := float64(weight - 400)
	var radius float64
	switch {
	case delta > 0:
		radius = math.RoundToEven(upm * math.Min(0.01125, delta*0.0000375))
	case delta < 0:
		radius = -math.RoundToEven(upm * math.Min(0.005, -delta*0.00005))
	default:
		return 0
	}
	if !pixelGrid || radius == 0 {
		return radius
	}
	sign := 1.0
	if radius < 0 {
		sign = -1
		radius = -radius
	}
	return sign * math.Max(1, math.RoundToEven(radius/4)) * 4
}

// PickFace chooses the face of weights (the source first) that a target
// weight uses. pins may fix a row's face by index, or -1 for synthetic
// emphasis of the source. Emphasis is the weight to pass to synthetic
// emboldening, measured from 400 as it is, or 0 when the face is used as drawn.
func PickFace(weights []uint16, pins map[int]int, target int) (index, emphasis int) {
	synthetic := false
	if pinned, ok := pins[WeightRow(target)]; ok && pinned >= -1 && pinned < len(weights) {
		index = max(pinned, 0)
		synthetic = target-int(weights[index]) >= 100
	} else {
		index, synthetic = PickWeight(weights, target)
	}
	if index < 0 || !synthetic {
		return index, 0
	}
	return index, 400 + target - int(weights[index])
}
