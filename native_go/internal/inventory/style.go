package inventory

import (
	"encoding/binary"
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
