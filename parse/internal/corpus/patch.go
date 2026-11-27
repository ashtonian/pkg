//go:build integration

package corpus

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/jsonmerge"
	"github.com/ashtonian/pkg/parse/internal/patchtest"
	"github.com/ashtonian/pkg/parse/patch"
)

// The cases of patch's SQL (patch.BuildPatchSQL), each statement executed on
// the engine and the row it leaves read back.

// patchDialect compiles a backend's filters and its patches.
type patchDialect interface {
	sqlDialect
	patch.RelationDialect
}

// patchDialectOf is b's dialect as patch compiles for it.
func patchDialectOf(t *testing.T, b sqlBackend) patchDialect {
	t.Helper()
	d, ok := b.dialect.(patchDialect)
	require.True(t, ok, "%T compiles no patch", b.dialect)
	return d
}

// changeTable is the things table the change cases patch on an engine.
type changeTable struct {
	// setup creates the types, collations and the things table.
	setup []string
	// readBack selects row 1's patchable columns as one text, each value as
	// a read returns it, NULL apart from any text.
	readBack string
}

func changeTableOn(engine Kind) changeTable {
	if engine == SQLite {
		table := changeTable{
			setup: []string{`CREATE TABLE things (id INTEGER PRIMARY KEY, name TEXT COLLATE NOCASE, email TEXT COLLATE NOCASE, mood TEXT,
			rank INTEGER, price NUMERIC, amount REAL, ratio REAL, span TEXT, at TEXT, doc TEXT, raw TEXT, tags TEXT NOT NULL DEFAULT '[]',
			version INTEGER NOT NULL DEFAULT 1, updated_at TEXT)`},
			// quote() keeps the storage class: 1 and 1.0 read apart. A
			// timestamp, a document and an array read as their values
			// (parse_canonical), as PostgreSQL's timestamptz, jsonb and array
			// text does.
			readBack: `SELECT quote(name) || '|' || quote(email) || '|' || quote(mood) || '|' || quote(rank) || '|' || quote(price) || '|' ||
			quote(amount) || '|' || quote(ratio) || '|' || quote(span) || '|' || quote(parse_canonical(at, 'timestamp')) || '|' ||
			quote(parse_canonical(doc, 'document')) || '|' || quote(parse_canonical(raw, 'document')) || '|' ||
			quote(parse_canonical(tags, 'array:string')) FROM things WHERE id = 1`,
		}
		return table
	}
	table := changeTable{
		setup: []string{
			// An extension is one per database: in public it outlives the test
			// schema's cleanup, as the PostGIS helper's does.
			`CREATE EXTENSION IF NOT EXISTS citext SCHEMA public`,
			`CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy')`,
			`CREATE COLLATION ci (provider = icu, locale = 'und-u-ks-level2', deterministic = false)`,
			`CREATE TABLE things (id BIGINT PRIMARY KEY, name TEXT COLLATE ci, email citext, mood mood,
				rank BIGINT, price NUMERIC(12,2), amount NUMERIC, ratio DOUBLE PRECISION, span INTERVAL, at TIMESTAMPTZ,
				doc JSONB, raw JSON, tags TEXT[] NOT NULL DEFAULT '{}', version BIGINT NOT NULL DEFAULT 1, updated_at TIMESTAMPTZ)`,
		},
		// A decimal's scale, and a json column's whitespace and key order,
		// are not part of the value.
		readBack: `SELECT concat_ws('|', quote_nullable(name::text), quote_nullable(email::text), quote_nullable(mood::text),
			quote_nullable(rank::text), quote_nullable(trim_scale(price)::text), quote_nullable(trim_scale(amount)::text), quote_nullable(ratio::text),
			quote_nullable(span::text), quote_nullable(at::text), quote_nullable(doc::text), quote_nullable(raw::jsonb::text),
			quote_nullable(tags::text)) FROM things WHERE id = 1`,
	}
	return table
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

func changeCases() []changeCase {
	cases := []changeCase{
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
		{name: "a timestamp stored in another offset", seedSQL: `UPDATE things SET at = '2025-12-31T19:00:00-05:00'`, patch: `{"at":"2026-01-01T00:00:00Z"}`},
		{name: "another instant", seed: `{"at":"2026-01-01T00:00:00Z"}`, patch: `{"at":"2026-01-01T00:00:00.000001Z"}`, changed: true},
		{name: "the same document", seed: `[{"op":"replace","path":"/doc","value":{"a":1,"b":[2]}}]`, patch: `[{"op":"replace","path":"/doc","value":{"b":[2],"a":1}}]`},
		{name: "a document stored in another key order", seedSQL: `UPDATE things SET doc = '{"b":[2], "a":1}'`,
			patch: `[{"op":"replace","path":"/doc","value":{"a":1,"b":[2]}}]`},
		{name: "a json column stored in another key order", seedSQL: `UPDATE things SET raw = '{"b":[2], "a":1}'`,
			patch: `[{"op":"replace","path":"/raw","value":{"a":1,"b":[2]}}]`},
		{name: "document numbers stored in another form", seedSQL: `UPDATE things SET doc = '{"a":1e2,"b":-0,"c":1.50e1}'`,
			patch: `[{"op":"replace","path":"/doc","value":{"a":100,"b":0,"c":15.0}}]`},
		{name: "a document number stored in another form", seedSQL: `UPDATE things SET doc = '{"a":1.0}'`,
			patch: `[{"op":"replace","path":"/doc","value":{"a":1}}]`, changed: true},
		{name: "another document", seed: `[{"op":"replace","path":"/doc","value":{"a":1}}]`, patch: `[{"op":"replace","path":"/doc","value":{"a":2}}]`, changed: true},
		{name: "a merge that sets what is there", seed: `[{"op":"replace","path":"/doc","value":{"a":1,"b":{"c":2}}}]`, patch: `{"doc":{"b":{"c":2}}}`},
		{name: "a merge deleting a missing member", seed: `[{"op":"replace","path":"/doc","value":{"a":1}}]`, patch: `{"doc":{"z":null}}`},
		{name: "a merge that changes a member", seed: `[{"op":"replace","path":"/doc","value":{"a":1,"b":{"c":2}}}]`, patch: `{"doc":{"b":{"c":3}}}`, changed: true},
		{name: "a merge into NULL", seed: `{"doc":null}`, patch: `{"doc":{"z":null}}`, changed: true},
		{name: "a merge into a json column that sets what is there", seed: `[{"op":"replace","path":"/raw","value":{"a":1,"b":{"c":2}}}]`, patch: `{"raw":{"b":{"c":2}}}`},
		{name: "a merge into a json column that changes a member", seed: `[{"op":"replace","path":"/raw","value":{"a":1}}]`, patch: `{"raw":{"b":{"c":3},"a":null}}`, changed: true},
		{name: "the same array", seed: `{"tags":["x","y"]}`, patch: `{"tags":["x","y"]}`},
		{name: "an array in another order", seed: `{"tags":["x","y"]}`, patch: `{"tags":["y","x"]}`, changed: true},
		{name: "an append", seed: `{"tags":["x"]}`, patch: `[{"op":"array_append","path":"/tags","value":"x"}]`, changed: true, repeats: true},
		{name: "a cleared array that is empty", seed: `{"tags":[]}`, patch: `{"tags":null}`},
		{name: "one column of two changing", seed: `{"rank":1,"name":"a"}`, patch: `{"rank":1,"name":"b"}`, changed: true},
		{name: "the server's columns do not count", seed: `{"rank":1}`, patch: `{"rank":1,"version":7,"updated_at":"2001-01-01T00:00:00Z"}`},
	}
	return cases
}

// Each change case's guarded UPDATE writes, stamps and counts the row
// exactly when the patch changes it, and a row it leaves reads back the
// same after the unguarded write too. On PostgreSQL equality is coarser
// than a read for numeric scale, intervals, a float's zero sign, citext and
// a nondeterministic collation, and a json column has no equality operator.
func buildPatchSQLChangePredicate(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db, d, table := b.db(t), patchDialectOf(t, b), changeTableOn(b.engine)
		ctx := t.Context()
		for _, stmt := range table.setup {
			_, err := db.ExecContext(ctx, stmt)
			require.NoError(t, err, stmt)
		}
		stamps := []patch.PatchSQLOption{patch.WithAutoTimestampSQL("updated_at", "CURRENT_TIMESTAMP"), patch.WithIncrement("version")}
		for _, tc := range changeCases() {
			t.Run(tc.name, func(t *testing.T) {
				ctx := t.Context()
				_, err := db.ExecContext(ctx, `DELETE FROM things`)
				require.NoError(t, err)
				_, err = db.ExecContext(ctx, `INSERT INTO things (id) VALUES (1)`)
				require.NoError(t, err)
				if tc.seed != "" {
					seed, err := patch.BuildPatchSQL(patchtest.ChangePatch(t, tc.seed), d)
					require.NoError(t, err)
					stmt := "UPDATE things SET " + seed.SetClause + " WHERE id = " + d.Placeholder(seed.ParamCount+1)
					_, err = db.ExecContext(ctx, stmt, append(seed.Args, 1)...)
					require.NoError(t, err, stmt)
				}
				if tc.seedSQL != "" {
					_, err := db.ExecContext(ctx, tc.seedSQL)
					require.NoError(t, err, tc.seedSQL)
				}

				p := patchtest.ChangePatch(t, tc.patch)
				plain, err := patch.BuildPatchSQL(p, d, stamps...)
				require.NoError(t, err)
				res, err := patch.BuildPatchSQL(p, d, append(stamps, patch.WithChangePredicate())...)
				require.NoError(t, err)
				assert.Equal(t, plain.SetClause, res.SetClause, "the predicate leaves the SET clause as it is")
				assert.Equal(t, plain.Args, res.Args, "the predicate binds nothing of its own")
				require.NotEmpty(t, res.ChangePredicate)

				byID := " WHERE id = " + d.Placeholder(res.ParamCount+1)
				stmt := "UPDATE things SET " + res.SetClause + byID + " AND " + res.ChangePredicate
				out, err := db.ExecContext(ctx, stmt, append(res.Args, 1)...)
				require.NoError(t, err, stmt)
				n, err := out.RowsAffected()
				require.NoError(t, err)

				want := tc.changed
				override, overridden := tc.on[d.Name()]
				if overridden {
					want = override
				}
				assert.Equal(t, want, n == 1, "rows updated by %s", stmt)
				if n == 0 {
					tx, err := db.BeginTx(ctx, nil)
					require.NoError(t, err)
					before := readChangedRow(t, tx, table.readBack)
					_, err = tx.ExecContext(ctx, "UPDATE things SET "+plain.SetClause+byID, append(plain.Args, 1)...)
					require.NoError(t, err)
					assert.Equal(t, before, readChangedRow(t, tx, table.readBack), "the write the predicate skipped would read back the same")
					require.NoError(t, tx.Rollback())
				}
				var version int64
				var stamped bool
				require.NoError(t, db.QueryRowContext(ctx, `SELECT version, updated_at IS NOT NULL FROM things WHERE id = 1`).Scan(&version, &stamped))
				assert.Equal(t, want, version == 2, "the counter moves only with a change")
				assert.Equal(t, want, stamped, "the stamp moves only with a change")

				if want {
					again, err := db.ExecContext(ctx, stmt, append(res.Args, 1)...)
					require.NoError(t, err, stmt)
					n, err := again.RowsAffected()
					require.NoError(t, err)
					assert.Equal(t, tc.repeats, n == 1, "the same patch again changes the row only if it repeats")
				}
			})
		}
	})
}

// readChangedRow is query's one text in tx.
func readChangedRow(t *testing.T, tx *sql.Tx, query string) string {
	t.Helper()
	var row string
	require.NoError(t, tx.QueryRowContext(t.Context(), query).Scan(&row), query)
	return row
}

// foldTable is the items table the fold cases patch on an engine: its
// CREATE TABLE, the INSERT of the starting row, and the SELECT expression
// yielding tags as JSON text (NULL when NULL).
type foldTable struct {
	schema, seed, tagsExpr string
}

func foldTableOn(engine Kind) foldTable {
	if engine == SQLite {
		table := foldTable{
			schema:   `CREATE TABLE items (id INTEGER PRIMARY KEY, tags TEXT, name TEXT NOT NULL, updated_at TEXT, meta TEXT, version INTEGER NOT NULL DEFAULT 1)`,
			seed:     `INSERT INTO "items" (id, tags, name) VALUES (1, '["zero"]', 'orig')`,
			tagsExpr: "tags",
		}
		return table
	}
	table := foldTable{
		schema:   `CREATE TABLE items (id BIGINT PRIMARY KEY, tags TEXT[], name TEXT NOT NULL, updated_at TIMESTAMPTZ, meta JSONB, version BIGINT NOT NULL DEFAULT 1)`,
		seed:     `INSERT INTO items (id, tags, name) VALUES (1, ARRAY['zero'], 'orig')`,
		tagsExpr: "array_to_json(tags)::text",
	}
	return table
}

type foldCase struct {
	name     string
	ops      string
	wantTags []string // nil means the column is NULL
	wantName string
	wantArgs int // bound values, excluding the WHERE id
}

// foldCases each start from tags=["zero"], name="orig".
func foldCases() []foldCase {
	cases := []foldCase{
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
	return cases
}

// Ordered operations on one column compose, not overwrite each other,
// executed and asserting the stored values; PostgreSQL also rejects any
// repeated column assignment.
func buildPatchSQLFoldsOrderedOps(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db, d, table := b.db(t), patchDialectOf(t, b), foldTableOn(b.engine)
		parser := patch.NewParser(patchtest.FoldSchema(), nil)
		update := func(t *testing.T, res *patch.PatchSQLResult) {
			t.Helper()
			stmt := fmt.Sprintf("UPDATE items SET %s WHERE id = %s", res.SetClause, d.Placeholder(res.ParamCount+1))
			_, err := db.ExecContext(t.Context(), stmt, append(res.Args, 1)...)
			require.NoError(t, err, stmt)
		}
		for _, tc := range foldCases() {
			t.Run(tc.name, func(t *testing.T) {
				resetFoldTable(t, db, table)
				parsed, err := parser.Parse([]byte(tc.ops))
				require.NoError(t, err)
				res, err := patch.BuildPatchSQL(parsed, d)
				require.NoError(t, err)
				assert.Len(t, res.Args, tc.wantArgs, "SET %s", res.SetClause)
				update(t, res)

				tags, name := readFoldRow(t, db, table)
				assert.Equal(t, tc.wantTags, tags)
				assert.Equal(t, tc.wantName, name)
			})
		}

		t.Run("patched auto-timestamp column assigned once", func(t *testing.T) {
			resetFoldTable(t, db, table)
			parsed, err := parser.Parse([]byte(`[{"op":"replace","path":"/updated_at","value":"2001-01-01T00:00:00Z"},{"op":"replace","path":"/name","value":"n"}]`))
			require.NoError(t, err)
			fixed := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
			res, err := patch.BuildPatchSQL(parsed, d,
				patch.WithAutoTimestamp("updated_at"), patch.WithTimeFunc(func() time.Time { return fixed }))
			require.NoError(t, err)
			assert.Equal(t, 1, strings.Count(res.SetClause, `"updated_at" =`), res.SetClause)
			// PostgreSQL rejects a repeated column assignment outright.
			update(t, res)
		})

		t.Run("auto-timestamp from the database's clock", func(t *testing.T) {
			resetFoldTable(t, db, table)
			parsed, err := parser.Parse([]byte(`[{"op":"replace","path":"/updated_at","value":"2001-01-01T00:00:00Z"},{"op":"replace","path":"/name","value":"n"}]`))
			require.NoError(t, err)
			res, err := patch.BuildPatchSQL(parsed, d, patch.WithAutoTimestampSQL("updated_at", "CURRENT_TIMESTAMP"))
			require.NoError(t, err)
			assert.Contains(t, res.SetClause, `"updated_at" = CURRENT_TIMESTAMP`)
			assert.Equal(t, 1, strings.Count(res.SetClause, `"updated_at" =`), res.SetClause)
			assert.Len(t, res.Args, 1, "only the name is bound")
			update(t, res)
		})

		t.Run("a counter moves one past what it holds, once", func(t *testing.T) {
			resetFoldTable(t, db, table)
			parsed, err := parser.Parse([]byte(`[{"op":"replace","path":"/version","value":99},{"op":"replace","path":"/name","value":"n"}]`))
			require.NoError(t, err)
			res, err := patch.BuildPatchSQL(parsed, d, patch.WithAutoTimestampSQL("updated_at", "CURRENT_TIMESTAMP"), patch.WithIncrement("version"))
			require.NoError(t, err)
			assert.Equal(t, 1, strings.Count(res.SetClause, `"version" =`), res.SetClause)
			assert.Len(t, res.Args, 1, "only the name is bound")
			stmt := fmt.Sprintf("UPDATE items SET %s WHERE id = %s RETURNING version", res.SetClause, d.Placeholder(res.ParamCount+1))
			for want := 2; want <= 3; want++ {
				var version int
				require.NoError(t, db.QueryRowContext(t.Context(), stmt, append(res.Args, 1)...).Scan(&version), stmt)
				assert.Equal(t, want, version, "the patch's 99 is not the server's")
			}
		})

		t.Run("a patch writing only server columns writes nothing", func(t *testing.T) {
			parsed, err := parser.Parse([]byte(`[{"op":"replace","path":"/version","value":99},{"op":"replace","path":"/updated_at","value":"2001-01-01T00:00:00Z"}]`))
			require.NoError(t, err)
			res, err := patch.BuildPatchSQL(parsed, d, patch.WithAutoTimestamp("updated_at"), patch.WithIncrement("version"))
			require.NoError(t, err)
			assert.True(t, res.Empty)
			assert.Empty(t, res.SetClause)
		})
	})
}

func resetFoldTable(t *testing.T, db *sql.DB, table foldTable) {
	t.Helper()
	for _, stmt := range []string{"DROP TABLE IF EXISTS items", table.schema, table.seed} {
		_, err := db.ExecContext(t.Context(), stmt)
		require.NoError(t, err, stmt)
	}
}

func readFoldRow(t *testing.T, db *sql.DB, table foldTable) ([]string, string) {
	t.Helper()
	var tagsJSON sql.NullString
	var name string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT "+table.tagsExpr+", name FROM items WHERE id = 1").Scan(&tagsJSON, &name))
	if !tagsJSON.Valid {
		return nil, name
	}
	var tags []string
	require.NoError(t, json.Unmarshal([]byte(tagsJSON.String), &tags))
	return tags, name
}

// docTable is a docs table of one document column on an engine.
type docTable struct {
	name   string
	schema string
	// storage is the doc column's declared type: core.SQLJSON for a json
	// column, "" otherwise.
	storage core.SQLType
	insert  string // INSERT with one placeholder for the document text
	read    string // SELECT yielding the document as JSON text
}

// docTablesOn are the document columns an engine holds a document in: a
// json (not jsonb) column on PostgreSQL is written through jsonb, and the
// result stored back.
func docTablesOn(engine Kind) []docTable {
	if engine == SQLite {
		tables := []docTable{{name: "text", schema: `CREATE TABLE docs (id INTEGER PRIMARY KEY, doc TEXT)`,
			insert: `INSERT INTO docs (id, doc) VALUES (1, ?1)`, read: `SELECT doc FROM docs WHERE id = 1`}}
		return tables
	}
	tables := []docTable{
		{name: "jsonb", schema: `CREATE TABLE docs (id BIGINT PRIMARY KEY, doc JSONB)`,
			insert: `INSERT INTO docs (id, doc) VALUES (1, $1::jsonb)`, read: `SELECT doc::text FROM docs WHERE id = 1`},
		{name: "json", schema: `CREATE TABLE docs (id BIGINT PRIMARY KEY, doc JSON)`, storage: core.SQLJSON,
			insert: `INSERT INTO docs (id, doc) VALUES (1, $1::json)`, read: `SELECT doc::text FROM docs WHERE id = 1`},
	}
	return tables
}

// onDocTables runs fn on a fresh database of b for each document column
// b's engine holds a document in.
func onDocTables(t *testing.T, engine Engine, fn func(t *testing.T, db *sql.DB, d patchDialect, table docTable)) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		for _, table := range docTablesOn(b.engine) {
			t.Run(table.name, func(t *testing.T) { fn(t, b.db(t), patchDialectOf(t, b), table) })
		}
	})
}

// resetDoc creates table afresh in db holding start ("" is NULL) in row 1.
func resetDoc(t *testing.T, db *sql.DB, table docTable, start string) {
	t.Helper()
	ctx := t.Context()
	_, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS docs")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, table.schema)
	require.NoError(t, err)
	var doc any
	if start != "" {
		doc = start
	}
	_, err = db.ExecContext(ctx, table.insert, doc)
	require.NoError(t, err)
}

// readDoc is row 1's document text, "" when NULL.
func readDoc(t *testing.T, db *sql.DB, table docTable) string {
	t.Helper()
	var got sql.NullString
	require.NoError(t, db.QueryRowContext(t.Context(), table.read).Scan(&got))
	return got.String
}

// memberSchema is a schema whose /doc is a document column stored as
// storage, it and its members at any depth patched by JSON Patch, taking at
// most 20 operations.
func memberSchema(storage core.SQLType) *patch.PatchSchema {
	ops := []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpReplace, patch.PatchOpRemove, patch.PatchOpArrayAppend, patch.PatchOpMove, patch.PatchOpTest}
	s, err := patch.NewPatchSchema("docs", map[string]*patch.PathSchema{
		"/doc":    {AllowedOps: ops, DataType: core.FieldTypeJSON, SQLType: storage, SQLColumn: "doc"},
		"/doc/**": {AllowedOps: ops, DataType: core.FieldTypeAny, SQLColumn: "doc"},
	})
	if err != nil {
		panic(err)
	}
	s.MaxOperations = 20
	return s
}

type memberCase struct {
	name  string
	start string // stored document; "" means NULL
	ops   string // JSON Patch
	merge string // the merge patch writing the same document; "" when none does
	want  string // resulting document; "" when the patch does not apply
	unmet int    // when it does not, the index of the first operation that fails
}

const memberBase = `{"a":1,"b":{"c":2,"d":3},"e":[1,2]}`

func memberCases() []memberCase {
	cases := []memberCase{
		{name: "replace a member", start: memberBase,
			ops: `[{"op":"replace","path":"/doc/a","value":9}]`, merge: `{"doc":{"a":9}}`,
			want: `{"a":9,"b":{"c":2,"d":3},"e":[1,2]}`},
		{name: "add a member", start: memberBase,
			ops: `[{"op":"add","path":"/doc/x","value":"new"}]`, merge: `{"doc":{"x":"new"}}`,
			want: `{"a":1,"b":{"c":2,"d":3},"e":[1,2],"x":"new"}`},
		{name: "add over a member replaces it", start: memberBase,
			ops: `[{"op":"add","path":"/doc/a","value":5}]`, merge: `{"doc":{"a":5}}`,
			want: `{"a":5,"b":{"c":2,"d":3},"e":[1,2]}`},
		{name: "remove a member", start: memberBase,
			ops: `[{"op":"remove","path":"/doc/a"}]`, merge: `{"doc":{"a":null}}`,
			want: `{"b":{"c":2,"d":3},"e":[1,2]}`},
		{name: "replace a nested member, its siblings kept", start: memberBase,
			ops: `[{"op":"replace","path":"/doc/b/c","value":7}]`, merge: `{"doc":{"b":{"c":7}}}`,
			want: `{"a":1,"b":{"c":7,"d":3},"e":[1,2]}`},
		{name: "remove a nested member", start: memberBase,
			ops: `[{"op":"remove","path":"/doc/b/d"}]`, merge: `{"doc":{"b":{"d":null}}}`,
			want: `{"a":1,"b":{"c":2},"e":[1,2]}`},
		{name: "an array member is replaced whole", start: memberBase,
			ops: `[{"op":"replace","path":"/doc/e","value":[3]}]`, merge: `{"doc":{"e":[3]}}`,
			want: `{"a":1,"b":{"c":2,"d":3},"e":[3]}`},
		{name: "an object value replaces the member rather than merging", start: memberBase,
			ops:  `[{"op":"replace","path":"/doc/b","value":{"z":1}}]`,
			want: `{"a":1,"b":{"z":1},"e":[1,2]}`},
		{name: "a null value is stored", start: memberBase,
			ops:  `[{"op":"add","path":"/doc/n","value":null}]`,
			want: `{"a":1,"b":{"c":2,"d":3},"e":[1,2],"n":null}`},
		{name: "keys holding quotes, backslashes, dots and newlines", start: `{"q\"u\\o":{"k":1},"a.b":1,"é\n":{"z":2}}`,
			ops:   `[{"op":"replace","path":"/doc/q\"u\\o/k","value":2},{"op":"remove","path":"/doc/a.b"},{"op":"add","path":"/doc/é\n/y","value":3}]`,
			merge: `{"doc":{"q\"u\\o":{"k":2},"a.b":null,"é\n":{"y":3}}}`,
			want:  `{"q\"u\\o":{"k":2},"é\n":{"z":2,"y":3}}`},
		{name: "a later operation reads the member an earlier one wrote", start: memberBase,
			ops:   `[{"op":"add","path":"/doc/x","value":{}},{"op":"add","path":"/doc/x/y","value":1},{"op":"replace","path":"/doc/x/y","value":2},{"op":"remove","path":"/doc/a"}]`,
			merge: `{"doc":{"x":{"y":2},"a":null}}`,
			want:  `{"b":{"c":2,"d":3},"e":[1,2],"x":{"y":2}}`},
		{name: "remove a member, then add it again", start: memberBase,
			ops: `[{"op":"remove","path":"/doc/a"},{"op":"add","path":"/doc/a","value":3}]`, merge: `{"doc":{"a":3}}`,
			want: `{"a":3,"b":{"c":2,"d":3},"e":[1,2]}`},
		{name: "members of a document the patch wrote whole", start: memberBase,
			ops:  `[{"op":"replace","path":"/doc","value":{"k":1}},{"op":"add","path":"/doc/m","value":2},{"op":"remove","path":"/doc/k"}]`,
			want: `{"m":2}`},
		{name: "a member operation before the document is written whole", start: memberBase,
			ops:  `[{"op":"remove","path":"/doc/a"},{"op":"replace","path":"/doc","value":{"k":1}}]`,
			want: `{"k":1}`},

		{name: "replace a missing member", start: memberBase, ops: `[{"op":"replace","path":"/doc/zz","value":1}]`, unmet: 0},
		{name: "remove a missing member", start: memberBase, ops: `[{"op":"remove","path":"/doc/zz"}]`, unmet: 0},
		{name: "add below a missing member", start: memberBase, ops: `[{"op":"add","path":"/doc/zz/y","value":1}]`, unmet: 0},
		{name: "add below a scalar", start: memberBase, ops: `[{"op":"add","path":"/doc/a/y","value":1}]`, unmet: 0},
		{name: "add below an array", start: memberBase, ops: `[{"op":"add","path":"/doc/e/y","value":1}]`, unmet: 0},
		{name: "add to a NULL document", start: "", ops: `[{"op":"add","path":"/doc/x","value":1}]`, unmet: 0},
		{name: "add to a document that is not an object", start: `[1]`, ops: `[{"op":"add","path":"/doc/x","value":1}]`, unmet: 0},
		{name: "the second operation fails on the stored row", start: memberBase,
			ops: `[{"op":"replace","path":"/doc/a","value":2},{"op":"remove","path":"/doc/zz"}]`, unmet: 1},
		{name: "remove a member twice", start: memberBase,
			ops: `[{"op":"remove","path":"/doc/a"},{"op":"remove","path":"/doc/a"}]`, unmet: 1},
		{name: "add below a member the patch set to a scalar", start: memberBase,
			ops: `[{"op":"replace","path":"/doc/b","value":1},{"op":"add","path":"/doc/b/y","value":1}]`, unmet: 1},
		{name: "a member of a removed document", start: memberBase,
			ops: `[{"op":"remove","path":"/doc"},{"op":"add","path":"/doc/x","value":1}]`, unmet: 1},
		{name: "replace a member the document written whole lacks", start: memberBase,
			ops: `[{"op":"replace","path":"/doc","value":{"k":1}},{"op":"replace","path":"/doc/a","value":1}]`, unmet: 1},
	}
	return cases
}

// RFC 6902 member writes compile to SQL on every dialect: each case is
// executed with its precondition, checked against json-patch applying the
// same operations to the document path's form of the row, and against the
// equivalent merge patch where there is one. A dialect that refuses a
// member name holding a double quote or a backslash refuses the case
// naming one, and its merge patch writes what json-patch does.
func buildPatchSQLMembers(t *testing.T, engine Engine) {
	onDocTables(t, engine, func(t *testing.T, db *sql.DB, d patchDialect, table docTable) {
		parser := patch.NewParser(memberSchema(table.storage), nil)
		for _, tc := range memberCases() {
			t.Run(tc.name, func(t *testing.T) {
				ctx := t.Context()
				resetDoc(t, db, table, tc.start)
				reference, referenceErr := applyRFC6902(t, tc.start, tc.ops)

				merged := func() string {
					t.Helper()
					resetDoc(t, db, table, tc.start)
					merge, err := patch.ParseMergePatch([]byte(tc.merge), patchtest.MergeSchema(table.storage), nil)
					require.NoError(t, err)
					mergeRes, err := patch.BuildPatchSQL(merge, d)
					require.NoError(t, err)
					_, err = db.ExecContext(t.Context(), "UPDATE docs SET "+mergeRes.SetClause+" WHERE id = "+d.Placeholder(mergeRes.ParamCount+1), append(mergeRes.Args, 1)...)
					require.NoError(t, err)
					return readDoc(t, db, table)
				}

				parsed, err := parser.Parse([]byte(tc.ops))
				require.NoError(t, err)
				res, err := patch.BuildPatchSQL(parsed, d)
				if tc.want == "" && err != nil {
					pe, ok := errors.AsType[*core.ParseError](err)
					require.True(t, ok, "%v is no ParseError", err)
					assert.Equal(t, patch.ECodePatchValueInvalid.String(), pe.Code, "the patch's own operations settle it fails")
					require.NotNil(t, pe.Index)
					assert.Equal(t, tc.unmet, *pe.Index)
					require.Error(t, referenceErr, "json-patch fails it too")
					return
				}
				require.NoError(t, err)

				where := "id = " + d.Placeholder(res.ParamCount+1)
				if res.Precondition != "" {
					where += " AND " + res.Precondition
				}
				stmt := "UPDATE docs SET " + res.SetClause + " WHERE " + where
				result, err := db.ExecContext(ctx, stmt, append(res.Args, 1)...)
				require.NoError(t, err, stmt)
				written, err := result.RowsAffected()
				require.NoError(t, err)

				if tc.want == "" {
					assert.Zero(t, written, "a row the patch does not apply to is not written")
					assert.Equal(t, decodeDoc(t, tc.start), decodeDoc(t, readDoc(t, db, table)))
					require.Error(t, referenceErr, "json-patch fails it too")
					var args []any
					bind := func(v any) string { args = append(args, v); return d.Placeholder(len(args)) }
					_, firstUnmet, err := patch.RenderPreconditions(d, res.Preconditions, bind)
					require.NoError(t, err)
					var unmet int
					require.NoError(t, db.QueryRowContext(ctx, "SELECT "+firstUnmet+" FROM docs WHERE id = 1", args...).Scan(&unmet))
					assert.Equal(t, tc.unmet, unmet, "the first operation the stored row fails")
					return
				}
				assert.Equal(t, int64(1), written)
				got := readDoc(t, db, table)
				assert.Equal(t, decodeDoc(t, tc.want), decodeDoc(t, got), "stored document")
				require.NoError(t, referenceErr)
				assert.Equal(t, reference, decodeDoc(t, got), "matches json-patch")

				if tc.merge == "" {
					return
				}
				assert.Equal(t, decodeDoc(t, got), decodeDoc(t, merged()), "the same row as the equivalent merge patch")
			})
		}
	})
}

// applyRFC6902 applies ops to the document path's form of a row whose doc
// is start ("" is null) with json-patch, and returns the resulting doc.
func applyRFC6902(t *testing.T, start, ops string) (any, error) {
	t.Helper()
	doc := "null"
	if start != "" {
		doc = start
	}
	decoded, err := jsonpatch.DecodePatch([]byte(ops))
	require.NoError(t, err)
	out, err := decoded.Apply(fmt.Appendf(nil, `{"doc":%s}`, doc))
	if err != nil {
		return nil, err
	}
	var row map[string]any
	require.NoError(t, json.Unmarshal(out, &row))
	return row["doc"], nil
}

type mergeCase struct {
	name  string
	start string // stored document; "" means NULL
	body  string // RFC 7396 merge patch
	want  string // resulting document; "" means NULL
}

const mergeBase = `{"a":1,"b":{"c":2,"d":3}}`

func mergeCases(t *testing.T) []mergeCase {
	cases := []mergeCase{
		{"siblings survive a nested merge", mergeBase, `{"doc":{"b":{"c":9}}}`, `{"a":1,"b":{"c":9,"d":3}}`},
		{"null deletes a member", mergeBase, `{"doc":{"a":null}}`, `{"b":{"c":2,"d":3}}`},
		{"nested null deletes a nested member", mergeBase, `{"doc":{"b":{"d":null}}}`, `{"a":1,"b":{"c":2}}`},
		{"new members, arrays replace", mergeBase, `{"doc":{"e":[1,2],"f":{"g":true}}}`, `{"a":1,"b":{"c":2,"d":3},"e":[1,2],"f":{"g":true}}`},
		{"deleting a missing member is a no-op", mergeBase, `{"doc":{"zz":null}}`, mergeBase},
		{"object over a scalar member", `{"a":1}`, `{"doc":{"a":{"z":1}}}`, `{"a":{"z":1}}`},
		{"merge into NULL", "", `{"doc":{"x":1}}`, `{"x":1}`},
		{"merge into a non-object document", `[1,2]`, `{"doc":{"x":1}}`, `{"x":1}`},
		{"keys are data, not paths", mergeBase, `{"doc":{"it's":"q","a.b":{"c":1},"{x}":2}}`, `{"a":1,"b":{"c":2,"d":3},"it's":"q","a.b":{"c":1},"{x}":2}`},
		{"null document removes", mergeBase, `{"doc":null}`, ""},
		{"non-object value replaces", mergeBase, `{"doc":[3]}`, `[3]`},
		{"empty patch object keeps the document", mergeBase, `{"doc":{}}`, mergeBase},
		{"an array member is not an object to merge into", `{"a":[{"x":1}]}`, `{"doc":{"a":{"0":{"y":2}}}}`, `{"a":{"0":{"y":2}}}`},
		{"nested keys needing quotes", `{"q\"u\\o":{"k":1},"é\n":{"z":{"w":1,"v":2}}}`, `{"doc":{"q\"u\\o":{"k":null,"j":2},"é\n":{"z":{"w":null}}}}`, `{"q\"u\\o":{"j":2},"é\n":{"z":{"v":2}}}`},
		{"five levels", `{"a":{"b":{"c":{"x":1}}}}`, `{"doc":{"a":{"b":{"c":{"d":1}}}}}`, `{"a":{"b":{"c":{"x":1,"d":1}}}}`},
		wideMergeCase(t),
	}
	return cases
}

// wideMergeCase merges far more members than one PostgreSQL function call
// takes arguments (100), at the top of the document and one level down.
func wideMergeCase(t *testing.T) mergeCase {
	t.Helper()
	patchDoc := map[string]any{}
	want := map[string]any{"keep": 1}
	nestedPatch := map[string]any{"gone": nil}
	nestedWant := map[string]any{"old": true}
	for i := range 120 {
		patchDoc[fmt.Sprintf("k%03d", i)] = i
		want[fmt.Sprintf("k%03d", i)] = i
		nestedPatch[fmt.Sprintf("m%03d", i)] = i
		nestedWant[fmt.Sprintf("m%03d", i)] = i
	}
	patchDoc["n"] = nestedPatch
	want["n"] = nestedWant
	body, err := json.Marshal(map[string]any{"doc": patchDoc})
	require.NoError(t, err)
	wantDoc, err := json.Marshal(want)
	require.NoError(t, err)
	c := mergeCase{"wider than a function call", `{"keep":1,"n":{"old":true,"gone":0}}`, string(body), string(wantDoc)}
	return c
}

// RFC 7396 merges compile to SQL on every dialect: executed, asserting the
// stored document, cross-checked against the document path's
// jsonmerge.Apply.
func buildPatchSQLMerge(t *testing.T, engine Engine) {
	onDocTables(t, engine, func(t *testing.T, db *sql.DB, d patchDialect, table docTable) {
		for _, tc := range mergeCases(t) {
			t.Run(tc.name, func(t *testing.T) {
				resetDoc(t, db, table, tc.start)
				parsed, err := patch.ParseMergePatch([]byte(tc.body), patchtest.MergeSchema(table.storage), nil)
				require.NoError(t, err)
				res, err := patch.BuildPatchSQL(parsed, d)
				require.NoError(t, err)
				stmt := fmt.Sprintf("UPDATE docs SET %s WHERE id = %s", res.SetClause, d.Placeholder(res.ParamCount+1))
				_, err = db.ExecContext(t.Context(), stmt, append(res.Args, 1)...)
				require.NoError(t, err, stmt)

				got := readDoc(t, db, table)
				assert.Equal(t, decodeDoc(t, tc.want), decodeDoc(t, got), "stored document")
				var body map[string]any
				require.NoError(t, json.Unmarshal([]byte(tc.body), &body))
				oracle := jsonmerge.Apply(decodeDoc(t, tc.start), body["doc"])
				assert.Equal(t, oracle, decodeDoc(t, got), "matches the document path")
			})
		}
	})
}

// decodeDoc is a document's text decoded, nil for "".
func decodeDoc(t *testing.T, s string) any {
	t.Helper()
	if s == "" {
		return nil
	}
	var v any
	require.NoError(t, json.Unmarshal([]byte(s), &v))
	return v
}

// A JSON Patch whose path names a member holding a double quote or a
// backslash writes it on SQLite as json-patch does. The path quotes the
// label with its escapes, which every SQLite parsesql.New opens a provider
// on reads as the member (dialect.SQLiteMinVersion).
func buildPatchSQLSQLiteWritesAQuoteOrBackslashInAMember(t *testing.T, engine Engine) {
	b := sqlBackendOf(engine)
	db, d := b.db(t), patchDialectOf(t, b)
	ctx := t.Context()
	_, err := db.ExecContext(ctx, `CREATE TABLE docs (id INTEGER PRIMARY KEY, doc TEXT)`)
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
				ctx := t.Context()
				body, err := json.Marshal([]map[string]any{tc.op})
				require.NoError(t, err)
				want, err := applyRFC6902(t, string(start), string(body))
				require.NoError(t, err)
				parsed, err := parser.Parse(body)
				require.NoError(t, err)
				res, err := patch.BuildPatchSQL(parsed, d)
				require.NoError(t, err)

				_, err = db.ExecContext(ctx, `DELETE FROM docs`)
				require.NoError(t, err)
				_, err = db.ExecContext(ctx, `INSERT INTO docs (id, doc) VALUES (1, ?1)`, string(start))
				require.NoError(t, err)
				where := "id = " + d.Placeholder(res.ParamCount+1)
				if res.Precondition != "" {
					where += " AND " + res.Precondition
				}
				updated, err := db.ExecContext(ctx, "UPDATE docs SET "+res.SetClause+" WHERE "+where, append(res.Args, 1)...)
				require.NoError(t, err)
				n, err := updated.RowsAffected()
				require.NoError(t, err)
				require.Equal(t, int64(1), n, "the precondition holds")
				var got string
				require.NoError(t, db.QueryRowContext(ctx, `SELECT doc FROM docs WHERE id = 1`).Scan(&got))
				assert.Equal(t, want, decodeDoc(t, got))
			})
		}
	}
}

// Appending to a SQLite JSON array keeps the element's JSON type for every
// element kind, so the stored array still decodes into its Go type.
// PostgreSQL holds an array column in its own typed array.
func arrayAppendKeepsElementTypes(t *testing.T, engine Engine) {
	b := sqlBackendOf(engine)
	d := patchDialectOf(t, b)
	ts := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	id := uuid.MustParse("0190a5b4-7c1e-7d8a-9b2c-3d4e5f6a7b8c")
	for _, tc := range []struct {
		name    string
		goType  reflect.Type
		seed    any
		element string
		want    any
	}{
		{"bool", reflect.TypeFor[[]bool](), []bool{false}, `true`, []bool{false, true}},
		{"int", reflect.TypeFor[[]int64](), []int64{1}, `9007199254740993`, []int64{1, 9007199254740993}},
		{"float", reflect.TypeFor[[]float64](), []float64{0.5}, `1.25`, []float64{0.5, 1.25}},
		{"string", reflect.TypeFor[[]string](), []string{"a"}, `"b"`, []string{"a", "b"}},
		{"timestamp", reflect.TypeFor[[]time.Time](), []time.Time{ts}, `"2026-10-01T00:00:00Z"`, []time.Time{ts, ts.Add(12 * time.Hour)}},
		{"uuid", reflect.TypeFor[[]uuid.UUID](), []uuid.UUID{id}, `"` + uuid.Nil.String() + `"`, []uuid.UUID{id, uuid.Nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			db := b.db(t)
			_, err := db.ExecContext(ctx, `CREATE TABLE items (id INTEGER PRIMARY KEY, vals TEXT)`)
			require.NoError(t, err)
			seed, err := json.Marshal(tc.seed)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `INSERT INTO items (id, vals) VALUES (1, ?)`, string(seed))
			require.NoError(t, err)

			schema, err := patch.NewPatchSchema("items", map[string]*patch.PathSchema{"/vals": {
				AllowedOps: []patch.PatchOp{patch.PatchOpArrayAppend},
				DataType:   core.FieldTypeArray, GoType: tc.goType, SQLColumn: "vals",
			}})
			require.NoError(t, err)
			schema.MaxOperations = 5
			parsed, err := patch.NewParser(schema, nil).Parse(fmt.Appendf(nil, `[{"op":"array_append","path":"/vals","value":%s}]`, tc.element))
			require.NoError(t, err)
			res, err := patch.BuildPatchSQL(parsed, d)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, fmt.Sprintf("UPDATE items SET %s WHERE id = %s", res.SetClause, d.Placeholder(res.ParamCount+1)), append(res.Args, 1)...)
			require.NoError(t, err)

			var stored string
			require.NoError(t, db.QueryRowContext(ctx, `SELECT vals FROM items WHERE id = 1`).Scan(&stored))
			got := reflect.New(tc.goType)
			require.NoError(t, json.Unmarshal([]byte(stored), got.Interface()), "stored %s", stored)
			require.Equal(t, tc.want, got.Elem().Interface(), "stored %s", stored)
		})
	}
}

// wrapperTable is how an engine holds a RankedVisit's timestamp: its column
// type, and the value a timestamp column binds.
type wrapperTable struct {
	timestampType string
	stamp         func(sql.NullTime) any
}

func wrapperTableOn(engine Kind) wrapperTable {
	if engine == SQLite {
		// SQLite stores a timestamp as its canonical text.
		table := wrapperTable{timestampType: "DATETIME", stamp: func(at sql.NullTime) any {
			if !at.Valid {
				return nil
			}
			return core.FormatTimestamp(at.Time)
		}}
		return table
	}
	table := wrapperTable{timestampType: "TIMESTAMPTZ", stamp: func(at sql.NullTime) any { return at }}
	return table
}

// A nullable wrapper column's patch has one result in the database and in
// Go: a replace stores the value, Valid, and a remove or a null stores
// NULL, the wrapper's zero value.
func patchNullableWrapperColumns(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db, d, table := b.db(t), patchDialectOf(t, b), wrapperTableOn(b.engine)
		ctx := t.Context()
		_, err := db.ExecContext(ctx, "CREATE TABLE ranked_visits (id BIGINT PRIMARY KEY, score BIGINT, seen_at "+table.timestampType+")")
		require.NoError(t, err)
		gen := core.NewSchemaGenerator()
		schema, err := patch.GeneratePatchSchema[patchtest.RankedVisit](gen)
		require.NoError(t, err)
		applier, err := patch.NewApplier[patchtest.RankedVisit](gen, nil)
		require.NoError(t, err)
		seeded := patchtest.RankedVisit{ID: 1, Score: sql.NullInt64{Int64: 3, Valid: true}, SeenAt: sql.NullTime{Time: patchtest.RankedSeen(), Valid: true}}

		for _, tt := range []struct {
			name  string
			body  string
			merge bool
			want  patchtest.RankedVisit
		}{
			{"replace a score", `[{"op":"replace","path":"/score","value":7}]`, false,
				patchtest.RankedVisit{ID: 1, Score: sql.NullInt64{Int64: 7, Valid: true}, SeenAt: seeded.SeenAt}},
			{"remove a score", `[{"op":"remove","path":"/score"}]`, false,
				patchtest.RankedVisit{ID: 1, SeenAt: seeded.SeenAt}},
			{"replace a time", `[{"op":"replace","path":"/seen_at","value":"2026-05-06T07:08:09Z"}]`, false,
				patchtest.RankedVisit{ID: 1, Score: seeded.Score, SeenAt: sql.NullTime{Time: time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC), Valid: true}}},
			{"remove a time", `[{"op":"remove","path":"/seen_at"}]`, false,
				patchtest.RankedVisit{ID: 1, Score: seeded.Score}},
			{"merge a score and a null time", `{"score":9,"seen_at":null}`, true,
				patchtest.RankedVisit{ID: 1, Score: sql.NullInt64{Int64: 9, Valid: true}}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				ctx := t.Context()
				_, err := db.ExecContext(ctx, "DELETE FROM ranked_visits")
				require.NoError(t, err)
				_, err = db.ExecContext(ctx, "INSERT INTO ranked_visits (id, score, seen_at) VALUES (1, 3, "+d.Placeholder(1)+")", table.stamp(seeded.SeenAt))
				require.NoError(t, err)
				var parsed *patch.ParsedPatch
				if tt.merge {
					parsed, err = patch.ParseMergePatch([]byte(tt.body), schema, nil)
				} else {
					parsed, err = patch.NewParser(schema, nil).Parse([]byte(tt.body))
				}
				require.NoError(t, err)

				built, err := patch.BuildPatchSQL(parsed, d)
				require.NoError(t, err)
				stmt := "UPDATE ranked_visits SET " + built.SetClause + " WHERE id = 1"
				_, err = db.ExecContext(ctx, stmt, built.Args...)
				require.NoError(t, err, stmt)
				var stored patchtest.RankedVisit
				err = db.QueryRowContext(ctx, "SELECT id, score, seen_at FROM ranked_visits").Scan(&stored.ID, &stored.Score, &stored.SeenAt)
				require.NoError(t, err)
				assertRankedVisit(t, tt.want, stored, "stored")

				applied, err := applier.Apply(parsed, seeded)
				require.NoError(t, err)
				assertRankedVisit(t, tt.want, applied, "applied")
			})
		}
	})
}

// assertRankedVisit compares times as instants: a driver reads them back in
// its own location.
func assertRankedVisit(t *testing.T, want, got patchtest.RankedVisit, what string) {
	t.Helper()
	assert.Equal(t, want.ID, got.ID, what)
	assert.Equal(t, want.Score, got.Score, what)
	assert.Equal(t, want.SeenAt.Valid, got.SeenAt.Valid, what)
	assert.True(t, want.SeenAt.Time.Equal(got.SeenAt.Time), "%s: seen_at %s, want %s", what, got.SeenAt.Time, want.SeenAt.Time)
}

// labelDocs is a docs table on one database, its doc column holding JSON
// documents, and the label a member of them is named.
type labelDocs struct {
	db      *sql.DB
	d       patchDialect
	label   string
	storage core.SQLType
	path    filter.JSONStorage
}

// A document that encoding/json wrote, with a member named label, is read
// through a filter, which inlines its path, and its member replaced,
// removed and added through patches, which bind theirs: each finds the
// member the stored text names, and the member the add writes decodes to
// the label exactly, once. A label escaped wrongly on both sides alike
// would pass a test that wrote and read through parse alone. A filter's
// declared members are names alone (filter.MaxPathMemberLen), so its path
// is built by hand.
//
// The labels hold what a path or a literal must escape somewhere: a
// single quote (an SQL literal), "." and "[" (SQLite's path steps), "$",
// and "<", ">" and "&", which encoding/json writes as \u003c, \u003e and
// \u0026. On PostgreSQL the label also holds a double quote and a
// backslash, which an escape string literal doubles; SQLite writes those
// too (BuildPatchSQL_SQLiteWritesAQuoteOrBackslashInAMember).
func pathLabelsReachTheMemberEncodingJSONNames(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		docs := labelDocs{db: b.db(t), d: patchDialectOf(t, b), label: `it's a.b[0]$<&>`, storage: core.SQLJSON, path: filter.StorageText}
		create := `CREATE TABLE docs (id INTEGER PRIMARY KEY, doc TEXT)`
		if b.engine == Postgres {
			docs.label, docs.storage, docs.path = `q"u\o'te a.b[0]$<&>`, core.SQLJSONB, filter.StorageJSONB
			create = `CREATE TABLE docs (id BIGINT PRIMARY KEY, doc JSONB)`
		}
		ctx := t.Context()
		_, err := docs.db.ExecContext(ctx, create)
		require.NoError(t, err)
		seeded, err := json.Marshal(map[string]any{docs.label: "seeded"})
		require.NoError(t, err)
		_, err = docs.db.ExecContext(ctx, `INSERT INTO docs (id, doc) VALUES (1, `+docs.d.Placeholder(1)+`), (2, '{"other":1}')`, string(seeded))
		require.NoError(t, err)

		assert.Equal(t, []int{1}, docs.selecting(t, "seeded"))

		docs.patch(t, 1, `"replace"`, `"replaced"`)
		assert.Equal(t, []docMember{{docs.label, "replaced"}}, docs.members(t, 1))

		docs.patch(t, 1, `"remove"`, "")
		assert.Empty(t, docs.members(t, 1))

		docs.patch(t, 2, `"add"`, `"added"`)
		assert.ElementsMatch(t, []docMember{{"other", float64(1)}, {docs.label, "added"}}, docs.members(t, 2))
		assert.Equal(t, []int{2}, docs.selecting(t, "added"))
	})
}

// patch applies the operation op on the member to the row id, its value
// value when not empty, and requires it to write the row: the member's
// precondition held.
func (l labelDocs) patch(t *testing.T, id int, op, value string) {
	t.Helper()
	body := `[{"op":` + op + `,"path":` + strconv.Quote(core.FormatPointer("doc", l.label))
	if value != "" {
		body += `,"value":` + value
	}
	body += `}]`
	parsed, err := patch.NewParser(memberSchema(l.storage), nil).Parse([]byte(body))
	require.NoError(t, err)
	set, err := patch.BuildPatchSQL(parsed, l.d)
	require.NoError(t, err)
	update := "UPDATE docs SET " + set.SetClause + " WHERE id = " + strconv.Itoa(id)
	if set.Precondition != "" {
		update += " AND " + set.Precondition
	}
	res, err := l.db.ExecContext(t.Context(), update, set.Args...)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "%s of %q: its precondition held of row %d", op, l.label, id)
}

// selecting is the rows whose member named label is value, as a filter
// reads it.
func (l labelDocs) selecting(t *testing.T, value string) []int {
	t.Helper()
	field := &filter.FieldSchema{ExternalName: "member", DataType: core.FieldTypeString,
		Path: &filter.JSONPath{Table: "docs", Column: "doc", Segments: []string{l.label}, Storage: l.path}}
	var args []any
	cond, err := l.d.Condition(field, filter.OpEq, value, func(v any) string {
		args = append(args, v)
		return l.d.Placeholder(len(args))
	})
	require.NoError(t, err)
	return dbtest.QueryRows[int](t, l.db, "SELECT id FROM docs WHERE "+cond+" ORDER BY id", args...)
}

// docMember is a stored document's member, its name as the stored text
// decodes and its value.
type docMember struct {
	name  string
	value any
}

// members are row id's document members, each as often as the text holds
// it: a member written beside one it failed to find appears twice.
func (l labelDocs) members(t *testing.T, id int) []docMember {
	t.Helper()
	var text string
	require.NoError(t, l.db.QueryRowContext(t.Context(), "SELECT doc FROM docs WHERE id = "+strconv.Itoa(id)).Scan(&text))
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	tok, err := dec.Token()
	require.NoError(t, err)
	require.Equal(t, json.Delim('{'), tok, text)
	var out []docMember
	for dec.More() {
		tok, err := dec.Token()
		require.NoError(t, err)
		name, ok := tok.(string)
		require.True(t, ok, "member name %v in %s", tok, text)
		var value any
		require.NoError(t, dec.Decode(&value))
		out = append(out, docMember{name, value})
	}
	return out
}
