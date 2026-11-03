package inventory

import "testing"

func TestPickWeight(t *testing.T) {
	for _, c := range []struct {
		weights   []uint16
		target    int
		want      uint16
		synthetic bool
	}{
		{[]uint16{400, 700}, 400, 400, false},
		{[]uint16{400, 700}, 700, 700, false},
		{[]uint16{400, 700}, 500, 400, true},
		{[]uint16{400, 500, 700}, 500, 500, false},
		{[]uint16{300, 500}, 400, 500, false},
		{[]uint16{300, 600}, 400, 300, true},
		{[]uint16{400}, 700, 400, true},
		{[]uint16{400, 600}, 700, 600, true},
		{[]uint16{400, 800}, 700, 800, false},
		{[]uint16{500, 700}, 400, 500, false},
		{[]uint16{700}, 400, 700, false},
		{[]uint16{350, 650}, 600, 650, false},
	} {
		i, synthetic := PickWeight(c.weights, c.target)
		if c.weights[i] != c.want || synthetic != c.synthetic {
			t.Errorf("PickWeight(%v, %d) = %d synthetic=%v, want %d synthetic=%v", c.weights, c.target, c.weights[i], synthetic, c.want, c.synthetic)
		}
	}
	if i, _ := PickWeight(nil, 400); i != -1 {
		t.Errorf("empty weights picked %d", i)
	}
}

func TestSingleFontAnchorAndEmphasis(t *testing.T) {
	for _, declared := range []int{0, -5, 400, 1001} {
		if got := SingleFontAnchor(declared); got != 500 {
			t.Errorf("SingleFontAnchor(%d) = %d, want 500", declared, got)
		}
	}
	for _, declared := range []int{1, 300, 450, 500, 700, 900, 1000} {
		if got := SingleFontAnchor(declared); got != declared {
			t.Errorf("SingleFontAnchor(%d) = %d, want the declared class", declared, got)
		}
	}
	for _, c := range []struct {
		declared, target int
		pins             map[int]int
		emphasis         int
		method           string
	}{
		{400, 500, nil, 0, "authored_static"},
		{0, 500, nil, 0, "authored_static"},
		{400, 400, nil, 300, "synthetic_reduction"},
		{400, 700, nil, 600, "synthetic_emphasis"},
		{400, 700, map[int]int{700: 0}, 0, "authored_static"},
		{400, 400, map[int]int{400: -1}, 300, "synthetic_reduction"},
		{700, 700, nil, 0, "authored_static"},
		{700, 500, nil, 200, "synthetic_reduction"},
		{700, 400, nil, 100, "synthetic_reduction"},
		{300, 400, nil, 500, "synthetic_emphasis"},
		{500, 680, nil, 580, "synthetic_emphasis"},
	} {
		emphasis, method := SingleFontEmphasis(c.declared, c.target, c.pins)
		if emphasis != c.emphasis || method != c.method {
			t.Errorf("SingleFontEmphasis(%d, %d, %v) = %d %s, want %d %s", c.declared, c.target, c.pins, emphasis, method, c.emphasis, c.method)
		}
	}
}

func TestEmphasisRadiusAmounts(t *testing.T) {
	for _, c := range []struct {
		upm    float64
		weight int
		pixel  bool
		want   float64
	}{
		{1000, 400, false, 0},
		{1000, 500, false, 8},
		{1000, 600, false, 15},
		{1000, 700, false, 22},
		{1000, 300, false, -5},
		{1000, 100, false, -5},
		{2048, 300, false, -10},
		{1000, 700, true, 24},
		{1024, 300, true, -4},
	} {
		if got := EmphasisRadius(c.upm, c.weight, c.pixel); got != c.want {
			t.Errorf("EmphasisRadius(%v, %d, %v) = %v, want %v", c.upm, c.weight, c.pixel, got, c.want)
		}
	}
}

func TestSameFamily(t *testing.T) {
	regular := Style{Family: "Noto Sans TC", Weight: 400, Width: 5}
	for _, c := range []struct {
		other Style
		want  bool
	}{
		{Style{Family: "noto sans tc ", Weight: 700, Width: 5}, true},
		{Style{Family: "Noto Sans TC", Weight: 700, Width: 3}, false},
		{Style{Family: "Noto Sans TC", Weight: 400, Width: 5, Italic: true}, false},
		{Style{Family: "Noto Serif TC", Weight: 700, Width: 5}, false},
	} {
		if got := SameFamily(regular, c.other); got != c.want {
			t.Errorf("SameFamily(%+v) = %v, want %v", c.other, got, c.want)
		}
	}
}
