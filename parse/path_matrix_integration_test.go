//go:build integration

package parse_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// Typed JSON paths on each backend: SQLite's documents are JSON text, and
// PostgreSQL's are each storage a document column can declare.
func TestPathMatrix(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		if b.engine == sqliteEngine {
			runPathMatrix(t, b, "")
			return
		}
		for _, storage := range []filter.JSONStorage{filter.StorageJSONB, filter.StorageJSON, filter.StorageText} {
			t.Run(string(storage), func(t *testing.T) { runPathMatrix(t, b, storage) })
		}
	})
}

// loadPathRecords writes filtertest.PathRecords into path_records on a
// fresh database of b, its documents held as schema declares them.
func loadPathRecords(t *testing.T, b sqlBackend, schema *filter.ResourceSchema) *sql.DB {
	t.Helper()
	db := b.open(t)
	table := schemaTable(schema)
	b.load(t, db, table, recordRows(t, table, filtertest.PathRecords()))
	return db
}

// pathSchema is filtertest.PathSchema with its documents held as storage,
// or as the schema declares them for none.
func pathSchema(t *testing.T, storage filter.JSONStorage) *filter.ResourceSchema {
	t.Helper()
	schema, err := filtertest.PathSchema()
	require.NoError(t, err)
	if storage != "" {
		schema = withStorage(t, schema, storage)
	}
	return schema
}

// runPathMatrix runs the path cases on b, its documents held as storage,
// then checks that every declared path reads, row by row, the value
// filter.PathValue gives the same document: the SQL rendering and the
// reference evaluator are one rule.
func runPathMatrix(t *testing.T, b sqlBackend, storage filter.JSONStorage) {
	t.Helper()
	schema := pathSchema(t, storage)
	db := loadPathRecords(t, b, schema)
	query := b.filterIDs(db, schema)
	t.Run("cases", func(t *testing.T) { filtertest.RunPaths(t, schema, query) })
	t.Run("sets", func(t *testing.T) { filtertest.RunPathSets(t, schema, query) })
	t.Run("values match PathValue", func(t *testing.T) {
		names := slices.Sorted(func(yield func(string) bool) {
			for n, f := range schema.Fields {
				if f.Path != nil && !yield(n) {
					return
				}
			}
		})
		require.Len(t, names, 14, "the fixture's declared paths")
		for _, name := range names {
			f := schema.Fields[name]
			for _, row := range queryRows[[]any](t, db, "SELECT id, "+f.Path.Column+", "+filter.FieldExpr(b.dialect, f)+" FROM path_records ORDER BY id") {
				id, doc, got := row[0], row[1], row[2]
				want, present, err := filter.PathValue(textOf(doc), f)
				require.NoError(t, err, "%s row %v", name, id)
				if !present {
					want = nil
				}
				same := sameValue(got, want)
				if f.DataType == core.FieldTypeDecimal {
					same = sameDecimal(got, want)
				}
				assert.True(t, same, "%s row %v: SQL %v (%T), PathValue %v (%T)", name, id, got, got, want, want)
			}
		}
	})
}

// withStorage declares the fixture's documents held as storage.
func withStorage(t *testing.T, s *filter.ResourceSchema, storage filter.JSONStorage) *filter.ResourceSchema {
	t.Helper()
	out := *s
	out.Fields = map[string]*filter.FieldSchema{}
	for n, f := range s.Fields {
		c := *f
		if c.Path != nil {
			p := *c.Path
			p.Storage = storage
			c.Path = &p
		} else if core.IsDocument(c.DataType) {
			c.Storage = storage
		}
		out.Fields[n] = &c
	}
	return &out
}

func textOf(v any) any {
	b, ok := v.([]byte)
	if ok {
		return string(b)
	}
	return v
}

// sameValue compares a scanned SQL value with PathValue's: numbers by value
// (drivers return int64, float64 or numeric text), booleans across 0/1. A
// number member, which PathValue reads exactly, is PostgreSQL's numeric
// text and SQLite's INTEGER exactly, and SQLite's REAL as the double
// json_extract rounds it to.
func sameValue(got, want any) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	if n, ok := want.(json.Number); ok {
		if g, isFloat := got.(float64); isFloat {
			wf, _ := strconv.ParseFloat(string(n), 64) // ±Inf beyond float64's range, as SQLite reads it
			return g == wf
		}
		return sameDecimal(got, string(n))
	}
	switch w := want.(type) {
	case bool:
		switch g := got.(type) {
		case bool:
			return g == w
		case int64:
			return (g == 1) == w
		}
		return false
	case int64, float64:
		g, ok := numberOf(got)
		wf, _ := numberOf(w)
		wi, wInt := w.(int64)
		gi, gInt := got.(int64)
		if gInt && wInt {
			return gi == wi
		}
		s, gText := got.(string)
		if gText && wInt {
			return s == fmt.Sprint(wi)
		}
		return ok && g == wf
	}
	return reflect.DeepEqual(textOf(got), want)
}

// sameDecimal compares a decimal path's SQL value — a number, or numeric
// text keeping its scale — with PathValue's canonical text by value.
func sameDecimal(got, want any) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	c, ok := core.CompareDecimals(textOf(got), want)
	return ok && c == 0
}

func numberOf(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case []byte:
		var f float64
		_, err := fmt.Sscan(string(x), &f)
		return f, err == nil
	case string:
		var f float64
		_, err := fmt.Sscan(x, &f)
		return f, err == nil
	}
	return 0, false
}
