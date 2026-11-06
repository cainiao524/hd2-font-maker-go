package inventory

// UniqueFaces keeps the first occurrence of each content identity and remaps
// manual row choices to the surviving face. Equal weights alone are not
// duplicates: different drawings or character coverage must remain available.
func UniqueFaces(keys []string, pins map[int]int) ([]int, map[int]int) {
	seen := make(map[string]int)
	indices := make([]int, len(keys))
	var keep []int
	for i, key := range keys {
		index, ok := seen[key]
		if !ok {
			index = len(keep)
			seen[key] = index
			keep = append(keep, i)
		}
		indices[i] = index
	}
	if len(keep) == len(keys) {
		return keep, pins
	}
	var remapped map[int]int
	for target, index := range pins {
		if index < -1 || index >= len(indices) {
			continue
		}
		if remapped == nil {
			remapped = make(map[int]int)
		}
		if index >= 0 {
			index = indices[index]
		}
		remapped[target] = index
	}
	return keep, remapped
}
