package quadratic

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

func TestFontToolsCu2QuOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Curve     [4][2]float64
		Tolerance float64
		Spline    [][2]float64
	}
	if err = json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		got, err := Curve(r.Curve, r.Tolerance)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if len(got) != len(r.Spline) {
			t.Fatalf("case %d split count %d != %d", i, len(got), len(r.Spline))
		}
		for j := range got {
			for a := 0; a < 2; a++ {
				if math.Abs(got[j][a]-r.Spline[j][a]) > 1e-8 {
					t.Fatalf("case %d control mismatch", i)
				}
			}
		}
	}
}
