package inventory

import (
	"reflect"
	"testing"
)

func TestDuplicateFacesKeepManualSources(t *testing.T) {
	pins := map[int]int{400: -1, 500: 1, 700: 3}
	keep, remapped := UniqueFaces([]string{"regular", "regular", "bold", "bold"}, pins)
	if !reflect.DeepEqual(keep, []int{0, 2}) || !reflect.DeepEqual(remapped, map[int]int{400: -1, 500: 0, 700: 1}) {
		t.Fatalf("duplicate removal changed manual sources: %v %v", keep, remapped)
	}
	if pins[500] != 1 || pins[700] != 3 {
		t.Fatal("deduplication mutated caller settings")
	}
}
