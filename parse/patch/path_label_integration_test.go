//go:build integration

package patch_test

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/patch"
)

// awkwardLabel holds what a JSON path label must escape somewhere: a
// double quote and a backslash in SQLite's quoted labels, a single quote
// in an SQL string literal, and a backslash in PostgreSQL's escape strings.
const awkwardLabel = `q"u\o'te`

// TestPathLabels_PatchWritesWhatAFilterReads writes a member named
// awkwardLabel through a patch, which binds its path, and reads it back
// through a filter, which inlines it, on each dialect. A filter's declared
// members are names alone (filter.MaxPathMemberLen), so the path is built
// by hand: each label renders safely whatever it holds.
func TestPathLabels_PatchWritesWhatAFilterReads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		open    func(testing.TB) *sql.DB
		d       filter.Dialect
		storage core.SQLType
		path    filter.JSONStorage
		schema  string
	}{
		{"sqlite", dbtest.SQLite, filter.SQLiteDialect{}, core.SQLJSON, filter.StorageText, `CREATE TABLE docs (id INTEGER PRIMARY KEY, doc TEXT)`},
		{"postgres", dbtest.Postgres, filter.PostgresDialect{}, core.SQLJSONB, filter.StorageJSONB, `CREATE TABLE docs (id BIGINT PRIMARY KEY, doc JSONB)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := tc.open(t)
			_, err := db.ExecContext(t.Context(), tc.schema)
			require.NoError(t, err)
			_, err = db.ExecContext(t.Context(), `INSERT INTO docs (id, doc) VALUES (1, '{"other":1}'), (2, '{"other":2}')`)
			require.NoError(t, err)

			body, err := json.Marshal([]map[string]any{{"op": "add", "path": core.FormatPointer("doc", awkwardLabel), "value": "x"}})
			require.NoError(t, err)
			parsed, err := patch.NewParser(memberSchema(tc.storage), nil).Parse(body)
			require.NoError(t, err)
			set, err := patch.BuildPatchSQL(parsed, tc.d)
			require.NoError(t, err)
			update := "UPDATE docs SET " + set.SetClause + " WHERE id = 1"
			if set.Precondition != "" {
				update += " AND " + set.Precondition
			}
			_, err = db.ExecContext(t.Context(), update, set.Args...)
			require.NoError(t, err)

			field := &filter.FieldSchema{ExternalName: "member", DataType: core.FieldTypeString,
				Path: &filter.JSONPath{Table: "docs", Column: "doc", Segments: []string{awkwardLabel}, Storage: tc.path}}
			var args []any
			cond, err := tc.d.Condition(field, filter.OpEq, "x", func(v any) string {
				args = append(args, v)
				return tc.d.Placeholder(len(args))
			})
			require.NoError(t, err)
			rows, err := db.QueryContext(t.Context(), "SELECT id FROM docs WHERE "+cond, args...)
			require.NoError(t, err)
			defer rows.Close()
			var ids []int
			for rows.Next() {
				var id int
				require.NoError(t, rows.Scan(&id))
				ids = append(ids, id)
			}
			require.NoError(t, rows.Err())
			assert.Equal(t, []int{1}, ids)
		})
	}
}
