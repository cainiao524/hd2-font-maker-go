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
