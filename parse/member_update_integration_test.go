//go:build integration

package parse_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestUpdate_Members_SQLite(t *testing.T) {
	runMemberUpdates(t, func(t *testing.T) *sql.DB { return dbtest.SQLite(t) }, filter.SQLiteDialect{}, "TEXT")
}

func TestUpdate_Members_Postgres(t *testing.T) {
	runMemberUpdates(t, func(t *testing.T) *sql.DB { return dbtest.Postgres(t) }, filter.PostgresDialect{}, "JSONB")
}

// runMemberUpdates updates gadgets' documents member by member through
// BuildUpdate and the dialect's executor: the rows equal what
// ApplyValidatedPatch makes of them, and a patch a target row's document
// does not take fails as ApplyValidatedPatch fails it, writing no row.
func runMemberUpdates(t *testing.T, open func(*testing.T) *sql.DB, d parse.Dialect, docType string) {
	r := gadgetAttrsResource(t)
	rows := []gadgetAttrs{
		{ID: 1, Attrs: map[string]any{"color": "blue", "old": true, "n": map[string]any{"k": "v"}}},
		{ID: 2, Attrs: map[string]any{"color": "green"}},
	}
	setup := func(t *testing.T) *sql.DB {
		t.Helper()
		db := open(t)
		_, err := db.ExecContext(t.Context(), `CREATE TABLE gadget_attrs (id BIGINT PRIMARY KEY, attrs `+docType+` NOT NULL)`)
		require.NoError(t, err)
		insert := `INSERT INTO gadget_attrs VALUES (?, ?)`
		if isPostgres(d) {
			insert = `INSERT INTO gadget_attrs VALUES ($1, $2)`
		}
		for _, row := range rows {
			raw, err := json.Marshal(row.Attrs)
			require.NoError(t, err)
			_, err = db.ExecContext(t.Context(), insert, row.ID, string(raw))
			require.NoError(t, err)
		}
		return db
	}
	stored := func(t *testing.T, db *sql.DB) []gadgetAttrs {
		t.Helper()
		req, err := r.ParseList(nil)
		require.NoError(t, err)
		plan, err := r.BuildList(d, req, parse.Scope{})
		require.NoError(t, err)
		return listPage(t, db, r, d, plan).Items
	}
	jsonOf := func(t *testing.T, v any) string {
		t.Helper()
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		return string(raw)
	}

	t.Run("members written in one statement", func(t *testing.T) {
		db := setup(t)
		parsed, err := r.ParsePatch([]byte(`[{"op":"replace","path":"/attrs/color","value":"red"},{"op":"add","path":"/attrs/n/size","value":{"w":2}},{"op":"remove","path":"/attrs/old"}]`))
		require.NoError(t, err)
		plan, err := r.BuildUpdate(d, parsed, parse.ByID(int64(1)), parse.Scope{})
		require.NoError(t, err)
		res, err := applyUpdate(t, db, r, d, plan, false)
		require.NoError(t, err)
		require.Len(t, res.Changed, 1)
		want, err := r.ApplyValidatedPatch(parsed, rows[0])
		require.NoError(t, err)
		assert.JSONEq(t, jsonOf(t, want), jsonOf(t, res.Changed[0]))
		assert.JSONEq(t, jsonOf(t, []gadgetAttrs{want, rows[1]}), jsonOf(t, stored(t, db)))
	})

	for _, tc := range []struct {
		name    string
		ops     string
		target  parse.Target
		failing int // the index in rows of the target row the patch fails on
		unmet   int
	}{
		{"the operation a row fails is the update's error", `[{"op":"replace","path":"/attrs/color","value":"x"},{"op":"remove","path":"/attrs/zz"}]`, parse.ByID(int64(1)), 0, 1},
		{"one target row of two fails, and neither is written", `[{"op":"remove","path":"/attrs/old"}]`, parse.ByIDs(int64(1), int64(2)), 1, 0},
		{"an add below a member that is not an object", `[{"op":"add","path":"/attrs/color/x","value":1}]`, parse.ByID(int64(2)), 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setup(t)
			parsed, err := r.ParsePatch([]byte(tc.ops))
			require.NoError(t, err)
			plan, err := r.BuildUpdate(d, parsed, tc.target, parse.Scope{})
			require.NoError(t, err)
			_, err = applyUpdate(t, db, r, d, plan, false)
			pe, ok := errors.AsType[*core.ParseError](err)
			require.True(t, ok, "%v is no ParseError", err)
			assert.Equal(t, core.CodePatchValueInvalid.String(), pe.Code)
			require.NotNil(t, pe.Index)
			assert.Equal(t, tc.unmet, *pe.Index)
			assert.JSONEq(t, jsonOf(t, rows), jsonOf(t, stored(t, db)), "no row is written")

			_, err = r.ApplyValidatedPatch(parsed, rows[tc.failing])
			memory, ok := errors.AsType[*core.ParseError](err)
			require.True(t, ok, "ApplyValidatedPatch: %v is no ParseError", err)
			assert.Equal(t, pe.Code, memory.Code)
			assert.Equal(t, *pe.Index, *memory.Index)
		})
	}

	t.Run("a member set to the value it holds is unchanged", func(t *testing.T) {
		db := setup(t)
		parsed, err := r.ParsePatch([]byte(`[{"op":"replace","path":"/attrs/color","value":"blue"}]`))
		require.NoError(t, err)
		plan, err := r.BuildUpdate(d, parsed, parse.ByID(int64(1)), parse.Scope{}, parse.SkipUnchanged())
		require.NoError(t, err)
		res, err := applyUpdate(t, db, r, d, plan, false)
		require.NoError(t, err)
		assert.Empty(t, res.Changed)
		require.Len(t, res.Unchanged, 1)
		assert.JSONEq(t, jsonOf(t, rows[0]), jsonOf(t, res.Unchanged[0]))
	})
}
