//go:build unit

package core_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// BenchmarkKeysetWalk sorts a page of canonical key values and seeks past a
// cursor in memory, as a memory-backed list does. Every comparison reads
// both values in their key's type's order (core.CompareCanonical); a number
// key holds integers and floats alike, and a path member's number as
// written.
func BenchmarkKeysetWalk(b *testing.B) {
	const rows = 1000
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	keys := map[string]struct {
		key    filter.CursorKey
		values func(i int) any
	}{
		"string": {filter.CursorKey{DataType: core.FieldTypeString}, func(i int) any { return fmt.Sprintf("name-%05d", (i*7919)%rows) }},
		"timestamp": {filter.CursorKey{DataType: core.FieldTypeTimestamp}, func(i int) any {
			return core.FormatTimestamp(start.Add(time.Duration((i*7919)%rows) * time.Second))
		}},
		"float": {filter.CursorKey{DataType: core.FieldTypeFloat}, func(i int) any { return float64((i*7919)%rows) + 0.5 }},
		"mixed int and float": {filter.CursorKey{DataType: core.FieldTypeFloat}, func(i int) any {
			if i%2 == 0 {
				return int64((i * 7919) % rows)
			}
			return float64((i*7919)%rows) + 0.25
		}},
		"document literal": {filter.CursorKey{DataType: core.FieldTypeFloat}, func(i int) any {
			return json.Number(fmt.Sprintf("%d.5", (i*7919)%rows))
		}},
	}
	for _, name := range []string{"string", "timestamp", "float", "mixed int and float", "document literal"} {
		k := keys[name]
		values := make([]any, rows)
		for i := range values {
			values[i] = k.values(i)
		}
		cursor := values[rows/2]
		page := make([]any, rows)
		b.Run(name, func(b *testing.B) {
			compare := func(x, y any) int {
				c, err := filter.CompareKeyValues(k.key, x, y)
				if err != nil {
					b.Fatal(err)
				}
				return c
			}
			b.ReportAllocs()
			for b.Loop() {
				copy(page, values)
				slices.SortFunc(page, compare)
				after := 0
				for after < len(page) && compare(page[after], cursor) <= 0 {
					after++
				}
				if after == 0 {
					b.Fatal("the cursor is on the page")
				}
			}
		})
	}
}
