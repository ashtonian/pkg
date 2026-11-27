//go:build integration

package parse_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

type hasKeyCase struct {
	doc          any
	color, other *bool
}

// hasKeyDocs are the shapes `$has_key` distinguishes, with jsonb `?`'s
// answers for the keys "color" and "x" as PostgreSQL 18 gives them; a NULL
// column (nil) is NULL.
func hasKeyDocs() []hasKeyCase {
	docs := []hasKeyCase{
		{`{"color":1}`, new(true), new(false)},
		{`{"color":null}`, new(true), new(false)},
		{`["color",1]`, new(true), new(false)},
		{`[["color"]]`, new(false), new(false)},
		{`"color"`, new(true), new(false)},
		{`"x"`, new(false), new(true)},
		{`3`, new(false), new(false)},
		{`null`, new(false), new(false)},
		{`{"a":{"color":1}}`, new(false), new(false)},
		{`{}`, new(false), new(false)},
		{`[]`, new(false), new(false)},
		{nil, nil, nil},
	}
	return docs
}

type hasKeyDoc struct {
	ID  string          `json:"id"  db:"id"  parse:"filter"`
	Doc json.RawMessage `json:"doc" db:"doc" parse:"filter"`
}

// `$has_key` on each backend against every shape, on SQLite's JSON text as
// on PostgreSQL's jsonb, and the reference JSONHasKey against the same
// documents.
func TestHasKey(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		db := b.open(t)
		table := sqlTable{name: "has_key_docs", columns: []sqlColumn{column[string]("id"), column[json.RawMessage]("doc")}, key: []string{"id"}}
		docs := hasKeyDocs()
		rows := make([][]any, len(docs))
		for i, c := range docs {
			rows[i] = []any{fmt.Sprintf("%02d", i), c.doc}
		}
		b.load(t, db, table, rows)
		schema, err := filter.GenerateFilterSchema[hasKeyDoc](core.NewSchemaGenerator())
		require.NoError(t, err)
		for _, key := range []string{"color", "x"} {
			ast, err := filter.NewFilterParser(schema, nil).Parse(`{"doc":{"$has_key":"` + key + `"}}`)
			require.NoError(t, err)
			res, err := filter.NewSQLBuilder(schema, b.dialect).Build(ast)
			require.NoError(t, err)
			got := queryRows[sql.NullBool](t, db, "SELECT ("+res.Where+") FROM has_key_docs ORDER BY id", res.Args...)
			require.Len(t, got, len(docs))
			for i, c := range docs {
				want := c.color
				if key == "x" {
					want = c.other
				}
				if want == nil {
					assert.False(t, got[i].Valid, "doc %v key %s: a NULL document is NULL", c.doc, key)
					continue
				}
				assert.True(t, got[i].Valid && got[i].Bool == *want, "doc %v key %s: SQL %v, want %v", c.doc, key, got[i], *want)
				var decoded any
				doc, ok := c.doc.(string)
				require.True(t, ok)
				require.NoError(t, json.Unmarshal([]byte(doc), &decoded))
				assert.Equal(t, *want, filter.JSONHasKey(decoded, key), "reference: doc %v key %s", c.doc, key)
			}
		}
	})
}
