package otlayout

import (
	"encoding/binary"
	"reflect"
	"testing"
)

// These fixtures are deliberately small OpenType subtables rather than complete
// fonts. They model the sharing HarfBuzz can retain after subsetting: several
// distinct subtables point at one RuleSet, and one subtable points at the same
// RuleSet more than once. A lookup index is stored at one physical byte offset,
// so rebasing it twice corrupts the rule even though the first rebase was valid.

type contextCall struct {
	at    int
	chain bool
}

type recordField struct {
	at  int
	old int
}

func syntheticContextBytes(length int, fields map[int]int) []byte {
	data := make([]byte, length)
	for at, value := range fields {
		if at < 0 || at+2 > len(data) {
			panic("synthetic context field outside table")
		}
		put16(data, at, value)
	}
	return data
}

func runFixContext(t *testing.T, source []byte, base, limit int, calls ...contextCall) []byte {
	t.Helper()
	b := buffer{data: append([]byte(nil), source...)}
	seen := map[int]bool{}
	for i, call := range calls {
		fixContext(&b, call.at, call.chain, base, limit, seen)
		if b.err != nil {
			t.Fatalf("fixContext call %d at %d (chain=%v): %v", i, call.at, call.chain, b.err)
		}
	}
	return b.data
}

func checkRecordFields(t *testing.T, source, result []byte, base int, fields []recordField) {
	t.Helper()
	for _, field := range fields {
		if field.at < 0 || field.at+2 > len(source) || field.at+2 > len(result) {
			t.Fatalf("record field %d outside table", field.at)
		}
		old := int(binary.BigEndian.Uint16(source[field.at:]))
		if old != field.old {
			t.Fatalf("fixture record at %d starts at %d, want %d", field.at, old, field.old)
		}
		got := int(binary.BigEndian.Uint16(result[field.at:]))
		want := field.old + base
		if got != want {
			t.Fatalf("record at %d = %d after shared traversal, want %d (one rebase of %d)", field.at, got, want, field.old)
		}
	}
}

func TestFixContextDoesNotRebaseSharedRecordsTwice(t *testing.T) {
	const (
		base  = 40
		limit = 8
	)

	t.Run("context format 1 shared RuleSet across subtables", func(t *testing.T) {
		// Two format-1 subtables have separate headers but both RuleSet offsets
		// resolve to the set at byte 20. Rule 0 and rule 1 have distinct record
		// fields, including two fields that intentionally share the value 1.
		source := syntheticContextBytes(48, map[int]int{
			// subtable A
			0: 1, 2: 0, 4: 1, 6: 20,
			// subtable B
			8: 1, 10: 0, 12: 1, 14: 12,
			// shared RuleSet
			20: 2, 22: 6, 24: 16,
			// rule 0: two input glyphs, one record
			26: 2, 28: 1, 30: 0, 32: 0, 34: 1,
			// rule 1: one input glyph, two records
			36: 1, 38: 2, 40: 0, 42: 1, 44: 1, 46: 2,
		})
		result := runFixContext(t, source, base, limit, contextCall{at: 0}, contextCall{at: 8})
		checkRecordFields(t, source, result, base, []recordField{
			{at: 34, old: 1},
			{at: 42, old: 1},
			{at: 46, old: 2},
		})
	})

	t.Run("context format 2 shared ClassSet offset", func(t *testing.T) {
		// A single format-2 subtable lists the same ClassSet twice. fixContext
		// traverses both entries, so the shared rule bytes must be seen once.
		source := syntheticContextBytes(40, map[int]int{
			0: 2, 2: 0, 4: 0, 6: 2, 8: 12, 10: 12,
			// shared ClassSet
			12: 2, 14: 6, 16: 16,
			// rule 0: two input glyphs, one record
			18: 2, 20: 1, 22: 1, 24: 1, 26: 4,
			// rule 1: one input glyph, two records
			28: 1, 30: 2, 32: 0, 34: 5, 36: 1, 38: 6,
		})
		result := runFixContext(t, source, base, limit, contextCall{at: 0})
		checkRecordFields(t, source, result, base, []recordField{
			{at: 26, old: 4},
			{at: 34, old: 5},
			{at: 38, old: 6},
		})
	})

	t.Run("chained context format 1 shared RuleSet across subtables", func(t *testing.T) {
		source := syntheticContextBytes(60, map[int]int{
			// subtable A
			0: 1, 2: 0, 4: 1, 6: 20,
			// subtable B
			8: 1, 10: 0, 12: 1, 14: 12,
			// shared chain RuleSet
			20: 2, 22: 6, 24: 24,
			// rule 0: one backtrack, two input, one lookahead, one record
			26: 1, 28: 7, 30: 2, 32: 0, 34: 1, 36: 6, 38: 1, 40: 0, 42: 1,
			// rule 1: no backtrack, one input, no lookahead, two records
			44: 0, 46: 1, 48: 0, 50: 2, 52: 0, 54: 1, 56: 0, 58: 2,
		})
		result := runFixContext(t, source, base, limit, contextCall{at: 0, chain: true}, contextCall{at: 8, chain: true})
		checkRecordFields(t, source, result, base, []recordField{
			{at: 42, old: 1},
			{at: 54, old: 1},
			{at: 58, old: 2},
		})
	})

	t.Run("chained context format 2 shared ClassSet across subtables", func(t *testing.T) {
		source := syntheticContextBytes(68, map[int]int{
			// subtable A
			0: 2, 2: 0, 4: 0, 6: 0, 8: 0, 10: 1, 12: 28,
			// subtable B
			14: 2, 16: 0, 18: 0, 20: 0, 22: 0, 24: 1, 26: 14,
			// shared chain ClassSet
			28: 2, 30: 6, 32: 24,
			// rule 0: one backtrack, two input, one lookahead, one record
			34: 1, 36: 1, 38: 2, 40: 0, 42: 1, 44: 1, 46: 1, 48: 0, 50: 2,
			// rule 1: no backtrack, one input, no lookahead, two records
			52: 0, 54: 1, 56: 0, 58: 2, 60: 0, 62: 2, 64: 1, 66: 3,
		})
		result := runFixContext(t, source, base, limit, contextCall{at: 0, chain: true}, contextCall{at: 14, chain: true})
		checkRecordFields(t, source, result, base, []recordField{
			{at: 50, old: 2},
			{at: 62, old: 2},
			{at: 66, old: 3},
		})
	})
}

// makeSharedGPOSPart returns a minimal GPOS table with two lookups. Lookup 0 is
// a context-positioning lookup whose two format-1 subtables share one RuleSet;
// lookup 1 is a harmless non-context lookup. The shared rule records deliberately
// have indices 0, 0, and 1, all valid for the two-entry source lookup list.
func makeSharedGPOSPart() []byte {
	data := make([]byte, 92)
	put32(data, 0, 0x10000)
	// Empty ScriptList and FeatureList; the LookupList is what this test exercises.
	put16(data, 4, 10)
	put16(data, 6, 12)
	put16(data, 8, 14)
	put16(data, 14, 2)  // lookupCount
	put16(data, 16, 6)  // lookup 0 at 20
	put16(data, 18, 64) // lookup 1 at 78

	// Context lookup: two subtables sharing the RuleSet at 50.
	put16(data, 20, 7)
	put16(data, 22, 0)
	put16(data, 24, 2)
	put16(data, 26, 10)
	put16(data, 28, 20)
	// Subtable A.
	put16(data, 30, 1)
	put16(data, 32, 0)
	put16(data, 34, 1)
	put16(data, 36, 20)
	// Subtable B.
	put16(data, 40, 1)
	put16(data, 42, 0)
	put16(data, 44, 1)
	put16(data, 46, 10)
	// Shared RuleSet and its two distinct rules.
	put16(data, 50, 2)
	put16(data, 52, 6)
	put16(data, 54, 16)
	put16(data, 56, 2)
	put16(data, 58, 1)
	put16(data, 60, 0)
	put16(data, 62, 0)
	put16(data, 64, 0)
	put16(data, 66, 1)
	put16(data, 68, 2)
	put16(data, 70, 0)
	put16(data, 72, 0)
	put16(data, 74, 1)
	put16(data, 76, 1)

	// Non-context lookup 1. mergeLayout only reads this header and its subtable
	// offset; the copied SinglePos payload is intentionally minimal.
	put16(data, 78, 1)
	put16(data, 80, 0)
	put16(data, 82, 1)
	put16(data, 84, 8)
	put16(data, 86, 1)
	put16(data, 88, 0)
	put16(data, 90, 0)
	return data
}

func contextFormat1RecordValues(t *testing.T, data []byte, p int) []int {
	t.Helper()
	b := buffer{data: data}
	if format := b.u16(p); format != 1 {
		t.Fatalf("context subtable at %d has format %d, want 1", p, format)
	}
	set := p + b.u16(p+6)
	rules := b.u16(set)
	var values []int
	for i := 0; i < rules; i++ {
		rule := set + b.u16(set+2+2*i)
		glyphs := b.u16(rule)
		records := b.u16(rule + 2)
		if glyphs == 0 {
			t.Fatalf("empty contextual input at %d", rule)
		}
		for j := 0; j < records; j++ {
			at := rule + 4 + 2*(glyphs-1) + 4*j + 2
			values = append(values, int(b.u16(at)))
		}
	}
	if b.err != nil {
		t.Fatalf("parse merged context subtable at %d: %v", p, b.err)
	}
	return values
}

func mergedGPOSContextValues(t *testing.T, table []byte, lookupIndex int) [][]int {
	t.Helper()
	b := buffer{data: table}
	list := b.u16(8)
	count := b.u16(list)
	if lookupIndex < 0 || lookupIndex >= count {
		t.Fatalf("merged GPOS lookup %d outside list of %d", lookupIndex, count)
	}
	lookup := list + b.u16(list+2+2*lookupIndex)
	kind := b.u16(lookup)
	subtables := b.u16(lookup + 4)
	var result [][]int
	for i := 0; i < subtables; i++ {
		p := lookup + b.u16(lookup+6+2*i)
		actual := kind
		if kind == 9 { // GPOS extension lookup
			if b.u16(p) != 1 {
				t.Fatalf("merged lookup %d subtable %d is not extension format 1", lookupIndex, i)
			}
			actual = b.u16(p + 2)
			p += b.u32(p + 4)
		}
		if actual != 7 {
			t.Fatalf("merged lookup %d subtable %d has type %d, want context positioning", lookupIndex, i, actual)
		}
		result = append(result, contextFormat1RecordValues(t, table, p))
	}
	if b.err != nil {
		t.Fatalf("parse merged GPOS lookup %d: %v", lookupIndex, b.err)
	}
	return result
}

func TestMergeRebasesSharedContextOncePerSource(t *testing.T) {
	part := Part{GPOS: makeSharedGPOSPart()}
	merged, err := Merge([]Part{part, part})
	if err != nil {
		t.Fatalf("merge two identical-shape GPOS sources: %v", err)
	}
	table := merged["GPOS"]
	if len(table) == 0 {
		t.Fatal("merge omitted GPOS")
	}

	// The first source has no lookup prefix (base 0). The second source starts
	// after two lookups (base 2). Identical byte offsets in the second source
	// must be processed by its own seen map, while each source's two subtables
	// must only rebase the shared rule once.
	cases := []struct {
		lookup int
		want   [][]int
	}{
		{lookup: 0, want: [][]int{{0, 0, 1}, {0, 0, 1}}}, // source 1, two subtables
		{lookup: 2, want: [][]int{{2, 2, 3}, {2, 2, 3}}}, // source 2, two subtables
	}
	for _, test := range cases {
		got := mergedGPOSContextValues(t, table, test.lookup)
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("merged lookup %d record values = %v, want %v", test.lookup, got, test.want)
		}
	}
}
