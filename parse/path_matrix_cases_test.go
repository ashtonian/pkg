//go:build unit || integration

package parse_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
	"github.com/stretchr/testify/require"
)

// pathDB holds filtertest.PathRecords in a path_records table whose attrs
// and settings columns are created by docType.
type pathDB struct {
	db      *sql.DB
	dialect filter.Dialect
	docType string // column type of both documents
	cast    string // wraps each document placeholder, e.g. "%s::jsonb"
}

func loadPathRecords(t *testing.T, p pathDB) *filter.ResourceSchema {
	t.Helper()
	if _, err := p.db.Exec("CREATE TABLE path_records (id TEXT PRIMARY KEY, attrs " + p.docType + ", settings " + p.docType + ")"); err != nil {
		t.Fatalf("create path_records: %v", err)
	}
	doc := func(i int) string {
		ph := p.dialect.Placeholder(i)
		if p.cast != "" {
			return fmt.Sprintf(p.cast, ph)
		}
		return ph
	}
	insert := "INSERT INTO path_records (id, attrs, settings) VALUES (" + p.dialect.Placeholder(1) + ", " + doc(2) + ", " + doc(3) + ")"
	for _, r := range filtertest.PathRecords() {
		var settings any
		if r.Settings != nil {
			b, err := json.Marshal(r.Settings)
			if err != nil {
				t.Fatal(err)
			}
			settings = string(b)
		}
		if _, err := p.db.Exec(insert, r.ID, jsonText(r.Attrs), settings); err != nil {
			t.Fatalf("insert %s: %v", r.ID, err)
		}
	}
	schema, err := filtertest.PathSchema()
	if err != nil {
		t.Fatalf("path schema: %v", err)
	}
	return schema
}

// runPathMatrix runs the path cases on p, then checks that every declared
// path reads, row by row, the value filter.PathValue gives the same
// document: the SQL rendering and the reference evaluator are one rule.
func runPathMatrix(t *testing.T, p pathDB, storage filter.JSONStorage) {
	t.Helper()
	schema := loadPathRecords(t, p)
	if storage != "" {
		schema = withStorage(t, schema, storage)
	}
	t.Run("cases", func(t *testing.T) {
		filtertest.RunPaths(t, schema, func(t *testing.T, f *filter.ParsedFilter) ([]string, error) {
			res, err := filter.NewSQLBuilder(schema, p.dialect).Build(f.AST)
			if err != nil {
				return nil, err
			}
			return queryIDs(t, p.db, "SELECT id FROM path_records WHERE "+res.Where, res.Args), nil
		})
	})
	t.Run("values match PathValue", func(t *testing.T) {
		names := slices.Sorted(func(yield func(string) bool) {
			for n, f := range schema.Fields {
				if f.Path != nil && !yield(n) {
					return
				}
			}
		})
		if len(names) != 10 {
			t.Fatalf("declared paths = %v, want the fixture's 10", names)
		}
		for _, name := range names {
			f := schema.Fields[name]
			rows, err := p.db.Query("SELECT id, " + f.Path.Column + ", " + filter.FieldExpr(p.dialect, f) + " FROM path_records ORDER BY id")
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for rows.Next() {
				var id string
				var doc, got any
				if err := rows.Scan(&id, &doc, &got); err != nil {
					t.Fatal(err)
				}
				want, present, err := filter.PathValue(textOf(doc), f)
				if err != nil {
					t.Fatalf("%s row %s: %v", name, id, err)
				}
				if !present {
					want = nil
				}
				if !sameValue(got, want) {
					t.Errorf("%s row %s: SQL %v (%T), PathValue %v (%T)", name, id, got, got, want, want)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			require.NoError(t, rows.Close())
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
	switch x := v.(type) {
	case []byte:
		return string(x)
	}
	return v
}

// sameValue compares a scanned SQL value with PathValue's: numbers by value
// (drivers return int64, float64 or numeric text), booleans across 0/1.
func sameValue(got, want any) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
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
		if gi, gok := got.(int64); gok {
			if wi, wok := w.(int64); wok {
				return gi == wi
			}
		}
		if s, found := got.(string); found {
			if wi, wok := w.(int64); wok {
				return s == fmt.Sprint(wi)
			}
		}
		return ok && g == wf
	}
	if b, ok := got.([]byte); ok {
		got = string(b)
	}
	return reflect.DeepEqual(got, want)
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
