//go:build unit

package filter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// The reference evaluator answers every conformance case the SQL dialects
// answer: over rows held as Go values, and over paths whose documents are
// held as JSON text, Go structs or decoded JSON.

func TestEvaluateConformance(t *testing.T) {
	filtertest.Run(t, "memory", evaluateOver(filtertest.Records(), func(r filtertest.Record) (string, filter.Row) {
		return r.ID, structRow(r)
	}))
}

func TestEvaluateSetConformance(t *testing.T) {
	filtertest.RunSets(t, "memory", evaluateOver(filtertest.Records(), func(r filtertest.Record) (string, filter.Row) {
		return r.ID, structRow(r)
	}))
	schema, err := filtertest.PathSchema()
	if err != nil {
		t.Fatalf("path schema: %v", err)
	}
	t.Run("paths", func(t *testing.T) {
		filtertest.RunPathSets(t, schema, evaluateOver(filtertest.PathRecords(), func(r filtertest.PathRecord) (string, filter.Row) {
			return r.ID, structRow(r)
		}))
	})
}

func TestEvaluateMembershipConformance(t *testing.T) {
	eval := evaluateOver(filtertest.Records(), func(r filtertest.Record) (string, filter.Row) {
		return r.ID, structRow(r)
	})
	filtertest.RunMembership(t, func(t *testing.T, f *filter.ParsedFilter) ([]string, error) {
		resolved, err := filter.ResolveMemberships(context.Background(), f, filtertest.Lookups())
		if err != nil {
			return nil, err
		}
		return eval(t, resolved)
	})
}

func TestEvaluatePathConformance(t *testing.T) {
	schema, err := filtertest.PathSchema()
	if err != nil {
		t.Fatalf("path schema: %v", err)
	}
	t.Run("stored documents", func(t *testing.T) {
		filtertest.RunPaths(t, schema, evaluateOver(filtertest.PathRecords(), func(r filtertest.PathRecord) (string, filter.Row) {
			return r.ID, structRow(r)
		}))
	})
	t.Run("decoded documents", func(t *testing.T) {
		filtertest.RunPaths(t, schema, evaluateOver(filtertest.PathRecords(), func(r filtertest.PathRecord) (string, filter.Row) {
			return r.ID, decodedRow(t, r)
		}))
	})
}

// evaluateOver is a filtertest.Backend selecting the rows whose filter
// Evaluate holds True.
func evaluateOver[R any](records []R, row func(R) (string, filter.Row)) filtertest.Backend {
	return func(t *testing.T, f *filter.ParsedFilter) ([]string, error) {
		var ids []string
		for _, r := range records {
			id, rw := row(r)
			truth, err := filter.Evaluate(f.AST.Root, rw)
			if err != nil {
				return nil, err
			}
			if truth == filter.True {
				ids = append(ids, id)
			}
		}
		return ids, nil
	}
}

// structRow reads a fixture record's columns from its fields, by db tag.
func structRow(record any) filter.ColumnRow {
	rv := reflect.ValueOf(record)
	columns := make(map[string]any, rv.NumField())
	for i := range rv.NumField() {
		if c := rv.Type().Field(i).Tag.Get("db"); c != "" {
			columns[c] = rv.Field(i).Interface()
		}
	}
	return byColumn(columns)
}

// byColumn reads columns by name; generated schemas qualify a column with
// its table (records.name).
func byColumn(columns map[string]any) filter.ColumnRow {
	return func(expr string) (any, bool) {
		if _, column, qualified := strings.Cut(expr, "."); qualified {
			expr = column
		}
		v, ok := columns[expr]
		return v, ok
	}
}

// decodedRow is structRow with every document column decoded to generic
// JSON, as a store holding decoded documents keeps them.
func decodedRow(t *testing.T, r filtertest.PathRecord) filter.ColumnRow {
	decode := func(v any) any {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("encode %T: %v", v, err)
		}
		if bytes.Equal(b, []byte("null")) {
			return nil
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		var doc any
		if err := dec.Decode(&doc); err != nil {
			t.Fatalf("decode %s: %v", b, err)
		}
		return doc
	}
	return byColumn(map[string]any{"id": r.ID, "attrs": decode(r.Attrs), "settings": decode(r.Settings)})
}
