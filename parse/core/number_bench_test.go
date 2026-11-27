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

// keysetWalk is one key a walk orders rows by, and each row's stored value
// of it, every one distinct.
type keysetWalk struct {
	name   string
	key    filter.CursorKey
	values []any
}

// keysetWalks are the keys BenchmarkKeysetWalk and
// BenchmarkKeysetWalkCanonical walk, each over rows stored values.
func keysetWalks(rows int) []keysetWalk {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	keys := []struct {
		name   string
		key    filter.CursorKey
		values func(i int) any
	}{
		{"string", filter.CursorKey{DataType: core.FieldTypeString}, func(i int) any { return fmt.Sprintf("name-%05d", (i*7919)%rows) }},
		{"timestamp", filter.CursorKey{DataType: core.FieldTypeTimestamp}, func(i int) any {
			return core.FormatTimestamp(start.Add(time.Duration((i*7919)%rows) * time.Second))
		}},
		{"uuid", filter.CursorKey{DataType: core.FieldTypeUUID}, func(i int) any {
			return fmt.Sprintf("0198a3c2-7a1e-7c00-8000-%012x", (i*7919)%rows)
		}},
		{"date", filter.CursorKey{DataType: core.FieldTypeDate}, func(i int) any {
			return start.AddDate(0, 0, (i*7919)%rows).Format(time.DateOnly)
		}},
		{"float", filter.CursorKey{DataType: core.FieldTypeFloat}, func(i int) any { return float64((i*7919)%rows) + 0.5 }},
		{"mixed int and float", filter.CursorKey{DataType: core.FieldTypeFloat}, func(i int) any {
			if i%2 == 0 {
				return int64((i * 7919) % rows)
			}
			return float64((i*7919)%rows) + 0.25
		}},
		{"document literal", filter.CursorKey{DataType: core.FieldTypeFloat}, func(i int) any {
			return json.Number(fmt.Sprintf("%d.5", (i*7919)%rows))
		}},
	}
	walks := make([]keysetWalk, len(keys))
	for i, k := range keys {
		values := make([]any, rows)
		for j := range values {
			values[j] = k.values(j)
		}
		walks[i] = keysetWalk{name: k.name, key: k.key, values: values}
	}
	return walks
}

// BenchmarkKeysetWalk sorts a page of stored key values and seeks past a
// cursor in memory. Every comparison reads both values in their key's
// type's order (core.CompareCanonical) from the form a row stores them in;
// a number key holds integers and floats alike, and a path member's number
// as written.
func BenchmarkKeysetWalk(b *testing.B) {
	const rows = 1000
	for _, w := range keysetWalks(rows) {
		page := make([]any, rows)
		cursor := w.values[rows/2]
		b.Run(w.name, func(b *testing.B) {
			compare := func(x, y any) int {
				c, err := filter.CompareKeyValues(w.key, x, y)
				if err != nil {
					b.Fatal(err)
				}
				return c
			}
			b.ReportAllocs()
			for b.Loop() {
				copy(page, w.values)
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

// BenchmarkKeysetWalkCanonical is BenchmarkKeysetWalk over each row's key
// read once (filter.KeyValueOrder), as parsememory's walk reads a row's
// sort keys: a key whose canonical text orders as its values compares by
// code point, where BenchmarkKeysetWalk parses a timestamp's, a date's or
// a UUID's two texts on every comparison.
func BenchmarkKeysetWalkCanonical(b *testing.B) {
	const rows = 1000
	for _, w := range keysetWalks(rows) {
		order := filter.NewKeyValueOrder(w.key)
		read := make([]filter.SortValue, rows)
		for i, v := range w.values {
			sv, err := order.Read(v)
			if err != nil {
				b.Fatal(err)
			}
			read[i] = sv
		}
		page := make([]filter.SortValue, rows)
		cursor := read[rows/2]
		b.Run(w.name, func(b *testing.B) {
			compare := func(x, y filter.SortValue) int {
				c, err := order.Compare(x, y)
				if err != nil {
					b.Fatal(err)
				}
				return c
			}
			b.ReportAllocs()
			for b.Loop() {
				copy(page, read)
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
