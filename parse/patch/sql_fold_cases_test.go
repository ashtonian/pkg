//go:build unit || integration

package patch_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// foldSchema models a row with an array column, a scalar column and a
// server-managed timestamp.
func foldSchema() *patch.PatchSchema {
	return &patch.PatchSchema{
		Resource:      "items",
		MaxOperations: 50,
		AllowedPaths: map[string]*patch.PathSchema{
			"/tags": {
				AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpRemove, patch.PatchOpArrayAppend},
				DataType:   core.FieldTypeArray, SQLColumn: "tags",
			},
			"/name": {
				AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove, patch.PatchOpMove, patch.PatchOpCopy, patch.PatchOpTest},
				DataType:   core.FieldTypeString, SQLColumn: "name",
			},
			"/updated_at": {
				AllowedOps: []patch.PatchOp{patch.PatchOpReplace},
				DataType:   core.FieldTypeTimestamp, SQLColumn: "updated_at",
			},
			"/meta/*": {
				AllowedOps: []patch.PatchOp{patch.PatchOpReplace},
				DataType:   core.FieldTypeString, SQLColumn: "meta",
			},
		},
	}
}

type foldCase struct {
	name     string
	ops      string
	wantTags []string // nil means the column is NULL
	wantName string
	wantArgs int // bound values, excluding the WHERE id
}

// Every case starts from tags=["zero"], name="orig".
var foldCases = []foldCase{
	{name: "two appends keep both", ops: `[{"op":"array_append","path":"/tags","value":"one"},{"op":"array_append","path":"/tags","value":"two"}]`,
		wantTags: []string{"zero", "one", "two"}, wantName: "orig", wantArgs: 2},
	{name: "replace then append", ops: `[{"op":"replace","path":"/tags","value":["a"]},{"op":"array_append","path":"/tags","value":"b"}]`,
		wantTags: []string{"a", "b"}, wantName: "orig", wantArgs: 1},
	{name: "replace then appends bind one array", ops: `[{"op":"replace","path":"/tags","value":["a"]},{"op":"array_append","path":"/tags","value":"b"},{"op":"array_append","path":"/tags","value":"c"}]`,
		wantTags: []string{"a", "b", "c"}, wantName: "orig", wantArgs: 1},
	{name: "append then replace discards the append", ops: `[{"op":"array_append","path":"/tags","value":"x"},{"op":"replace","path":"/tags","value":["a"]}]`,
		wantTags: []string{"a"}, wantName: "orig", wantArgs: 1},
	{name: "remove then append starts empty", ops: `[{"op":"remove","path":"/tags"},{"op":"array_append","path":"/tags","value":"b"}]`,
		wantTags: []string{"b"}, wantName: "orig", wantArgs: 1},
	{name: "append then remove", ops: `[{"op":"array_append","path":"/tags","value":"b"},{"op":"remove","path":"/tags"}]`,
		wantTags: nil, wantName: "orig", wantArgs: 0},
	{name: "last scalar replace wins", ops: `[{"op":"replace","path":"/name","value":"x"},{"op":"replace","path":"/name","value":"y"}]`,
		wantTags: []string{"zero"}, wantName: "y", wantArgs: 1},
	{name: "interleaved columns", ops: `[{"op":"array_append","path":"/tags","value":"t1"},{"op":"replace","path":"/name","value":"n"},{"op":"array_append","path":"/tags","value":"t2"}]`,
		wantTags: []string{"zero", "t1", "t2"}, wantName: "n", wantArgs: 3},
}

// foldDB describes one database under test.
type foldDB struct {
	db       *sql.DB
	dialect  patch.SQLDialect
	schema   string // CREATE TABLE statement
	seed     string // INSERT for the starting row
	tagsExpr string // SELECT expression yielding tags as JSON text (NULL when NULL)
}

func runFoldCases(t *testing.T, fdb foldDB) {
	t.Helper()
	parser := patch.NewParser(foldSchema(), nil)
	for _, tc := range foldCases {
		t.Run(tc.name, func(t *testing.T) {
			resetFoldTable(t, fdb)
			parsed, err := parser.Parse([]byte(tc.ops))
			require.NoError(t, err)
			res, err := patch.BuildPatchSQL(parsed, fdb.dialect)
			require.NoError(t, err)
			assert.Len(t, res.Args, tc.wantArgs, "SET %s", res.SetClause)

			stmt := fmt.Sprintf("UPDATE items SET %s WHERE id = %s", res.SetClause, fdb.dialect.Placeholder(res.ParamCount+1))
			_, err = fdb.db.Exec(stmt, append(res.Args, 1)...)
			require.NoError(t, err, stmt)

			tags, name := readFoldRow(t, fdb)
			assert.Equal(t, tc.wantTags, tags)
			assert.Equal(t, tc.wantName, name)
		})
	}

	t.Run("patched auto-timestamp column assigned once", func(t *testing.T) {
		resetFoldTable(t, fdb)
		parsed, err := parser.Parse([]byte(`[{"op":"replace","path":"/updated_at","value":"2001-01-01T00:00:00Z"},{"op":"replace","path":"/name","value":"n"}]`))
		require.NoError(t, err)
		fixed := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
		res, err := patch.BuildPatchSQL(parsed, fdb.dialect,
			patch.WithAutoTimestamp("updated_at"), patch.WithTimeFunc(func() time.Time { return fixed }))
		require.NoError(t, err)
		assert.Equal(t, 1, strings.Count(res.SetClause, "updated_at ="), res.SetClause)
		// PostgreSQL rejects a repeated column assignment outright.
		stmt := fmt.Sprintf("UPDATE items SET %s WHERE id = %s", res.SetClause, fdb.dialect.Placeholder(res.ParamCount+1))
		_, err = fdb.db.Exec(stmt, append(res.Args, 1)...)
		require.NoError(t, err, stmt)
	})

	t.Run("auto-timestamp from the database's clock", func(t *testing.T) {
		resetFoldTable(t, fdb)
		parsed, err := parser.Parse([]byte(`[{"op":"replace","path":"/updated_at","value":"2001-01-01T00:00:00Z"},{"op":"replace","path":"/name","value":"n"}]`))
		require.NoError(t, err)
		res, err := patch.BuildPatchSQL(parsed, fdb.dialect, patch.WithAutoTimestampSQL("updated_at", "CURRENT_TIMESTAMP"))
		require.NoError(t, err)
		assert.Contains(t, res.SetClause, "updated_at = CURRENT_TIMESTAMP")
		assert.Equal(t, 1, strings.Count(res.SetClause, "updated_at ="), res.SetClause)
		assert.Len(t, res.Args, 1, "only the name is bound")
		stmt := fmt.Sprintf("UPDATE items SET %s WHERE id = %s", res.SetClause, fdb.dialect.Placeholder(res.ParamCount+1))
		_, err = fdb.db.Exec(stmt, append(res.Args, 1)...)
		require.NoError(t, err, stmt)
	})
}

func resetFoldTable(t *testing.T, fdb foldDB) {
	t.Helper()
	_, err := fdb.db.Exec("DROP TABLE IF EXISTS items")
	require.NoError(t, err)
	_, err = fdb.db.Exec(fdb.schema)
	require.NoError(t, err)
	_, err = fdb.db.Exec(fdb.seed)
	require.NoError(t, err)
}

func readFoldRow(t *testing.T, fdb foldDB) ([]string, string) {
	t.Helper()
	var tagsJSON sql.NullString
	var name string
	require.NoError(t, fdb.db.QueryRow("SELECT "+fdb.tagsExpr+", name FROM items WHERE id = 1").Scan(&tagsJSON, &name))
	if !tagsJSON.Valid {
		return nil, name
	}
	var tags []string
	require.NoError(t, json.Unmarshal([]byte(tagsJSON.String), &tags))
	return tags, name
}

func TestBuildPatchSQL_RejectsUncompilableOps(t *testing.T) {
	parser := patch.NewParser(foldSchema(), nil)
	cases := map[string]struct {
		ops  string
		code string
	}{
		"move":        {ops: `[{"op":"move","from":"/name","path":"/name"}]`, code: patch.ECodeOpNotSQLCompilable.String()},
		"copy":        {ops: `[{"op":"copy","from":"/name","path":"/name"}]`, code: patch.ECodeOpNotSQLCompilable.String()},
		"nested path": {ops: `[{"op":"replace","path":"/meta/color","value":"red"}]`, code: patch.ECodeOpNotSQLCompilable.String()},
		"test":        {ops: `[{"op":"test","path":"/name","value":"orig"}]`, code: patch.ECodeTestOpSQL.String()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			parsed, err := parser.Parse([]byte(tc.ops))
			require.NoError(t, err)
			res, err := patch.BuildPatchSQL(parsed, filter.SQLiteDialect{})
			require.Error(t, err)
			assert.Nil(t, res, "a rejected op must not yield a result a caller could treat as an empty patch")
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, tc.code, pe.Code)
		})
	}
}
