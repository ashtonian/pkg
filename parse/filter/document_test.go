//go:build unit

package filter_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// Every reader of a document reads a Document as it reads the document's
// JSON text.
func TestDocument_ReadsAsItsJSON(t *testing.T) {
	schema, err := filtertest.PathSchema()
	require.NoError(t, err)
	for _, r := range filtertest.PathRecords() {
		doc, err := filter.DecodeDocument(r.Attrs)
		require.NoError(t, err, r.ID)
		for name, f := range schema.Fields {
			if f.Path == nil || f.Path.Column != "attrs" {
				continue
			}
			want, wantPresent, wantErr := filter.PathValue(r.Attrs, f)
			got, gotPresent, gotErr := filter.PathValue(doc, f)
			assert.Equal(t, wantErr, gotErr, "%s %s", r.ID, name)
			assert.Equal(t, want, got, "%s %s", r.ID, name)
			assert.Equal(t, wantPresent, gotPresent, "%s %s", r.ID, name)

			key := filter.CursorKey{Name: name, DataType: f.DataType, NumberKind: f.NumberKind, Path: f.Path}
			wantKey, wantErr := filter.KeyValue(key, r.Attrs)
			gotKey, gotErr := filter.KeyValue(key, &doc)
			assert.Equal(t, wantErr, gotErr, "%s key %s", r.ID, name)
			assert.Equal(t, wantKey, gotKey, "%s key %s", r.ID, name)
		}
	}
	for _, r := range filtertest.ArrayRecords() {
		doc, err := filter.DecodeDocument(r.Lines)
		require.NoError(t, err, r.ID)
		want, err := filter.DocumentElements(r.Lines)
		require.NoError(t, err, r.ID)
		got, err := filter.DocumentElements(doc)
		require.NoError(t, err, r.ID)
		assert.Equal(t, want, got, r.ID)
	}
}

func TestDecodeDocument(t *testing.T) {
	doc, err := filter.DecodeDocument([]byte(`{"n": 1.50, "tags": ["a"]}`))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"n": json.Number("1.50"), "tags": []any{"a"}}, doc.Root())

	for _, text := range []string{"", " ", "null"} {
		doc, err := filter.DecodeDocument([]byte(text))
		require.NoError(t, err, text)
		assert.Nil(t, doc.Root(), "%q is the JSON null document", text)
	}

	_, err = filter.DecodeDocument([]byte(`{"n":`))
	assert.Error(t, err)

	elems, err := filter.DocumentElements((*filter.Document)(nil))
	require.NoError(t, err)
	assert.Nil(t, elems, "a nil *Document is NULL")
}

// Evaluating a filter over documents held as JSON text decodes each one per
// path condition; a Document is decoded once.
func BenchmarkEvaluate_PathDocuments(b *testing.B) {
	schema, err := filtertest.PathSchema()
	require.NoError(b, err)
	parsed, err := filter.NewFilterParser(schema, nil).Parse(`{"attrs.color":{"$in":["red","blue"]},"attrs.size":{"$gte":2},"attrs.price":{"$lt":"100"}}`)
	require.NoError(b, err)
	records := filtertest.PathRecords()
	rows := func(doc func(json.RawMessage) any) []filter.Row {
		out := make([]filter.Row, len(records))
		for i, r := range records {
			attrs := doc(r.Attrs)
			out[i] = filter.ColumnRow(func(string) (any, bool) { return attrs, true })
		}
		return out
	}
	for name, rs := range map[string][]filter.Row{
		"json text": rows(func(raw json.RawMessage) any { return raw }),
		"document": rows(func(raw json.RawMessage) any {
			d, err := filter.DecodeDocument(raw)
			require.NoError(b, err)
			return &d
		}),
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for _, row := range rs {
					if _, err := filter.Evaluate(parsed.Root, row); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
