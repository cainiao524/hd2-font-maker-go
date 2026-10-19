package sfntio

import (
	"encoding/binary"
	"math/bits"
	"sort"
)

type cmapSegment struct {
	first, last int
	delta       bool
}

// UnicodeCmap writes the complete repertoire. A minimal-cost BMP segmentation
// keeps format 4 compact; format 12 always contains every code point.
func UnicodeCmap(mapping map[rune]uint16) []byte {
	keys := make([]int, 0, len(mapping))
	for cp := range mapping {
		if cp >= 0 && cp <= 0x10ffff {
			keys = append(keys, int(cp))
		}
	}
	sort.Ints(keys)
	var groups [][3]uint32
	for i := 0; i < len(keys); {
		first, last, gid := keys[i], keys[i], mapping[rune(keys[i])]
		i++
		for i < len(keys) && keys[i] == last+1 && int(mapping[rune(keys[i])]) == int(gid)+keys[i]-first {
			last = keys[i]
			i++
		}
		groups = append(groups, [3]uint32{uint32(first), uint32(last), uint32(gid)})
	}
	f12 := make([]byte, 16+12*len(groups))
	be := binary.BigEndian
	be.PutUint16(f12, 12)
	be.PutUint32(f12[4:], uint32(len(f12)))
	be.PutUint32(f12[12:], uint32(len(groups)))
	for i, g := range groups {
		for j, v := range g {
			be.PutUint32(f12[16+12*i+4*j:], v)
		}
	}
	bmpEnd := sort.SearchInts(keys, 0xffff)
	bmp := keys[:bmpEnd]
	n := len(bmp)
	cost := make([]int, n+1)
	previous := make([]int, n+1)
	delta := make([]bool, n+1)
	best, start, bestAt := 0, 0, 0
	for i := 1; i <= n; i++ {
		at := i - 1
		if at == 0 || bmp[at] != bmp[at-1]+1 || uint16(int(mapping[rune(bmp[at])])-bmp[at]) != uint16(int(mapping[rune(bmp[at-1])])-bmp[at-1]) {
			start = at
		}
		candidate := cost[at] - 2*bmp[at]
		if at == 0 || candidate < best {
			best, bestAt = candidate, at
		}
		cost[i], previous[i], delta[i] = cost[start]+8, start, true
		arrayCost := best + 8 + 2*(bmp[at]+1)
		if arrayCost < cost[i] {
			cost[i], previous[i], delta[i] = arrayCost, bestAt, false
		}
	}
	var segments []cmapSegment
	for i := n; i > 0; i = previous[i] {
		segments = append(segments, cmapSegment{bmp[previous[i]], bmp[i-1], delta[i]})
	}
	for i, j := 0, len(segments)-1; i < j; i, j = i+1, j-1 {
		segments[i], segments[j] = segments[j], segments[i]
	}
	segments = append(segments, cmapSegment{65535, 65535, true})
	var f4 []byte
	if 16+8+cost[n] <= 65535 {
		count := len(segments)
		f4 = make([]byte, 16+8*count)
		be.PutUint16(f4, 4)
		be.PutUint16(f4[6:], uint16(count*2))
		power := bits.Len(uint(count)) - 1
		be.PutUint16(f4[8:], uint16(2*(1<<power)))
		be.PutUint16(f4[10:], uint16(power))
		be.PutUint16(f4[12:], uint16(2*count-2*(1<<power)))
		for i, s := range segments {
			be.PutUint16(f4[14+2*i:], uint16(s.last))
			be.PutUint16(f4[16+2*count+2*i:], uint16(s.first))
			dpos := 16 + 4*count + 2*i
			opos := 16 + 6*count + 2*i
			if s.delta {
				gid := mapping[rune(s.first)]
				if s.first == 65535 {
					gid = 0
				}
				be.PutUint16(f4[dpos:], uint16(int(gid)-s.first))
			} else {
				be.PutUint16(f4[opos:], uint16(len(f4)-opos))
				for cp := s.first; cp <= s.last; cp++ {
					f4 = be.AppendUint16(f4, mapping[rune(cp)])
				}
			}
		}
		be.PutUint16(f4[2:], uint16(len(f4)))
	}
	count := 1
	if f4 != nil {
		count = 2
	}
	out := make([]byte, 4+8*count)
	be.PutUint16(out[2:], uint16(count))
	at := 4
	if f4 != nil {
		be.PutUint16(out[at:], 3)
		be.PutUint16(out[at+2:], 1)
		be.PutUint32(out[at+4:], uint32(len(out)))
		out = append(out, f4...)
		at += 8
	}
	be.PutUint16(out[at:], 3)
	be.PutUint16(out[at+2:], 10)
	be.PutUint32(out[at+4:], uint32(len(out)))
	return append(out, f12...)
}
