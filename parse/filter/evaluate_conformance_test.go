//go:build unit

package filter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// The reference evaluator answers every conformance case the SQL dialects
// answer: over rows held as Go values, and over paths whose documents are
// held as JSON text, Go structs or decoded JSON.

func TestEvaluateConformance(t *testing.T) {
	filtertest.Run(t, evaluateOver(filtertest.Records(), func(r filtertest.Record) (string, filter.Row) {
		return r.ID, structRow(r)
	}))
}

func TestEvaluateSetConformance(t *testing.T) {
	filtertest.RunSets(t, evaluateOver(filtertest.Records(), func(r filtertest.Record) (string, filter.Row) {
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
	t.Run("paths of parsed documents", func(t *testing.T) {
		filtertest.RunPathSets(t, schema, evaluateOver(filtertest.PathRecords(), func(r filtertest.PathRecord) (string, filter.Row) {
			return r.ID, parsedRow(t, r)
		}))
	})
}

func TestEvaluateQuotedConformance(t *testing.T) {
	filtertest.RunQuoted(t, evaluateOver(filtertest.QuotedRecords(), func(r filtertest.QuotedRecord) (string, filter.Row) {
		return r.ID, structRow(r)
	}))
}

func TestEvaluateGeoConformance(t *testing.T) {
	t.Run("stored", func(t *testing.T) {
		filtertest.RunGeo(t, evaluateOver(filtertest.GeoRecords(), func(r filtertest.GeoRecord) (string, filter.Row) {
			return r.ID, structRow(r)
		}))
	})
	t.Run("parsed", func(t *testing.T) {
		filtertest.RunGeo(t, evaluateOver(filtertest.GeoRecords(), func(r filtertest.GeoRecord) (string, filter.Row) {
			return r.ID, byColumn(map[string]any{"id": r.ID, "loc": parsedDocument(t, r.Loc), "pos": parsedDocument(t, r.Pos)})
		}))
	})
}

func TestEvaluateDecimalConformance(t *testing.T) {
	schema, err := filtertest.DecimalSchema()
	if err != nil {
		t.Fatalf("decimal schema: %v", err)
	}
	t.Run("stored", func(t *testing.T) {
		filtertest.RunDecimals(t, schema, evaluateOver(filtertest.DecimalRecords(), func(r filtertest.DecimalRecord) (string, filter.Row) {
			return r.ID, structRow(r)
		}))
	})
	t.Run("parsed", func(t *testing.T) {
		filtertest.RunDecimals(t, schema, evaluateOver(filtertest.DecimalRecords(), func(r filtertest.DecimalRecord) (string, filter.Row) {
			return r.ID, byColumn(map[string]any{"id": r.ID, "amount": r.Amount, "doc": parsedDocument(t, r.Doc)})
		}))
	})
}

func TestEvaluateArrayConformance(t *testing.T) {
	schema, err := filtertest.ArraySchema()
	if err != nil {
		t.Fatalf("array schema: %v", err)
	}
	t.Run("stored", func(t *testing.T) {
		filtertest.RunArrays(t, schema, evaluateOver(filtertest.ArrayRecords(), func(r filtertest.ArrayRecord) (string, filter.Row) {
			return r.ID, structRow(r)
		}))
	})
	t.Run("decoded", func(t *testing.T) {
		filtertest.RunArrays(t, schema, evaluateOver(filtertest.ArrayRecords(), func(r filtertest.ArrayRecord) (string, filter.Row) {
			return r.ID, byColumn(map[string]any{"id": r.ID, "doc": decodedJSON(t, r.Doc),
				"labels": decodedJSON(t, r.Labels), "scores": decodedJSON(t, r.Scores), "lines": decodedJSON(t, r.Lines)})
		}))
	})
	t.Run("parsed", func(t *testing.T) {
		filtertest.RunArrays(t, schema, evaluateOver(filtertest.ArrayRecords(), func(r filtertest.ArrayRecord) (string, filter.Row) {
			return r.ID, byColumn(map[string]any{"id": r.ID, "doc": parsedDocument(t, r.Doc),
				"labels": r.Labels, "scores": r.Scores, "lines": parsedDocument(t, r.Lines)})
		}))
	})
}

// A backend with its own evaluator of `$some` / `$none`, following
// DocumentElements' contract, selects the rows Evaluate and the SQL
// dialects select: a scalar element, even a string holding an object's
// JSON text, has no members.
func TestDocumentElementsConformance(t *testing.T) {
	schema, err := filtertest.ArraySchema()
	require.NoError(t, err)
	records := filtertest.ArrayRecords()
	filtertest.RunArrays(t, schema, func(t *testing.T, f *filter.ParsedFilter) ([]string, error) {
		var ids []string
		for _, r := range records {
			row := structRow(r)
			truth, err := ownElements{stored: row}.eval(f.AST.Root, row)
			if err != nil {
				return nil, err
			}
			if truth == filter.True {
				ids = append(ids, r.ID)
			}
		}
		return ids, nil
	})
}

// ownElements evaluates `$some` / `$none` and the logic above them itself,
// as DocumentElements documents for a backend with its own evaluator, and
// hands every other condition to filter.Evaluate, as it does a malformed
// logical node, which Evaluate refuses. stored reads a column as the store
// holds it.
type ownElements struct{ stored filter.ColumnRow }

func (o ownElements) eval(n *filter.FilterNode, row filter.Row) (filter.Truth, error) {
	switch {
	case n.Op == filter.OpNot && len(n.Children) != 1,
		(n.Op == filter.OpAnd || n.Op == filter.OpOr) && len(n.Children) == 0:
		return filter.Evaluate(n, row)
	}
	switch n.Op {
	case filter.OpSome, filter.OpNone:
		elems, err := o.elements(n.Field, row)
		if err != nil {
			return filter.False, err
		}
		found := false
		for _, e := range elems {
			t := filter.True
			if len(n.Children) > 0 {
				if t, err = o.eval(n.Children[0], filter.ElementRow{Element: e}); err != nil {
					return filter.False, err
				}
			}
			if t == filter.True {
				found = true
				break
			}
		}
		return truthOf(found == (n.Op == filter.OpSome)), nil
	case filter.OpNot:
		t, err := o.eval(n.Children[0], row)
		if err != nil || t == filter.Unknown {
			return t, err
		}
		return truthOf(t == filter.False), nil
	case filter.OpAnd, filter.OpOr:
		decides, out := truthOf(n.Op == filter.OpOr), truthOf(n.Op == filter.OpAnd)
		for _, c := range n.Children {
			t, err := o.eval(c, row)
			if err != nil {
				return filter.False, err
			}
			if t == decides {
				return t, nil
			}
			if t == filter.Unknown {
				out = filter.Unknown
			}
		}
		return out, nil
	}
	return filter.Evaluate(n, row)
}

// elements are the elements a `$some` / `$none` on f tests: a JSON array
// document's decoded from its stored column by DocumentElements, a scalar
// array's as row reads them.
func (o ownElements) elements(f *filter.FieldSchema, row filter.Row) ([]any, error) {
	if f.Elements != nil {
		column := f.Table + "." + f.Column
		doc, ok := o.stored(column)
		if !ok {
			return nil, fmt.Errorf("field %s: no column %s", f.ExternalName, column)
		}
		return filter.DocumentElements(doc)
	}
	v, present, err := row.Value(f)
	if err != nil || !present {
		return nil, err
	}
	elems, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("field %s: elements are %T", f.ExternalName, v)
	}
	return elems, nil
}

func truthOf(b bool) filter.Truth {
	if b {
		return filter.True
	}
	return filter.False
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

func TestEvaluatePolicyConformance(t *testing.T) {
	schema, err := filtertest.Schema()
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	eval := evaluateOver(filtertest.Records(), func(r filtertest.Record) (string, filter.Row) {
		return r.ID, structRow(r)
	})
	filtertest.RunPolicy(t, false, func(t *testing.T, q filtertest.PolicyQuery) ([]string, error) {
		ast, err := filter.ApplyPolicyConstraints(schema, q.Filter, q.Policy)
		if err != nil {
			return nil, err
		}
		if ast == nil {
			ast = &filter.FilterAST{}
		}
		resolved, err := filter.ResolveMemberships(context.Background(), filter.NewParsedFilter(ast), filtertest.Lookups())
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
	t.Run("parsed documents", func(t *testing.T) {
		filtertest.RunPaths(t, schema, evaluateOver(filtertest.PathRecords(), func(r filtertest.PathRecord) (string, filter.Row) {
			return r.ID, parsedRow(t, r)
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
	return byColumn(map[string]any{"id": r.ID, "attrs": decodedJSON(t, r.Attrs), "settings": decodedJSON(t, r.Settings)})
}

// parsedRow is structRow with every document column a filter.Document, as
// a store that decodes each document once keeps them.
func parsedRow(t *testing.T, r filtertest.PathRecord) filter.ColumnRow {
	return byColumn(map[string]any{"id": r.ID, "attrs": parsedDocument(t, r.Attrs), "settings": parsedDocument(t, r.Settings)})
}

// parsedDocument is v's JSON as a filter.Document, nil (SQL NULL) for null.
func parsedDocument(t *testing.T, v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode %T: %v", v, err)
	}
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	doc, err := filter.DecodeDocument(b)
	if err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return doc
}

// decodedJSON is v as generic JSON (numbers as json.Number), nil for null.
func decodedJSON(t *testing.T, v any) any {
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
