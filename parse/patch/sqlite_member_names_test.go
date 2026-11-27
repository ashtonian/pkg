//go:build unit

package patch_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/patch"
)

// TestBuildPatchSQL_SQLiteWritesAQuoteOrBackslashInAMember: a JSON Patch
// whose path names a member holding a double quote or a backslash writes
// it on SQLite as json-patch does. The path quotes the label with its
// escapes, which every SQLite parsesql.New opens a provider on reads as
// the member (dialect.SQLiteMinVersion).
func TestBuildPatchSQL_SQLiteWritesAQuoteOrBackslashInAMember(t *testing.T) {
	db := dbtest.SQLite(t)
	_, err := db.ExecContext(t.Context(), `CREATE TABLE docs (id INTEGER PRIMARY KEY, doc TEXT)`)
	require.NoError(t, err)
	parser := patch.NewParser(memberSchema(""), nil)
	for _, name := range []string{`x"y`, `x\y`, `"`, `\`, `\"`, `x".y`} {
		start, err := json.Marshal(map[string]any{"ok": map[string]any{}, name: map[string]any{"z0": 0}})
		require.NoError(t, err)
		for _, tc := range []struct {
			what string
			op   map[string]any
		}{
			{"added", map[string]any{"op": "add", "path": core.FormatPointer("doc", name+"+"), "value": 1}},
			{"replaced", map[string]any{"op": "replace", "path": core.FormatPointer("doc", name), "value": 1}},
			{"removed", map[string]any{"op": "remove", "path": core.FormatPointer("doc", name)}},
			{"added below", map[string]any{"op": "add", "path": core.FormatPointer("doc", "ok", name), "value": 1}},
			{"added inside", map[string]any{"op": "add", "path": core.FormatPointer("doc", name, "z"), "value": 1}},
		} {
			t.Run(tc.what+" "+name, func(t *testing.T) {
				body, err := json.Marshal([]map[string]any{tc.op})
				require.NoError(t, err)
				want, err := applyRFC6902(t, string(start), string(body))
				require.NoError(t, err)
				parsed, err := parser.Parse(body)
				require.NoError(t, err)
				res, err := patch.BuildPatchSQL(parsed, filter.SQLiteDialect{})
				require.NoError(t, err)

				_, err = db.ExecContext(t.Context(), `DELETE FROM docs`)
				require.NoError(t, err)
				_, err = db.ExecContext(t.Context(), `INSERT INTO docs (id, doc) VALUES (1, ?1)`, string(start))
				require.NoError(t, err)
				where := "id = " + filter.SQLiteDialect{}.Placeholder(res.ParamCount+1)
				if res.Precondition != "" {
					where += " AND " + res.Precondition
				}
				updated, err := db.ExecContext(t.Context(), "UPDATE docs SET "+res.SetClause+" WHERE "+where, append(res.Args, 1)...)
				require.NoError(t, err)
				n, err := updated.RowsAffected()
				require.NoError(t, err)
				require.Equal(t, int64(1), n, "the precondition holds")
				var got string
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT doc FROM docs WHERE id = 1`).Scan(&got))
				assert.Equal(t, want, decodeDoc(t, got))
			})
		}
	}
}
