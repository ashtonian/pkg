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

// TestPathLabels_PatchWritesWhatAFilterReads writes a member named label
// through a patch, which binds its path, and reads it back through a
// filter, which inlines it, on each dialect. Each label holds what a JSON
// path label must escape somewhere: a single quote in an SQL string
// literal, and on PostgreSQL a double quote and a backslash, which SQLite
// refuses to write (sqliteMemberPath). A filter's declared members are
// names alone (filter.MaxPathMemberLen), so the path is built by hand:
// each label renders safely whatever it holds.
func TestPathLabels_PatchWritesWhatAFilterReads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		open    func(testing.TB) *sql.DB
		d       filter.Dialect
		storage core.SQLType
		path    filter.JSONStorage
		schema  string
		label   string
	}{
		{"sqlite", dbtest.SQLite, filter.SQLiteDialect{}, core.SQLJSON, filter.StorageText, `CREATE TABLE docs (id INTEGER PRIMARY KEY, doc TEXT)`, `it's a.b[0]`},
		{"postgres", dbtest.Postgres, filter.PostgresDialect{}, core.SQLJSONB, filter.StorageJSONB, `CREATE TABLE docs (id BIGINT PRIMARY KEY, doc JSONB)`, `q"u\o'te`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := tc.open(t)
			_, err := db.ExecContext(t.Context(), tc.schema)
			require.NoError(t, err)
			_, err = db.ExecContext(t.Context(), `INSERT INTO docs (id, doc) VALUES (1, '{"other":1}'), (2, '{"other":2}')`)
			require.NoError(t, err)

			body, err := json.Marshal([]map[string]any{{"op": "add", "path": core.FormatPointer("doc", tc.label), "value": "x"}})
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
				Path: &filter.JSONPath{Table: "docs", Column: "doc", Segments: []string{tc.label}, Storage: tc.path}}
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
