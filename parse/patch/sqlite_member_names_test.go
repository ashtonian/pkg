//go:build unit

package patch_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dialect"
	"github.com/ashtonian/pkg/parse/patch"
)

// TestBuildPatchSQL_SQLiteRefusesAQuoteOrBackslashInAMember: a SQLite
// before dialect.SQLiteMinVersion reads a quoted path label's escapes
// otherwise, and a statement cannot tell which release runs it, so SQLite
// refuses a JSON Patch operation whose path writes or tests a member named
// with either, as E2070 naming the path. PostgreSQL binds the names as
// text and writes them; a merge patch binds them inside its JSON, and so
// does SQLite.
func TestBuildPatchSQL_SQLiteRefusesAQuoteOrBackslashInAMember(t *testing.T) {
	build := func(t *testing.T, d patch.SQLDialect, ops []map[string]any) error {
		t.Helper()
		body, err := json.Marshal(ops)
		require.NoError(t, err)
		parsed, err := patch.NewParser(memberSchema(core.SQLJSON), nil).Parse(body)
		require.NoError(t, err)
		_, err = patch.BuildPatchSQL(parsed, d, patch.WithChangePredicate())
		return err
	}
	for _, name := range []string{`x"y`, `x\y`, `"`, `\`, `\"`} {
		for _, tc := range []struct {
			what string
			op   map[string]any
		}{
			{"added", map[string]any{"op": "add", "path": core.FormatPointer("doc", name), "value": 1}},
			{"replaced", map[string]any{"op": "replace", "path": core.FormatPointer("doc", name), "value": 1}},
			{"removed", map[string]any{"op": "remove", "path": core.FormatPointer("doc", name)}},
			{"added below", map[string]any{"op": "add", "path": core.FormatPointer("doc", "ok", name), "value": 1}},
			{"the parent of one added", map[string]any{"op": "add", "path": core.FormatPointer("doc", name, "z"), "value": 1}},
		} {
			t.Run(tc.what+" "+name, func(t *testing.T) {
				err := build(t, filter.SQLiteDialect{}, []map[string]any{tc.op})
				require.ErrorIs(t, err, core.CodeOpNotSQLCompilable)
				var pe *core.ParseError
				require.ErrorAs(t, err, &pe)
				assert.Equal(t, tc.op["path"], pe.Path)
				assert.Contains(t, pe.Message, dialect.SQLiteMinVersion)
				assert.Contains(t, pe.Message, "apply via ApplyValidatedPatch")

				require.NoError(t, build(t, filter.PostgresDialect{}, []map[string]any{tc.op}))
			})
		}
		t.Run("merged "+name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"doc": map[string]any{name: 1}})
			require.NoError(t, err)
			parsed, err := patch.ParseMergePatch(body, memberSchema(core.SQLJSON), nil)
			require.NoError(t, err)
			_, err = patch.BuildPatchSQL(parsed, filter.SQLiteDialect{}, patch.WithChangePredicate())
			require.NoError(t, err)
		})
	}
	t.Run("other punctuation", func(t *testing.T) {
		err := build(t, filter.SQLiteDialect{}, []map[string]any{
			{"op": "add", "path": core.FormatPointer("doc", `it's a.b[0]$<&>/~`), "value": 1},
		})
		require.NoError(t, err)
	})
}
