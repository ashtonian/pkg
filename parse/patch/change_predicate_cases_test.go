//go:build unit || integration

package patch_test

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

// changeSchema models a row of every kind of column a change predicate
// compares. name and email are case-insensitive columns, mood an enum and
// span an interval on PostgreSQL, raw a declared json (not jsonb) column
// there; price pads to a scale, amount does not.
func changeSchema() *patch.PatchSchema {
	ops := []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpRemove}
	path := func(t core.FieldType, column string) *patch.PathSchema {
		return &patch.PathSchema{AllowedOps: ops, DataType: t, SQLColumn: column}
	}
	tags := path(core.FieldTypeArray, "tags")
	tags.AllowedOps = append(tags.AllowedOps, patch.PatchOpArrayAppend)
	tags.GoType = reflect.TypeFor[[]string]()
	raw := path(core.FieldTypeJSON, "raw")
	raw.SQLType = core.SQLJSON
	return &patch.PatchSchema{
		Resource:      "things",
		MaxOperations: 50,
		AllowedPaths: map[string]*patch.PathSchema{
			"/name":       path(core.FieldTypeString, "name"),
			"/email":      path(core.FieldTypeString, "email"),
			"/mood":       path(core.FieldTypeString, "mood"),
			"/rank":       path(core.FieldTypeInt, "rank"),
			"/price":      path(core.FieldTypeDecimal, "price"),
			"/amount":     path(core.FieldTypeDecimal, "amount"),
			"/ratio":      path(core.FieldTypeFloat, "ratio"),
			"/span":       path(core.FieldTypeString, "span"),
			"/at":         path(core.FieldTypeTimestamp, "at"),
			"/doc":        path(core.FieldTypeJSON, "doc"),
			"/raw":        raw,
			"/tags":       tags,
			"/version":    path(core.FieldTypeInt, "version"),
			"/updated_at": path(core.FieldTypeTimestamp, "updated_at"),
		},
	}
}

// changeDB describes one database under test.
type changeDB struct {
	db      *sql.DB
	dialect patch.SQLDialect
	setup   []string // types, collations and the things table, created once
	// readBack selects row 1's patchable columns as one text, each value as
	// a read returns it, NULL apart from any text.
	readBack string
}

type changeCase struct {
	name string
	// seed patches the row before the case, unguarded, as parse writes it;
	// seedSQL then writes what parse would not, such as reordered keys.
	seed    string
	seedSQL string
	patch   string
	changed bool
	// on overrides changed for the dialect it names.
	on map[string]bool
	// repeats is true for a patch that changes the row each time it is
	// sent: an append.
	repeats bool
}

func patchOf(t *testing.T, body string) *patch.ParsedPatch {
	t.Helper()
	if strings.HasPrefix(body, "[") {
		parsed, err := patch.NewParser(changeSchema(), nil).Parse([]byte(body))
		require.NoError(t, err, body)
		return parsed
	}
	parsed, err := patch.ParseMergePatch([]byte(body), changeSchema(), nil)
	require.NoError(t, err, body)
	return parsed
}

var changeCases = []changeCase{
	{name: "the same integer", seed: `{"rank":1}`, patch: `{"rank":1}`},
	{name: "another integer", seed: `{"rank":1}`, patch: `{"rank":2}`, changed: true},
	{name: "NULL to NULL", seed: `{"name":null}`, patch: `{"name":null}`},
	{name: "NULL to a value", seed: `{"name":null}`, patch: `{"name":"a"}`, changed: true},
	{name: "a value to NULL", seed: `{"name":"a"}`, patch: `{"name":null}`, changed: true},
	{name: "the same text", seed: `{"name":"Name"}`, patch: `{"name":"Name"}`},
	{name: "case alone, on a case-insensitive column", seed: `{"name":"Name"}`, patch: `{"name":"NAME"}`, changed: true},
	{name: "case alone, on citext", seed: `{"email":"Bob@x.io"}`, patch: `{"email":"bob@x.io"}`, changed: true},
	{name: "the same enum label", seed: `{"mood":"ok"}`, patch: `{"mood":"ok"}`},
	{name: "another enum label", seed: `{"mood":"ok"}`, patch: `{"mood":"happy"}`, changed: true},
	{name: "a decimal the column pads", seed: `{"price":"1.5"}`, patch: `{"price":"1.50"}`},
	{name: "a decimal in another scale", seed: `{"amount":"1.5"}`, patch: `{"amount":"1.50"}`},
	{name: "a decimal stored in another scale", seedSQL: `UPDATE things SET amount = 1.50`, patch: `{"amount":"1.5"}`},
	{name: "a number stored as an integer or a real", seedSQL: `UPDATE things SET price = 1.0, amount = 1`, patch: `{"price":"1","amount":"1"}`},
	{name: "another decimal", seed: `{"price":"1.5"}`, patch: `{"price":"1.51"}`, changed: true},
	{name: "a float zero of the other sign", seed: `{"ratio":0}`, seedSQL: `UPDATE things SET ratio = CAST('-0' AS REAL)`, patch: `{"ratio":0}`,
		on: map[string]bool{"postgres": true}},
	{name: "the same interval", seed: `{"span":"1 day"}`, patch: `{"span":"1 day"}`},
	{name: "an interval in other units", seed: `{"span":"1 day"}`, patch: `{"span":"24 hours"}`, changed: true},
	{name: "a timestamp's instant in another offset", seed: `{"at":"2026-01-01T00:00:00Z"}`, patch: `{"at":"2025-12-31T19:00:00-05:00"}`},
	{name: "another instant", seed: `{"at":"2026-01-01T00:00:00Z"}`, patch: `{"at":"2026-01-01T00:00:00.000001Z"}`, changed: true},
	{name: "the same document", seed: `[{"op":"replace","path":"/doc","value":{"a":1,"b":[2]}}]`, patch: `[{"op":"replace","path":"/doc","value":{"b":[2],"a":1}}]`},
	{name: "a document stored in another key order", seedSQL: `UPDATE things SET doc = '{"b":[2], "a":1}'`,
		patch: `[{"op":"replace","path":"/doc","value":{"a":1,"b":[2]}}]`, on: map[string]bool{"sqlite": true}},
	{name: "a json column stored in another key order", seedSQL: `UPDATE things SET raw = '{"b":[2], "a":1}'`,
		patch: `[{"op":"replace","path":"/raw","value":{"a":1,"b":[2]}}]`, on: map[string]bool{"sqlite": true}},
	{name: "a document number stored in another form", seedSQL: `UPDATE things SET doc = '{"a":1.0}'`,
		patch: `[{"op":"replace","path":"/doc","value":{"a":1}}]`, changed: true},
	{name: "another document", seed: `[{"op":"replace","path":"/doc","value":{"a":1}}]`, patch: `[{"op":"replace","path":"/doc","value":{"a":2}}]`, changed: true},
	{name: "a merge that sets what is there", seed: `[{"op":"replace","path":"/doc","value":{"a":1,"b":{"c":2}}}]`, patch: `{"doc":{"b":{"c":2}}}`},
	{name: "a merge deleting a missing member", seed: `[{"op":"replace","path":"/doc","value":{"a":1}}]`, patch: `{"doc":{"z":null}}`},
	{name: "a merge that changes a member", seed: `[{"op":"replace","path":"/doc","value":{"a":1,"b":{"c":2}}}]`, patch: `{"doc":{"b":{"c":3}}}`, changed: true},
	{name: "a merge into NULL", seed: `{"doc":null}`, patch: `{"doc":{"z":null}}`, changed: true},
	{name: "the same array", seed: `{"tags":["x","y"]}`, patch: `{"tags":["x","y"]}`},
	{name: "an array in another order", seed: `{"tags":["x","y"]}`, patch: `{"tags":["y","x"]}`, changed: true},
	{name: "an append", seed: `{"tags":["x"]}`, patch: `[{"op":"array_append","path":"/tags","value":"x"}]`, changed: true, repeats: true},
	{name: "a cleared array that is empty", seed: `{"tags":[]}`, patch: `{"tags":null}`},
	{name: "one column of two changing", seed: `{"rank":1,"name":"a"}`, patch: `{"rank":1,"name":"b"}`, changed: true},
	{name: "the server's columns do not count", seed: `{"rank":1}`, patch: `{"rank":1,"version":7,"updated_at":"2001-01-01T00:00:00Z"}`},
}

// runChangeCases executes each case's guarded UPDATE: it writes, stamps and
// counts the row exactly when the patch changes it, and a row it leaves reads
// back the same after the unguarded write too.
func runChangeCases(t *testing.T, cdb changeDB) {
	t.Helper()
	for _, stmt := range cdb.setup {
		_, err := cdb.db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	stamps := []patch.PatchSQLOption{patch.WithAutoTimestampSQL("updated_at", "CURRENT_TIMESTAMP"), patch.WithIncrement("version")}
	for _, tc := range changeCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cdb.db.Exec(`DELETE FROM things`)
			require.NoError(t, err)
			_, err = cdb.db.Exec(`INSERT INTO things (id) VALUES (1)`)
			require.NoError(t, err)
			if tc.seed != "" {
				seed, err := patch.BuildPatchSQL(patchOf(t, tc.seed), cdb.dialect)
				require.NoError(t, err)
				stmt := "UPDATE things SET " + seed.SetClause + " WHERE id = " + cdb.dialect.Placeholder(seed.ParamCount+1)
				_, err = cdb.db.Exec(stmt, append(seed.Args, 1)...)
				require.NoError(t, err, stmt)
			}
			if tc.seedSQL != "" {
				_, err := cdb.db.Exec(tc.seedSQL)
				require.NoError(t, err, tc.seedSQL)
			}

			p := patchOf(t, tc.patch)
			plain, err := patch.BuildPatchSQL(p, cdb.dialect, stamps...)
			require.NoError(t, err)
			res, err := patch.BuildPatchSQL(p, cdb.dialect, append(stamps, patch.WithChangePredicate())...)
			require.NoError(t, err)
			assert.Equal(t, plain.SetClause, res.SetClause, "the predicate leaves the SET clause as it is")
			assert.Equal(t, plain.Args, res.Args, "the predicate binds nothing of its own")
			require.NotEmpty(t, res.ChangePredicate)

			byID := " WHERE id = " + cdb.dialect.Placeholder(res.ParamCount+1)
			stmt := "UPDATE things SET " + res.SetClause + byID + " AND " + res.ChangePredicate
			out, err := cdb.db.Exec(stmt, append(res.Args, 1)...)
			require.NoError(t, err, stmt)
			n, err := out.RowsAffected()
			require.NoError(t, err)

			want := tc.changed
			if v, ok := tc.on[cdb.dialect.Name()]; ok {
				want = v
			}
			assert.Equal(t, want, n == 1, "rows updated by %s", stmt)
			if n == 0 {
				tx, err := cdb.db.Begin()
				require.NoError(t, err)
				before := readBack(t, tx, cdb.readBack)
				_, err = tx.Exec("UPDATE things SET "+plain.SetClause+byID, append(plain.Args, 1)...)
				require.NoError(t, err)
				assert.Equal(t, before, readBack(t, tx, cdb.readBack), "the write the predicate skipped would read back the same")
				require.NoError(t, tx.Rollback())
			}
			var version int64
			var stamped bool
			require.NoError(t, cdb.db.QueryRow(`SELECT version, updated_at IS NOT NULL FROM things WHERE id = 1`).Scan(&version, &stamped))
			assert.Equal(t, want, version == 2, "the counter moves only with a change")
			assert.Equal(t, want, stamped, "the stamp moves only with a change")

			if want {
				again, err := cdb.db.Exec(stmt, append(res.Args, 1)...)
				require.NoError(t, err, stmt)
				n, err := again.RowsAffected()
				require.NoError(t, err)
				assert.Equal(t, tc.repeats, n == 1, "the same patch again changes the row only if it repeats")
			}
		})
	}
}

func readBack(t *testing.T, tx *sql.Tx, query string) string {
	t.Helper()
	var row string
	require.NoError(t, tx.QueryRow(query).Scan(&row), query)
	return row
}
