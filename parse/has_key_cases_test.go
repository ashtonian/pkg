//go:build unit || integration

package parse_test

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// hasKeyDocs are the shapes `$has_key` distinguishes, with jsonb `?`'s
// answers for the keys "color" and "x" as PostgreSQL 18 gives them; a NULL
// column (nil) is NULL.
var hasKeyDocs = []struct {
	doc          any
	color, other *bool
}{
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

type hasKeyDoc struct {
	ID  string          `json:"id"  db:"id"  parse:"filter"`
	Doc json.RawMessage `json:"doc" db:"doc" parse:"filter"`
}

// runHasKey checks `$has_key` against every shape on db, and the reference
// JSONHasKey against the same table.
func runHasKey(t *testing.T, db *sql.DB, d filter.Dialect, docType, cast string) {
	t.Helper()
	_, err := db.Exec("CREATE TABLE has_key_docs (id TEXT PRIMARY KEY, doc " + docType + ")")
	require.NoError(t, err)
	ph := d.Placeholder(2)
	if cast != "" {
		ph += cast
	}
	for i, c := range hasKeyDocs {
		_, insertErr := db.Exec("INSERT INTO has_key_docs (id, doc) VALUES ("+d.Placeholder(1)+", "+ph+")", strconv.Itoa(i), c.doc)
		require.NoError(t, insertErr)
	}
	schema, err := filter.GenerateFilterSchema[hasKeyDoc](core.NewSchemaGenerator())
	require.NoError(t, err)
	for _, key := range []string{"color", "x"} {
		ast, err := filter.NewFilterParser(schema, nil).Parse(`{"doc":{"$has_key":"` + key + `"}}`)
		require.NoError(t, err)
		res, err := filter.NewSQLBuilder(schema, d).Build(ast)
		require.NoError(t, err)
		rows, err := db.Query("SELECT id, ("+res.Where+") FROM has_key_docs ORDER BY id", res.Args...)
		require.NoError(t, err, res.Where)
		for rows.Next() {
			var id string
			var got sql.NullBool
			require.NoError(t, rows.Scan(&id, &got))
			i, _ := strconv.Atoi(id)
			c := hasKeyDocs[i]
			want := c.color
			if key == "x" {
				want = c.other
			}
			if want == nil {
				assert.False(t, got.Valid, "doc %v key %s: a NULL document is NULL", c.doc, key)
				continue
			}
			assert.True(t, got.Valid && got.Bool == *want, "doc %v key %s: SQL %v, want %v", c.doc, key, got, *want)
			var decoded any
			doc, ok := c.doc.(string)
			require.True(t, ok)
			require.NoError(t, json.Unmarshal([]byte(doc), &decoded))
			assert.Equal(t, *want, filter.JSONHasKey(decoded, key), "reference: doc %v key %s", c.doc, key)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	}
}
