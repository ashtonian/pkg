//go:build unit

package patch_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/patch"
)

type applyDims struct {
	W int `json:"w"`
	H int `json:"h"`
}

type applyRow struct {
	ID     int64             `json:"id" db:"id"`
	Name   string            `json:"name" db:"name" parse:"patch"`
	Note   *string           `json:"note" db:"note" parse:"patch"`
	Rank   int32             `json:"rank" db:"rank" parse:"patch"`
	Tags   []string          `json:"tags" db:"tags" parse:"patch"`
	Meta   map[string]any    `json:"meta" db:"meta" parse:"patch"`
	Dims   applyDims         `json:"dims" db:"dims" parse:"patch"`
	Labels map[string]string `json:"labels" db:"labels" parse:"patch"`
}

// Every patch that compiles to SQL leaves the same row whether it is applied
// to a Go value or executed on SQLite.
func TestApplier_AgreesWithSQLite(t *testing.T) {
	gen := core.NewSchemaGenerator()
	schema := memberPatchSchema(t, gen)
	applier, err := patch.NewApplier[applyRow](gen, nil)
	require.NoError(t, err)
	seed := applySeed()
	for _, tc := range []struct {
		name  string
		merge bool
		body  string
	}{
		{"merge scalars", true, `{"name":"new","rank":5}`},
		{"merge null clears", true, `{"note":null}`},
		{"merge into a document", true, `{"meta":{"nested":{"x":null,"z":3},"k2":"v","list":[1,2]}}`},
		{"merge into a struct document", true, `{"dims":{"w":9}}`},
		{"merge into a typed map", true, `{"labels":{"old":null,"new":"x"}}`},
		{"replace an array", false, `[{"op":"replace","path":"/tags","value":["x","y"]}]`},
		{"append", false, `[{"op":"array_append","path":"/tags","value":"b"}]`},
		{"replace then append", false, `[{"op":"replace","path":"/tags","value":["x"]},{"op":"array_append","path":"/tags","value":"y"}]`},
		{"remove an array", false, `[{"op":"remove","path":"/tags"}]`},
		{"remove a map", false, `[{"op":"remove","path":"/meta"}]`},
		{"replace a document", false, `[{"op":"replace","path":"/meta","value":{"a":1}}]`},
		{"replace a struct document", false, `[{"op":"replace","path":"/dims","value":{"w":1}}]`},
		{"replace twice", false, `[{"op":"replace","path":"/name","value":"a"},{"op":"replace","path":"/name","value":"b"}]`},
		{"replace with null", false, `[{"op":"replace","path":"/note","value":null}]`},
		{"replace a document member", false, `[{"op":"replace","path":"/meta/k","value":2}]`},
		{"add and remove nested document members", false, `[{"op":"add","path":"/meta/nested/z","value":3},{"op":"remove","path":"/meta/nested/x"}]`},
		{"replace a struct document member", false, `[{"op":"replace","path":"/dims/w","value":9}]`},
		{"add and remove typed map members", false, `[{"op":"add","path":"/labels/new","value":"x"},{"op":"remove","path":"/labels/old"}]`},
		{"members of a document the patch wrote whole", false, `[{"op":"replace","path":"/meta","value":{"a":1}},{"op":"add","path":"/meta/b","value":2}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var parsed *patch.ParsedPatch
			if tc.merge {
				parsed, err = patch.ParseMergePatch([]byte(tc.body), schema, nil)
			} else {
				parsed, err = patch.NewParser(schema, nil).Parse([]byte(tc.body))
			}
			require.NoError(t, err)

			applied, err := applier.Apply(parsed, seed)
			require.NoError(t, err)
			goRow := rowDocument(t, applied)

			db := dbtest.SQLite(t)
			_, err = db.Exec(`CREATE TABLE rows (id INTEGER PRIMARY KEY, name TEXT NOT NULL, note TEXT, rank INTEGER NOT NULL, tags TEXT NOT NULL, meta TEXT NOT NULL, dims TEXT NOT NULL, labels TEXT NOT NULL)`)
			require.NoError(t, err)
			insertRow(t, db, seed)
			res, err := patch.BuildPatchSQL(parsed, filter.SQLiteDialect{})
			require.NoError(t, err)
			stmt := fmt.Sprintf("UPDATE rows SET %s WHERE id = ?%d", res.SetClause, res.ParamCount+1)
			if res.Precondition != "" {
				stmt += " AND " + res.Precondition
			}
			_, err = db.Exec(stmt, append(res.Args, seed.ID)...)
			require.NoError(t, err, stmt)

			assert.Equal(t, goRow, storedDocument(t, db, seed.ID))
		})
	}
}

// A ParsedPatch no parser produced is refused, never applied or compiled.
func TestApplier_RefusesUnparsedPatches(t *testing.T) {
	gen := core.NewSchemaGenerator()
	applier, err := patch.NewApplier[applyRow](gen, nil)
	require.NoError(t, err)
	byHand := []*patch.ParsedPatch{
		nil,
		{Operations: []*patch.PatchOperation{{Op: patch.PatchOpReplace, Path: "/name", Value: "x"}}},
	}
	for _, p := range byHand {
		_, err := applier.Apply(p, applyRow{})
		assert.True(t, errors.Is(err, patch.ErrUnparsed), "Apply: %v", err)
		_, err = patch.BuildPatchSQL(p, filter.SQLiteDialect{})
		assert.True(t, errors.Is(err, patch.ErrUnparsed), "BuildPatchSQL: %v", err)
		err = patch.ApplyPatchPolicyConstraints(p, nil, nil)
		assert.True(t, errors.Is(err, patch.ErrUnparsed), "ApplyPatchPolicyConstraints: %v", err)
	}
}

func insertRow(t *testing.T, db *sql.DB, r applyRow) {
	t.Helper()
	encode := func(v any) string {
		data, err := json.Marshal(v)
		require.NoError(t, err)
		return string(data)
	}
	_, err := db.Exec(`INSERT INTO rows (id, name, note, rank, tags, meta, dims, labels) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, r.Note, r.Rank, encode(r.Tags), encode(r.Meta), encode(r.Dims), encode(r.Labels))
	require.NoError(t, err)
}

// memberPatchSchema is applyRow's generated patch schema, with add, replace
// and remove allowed on members of its documents.
func memberPatchSchema(t *testing.T, gen *core.SchemaGenerator) *patch.PatchSchema {
	t.Helper()
	schema, err := patch.GeneratePatchSchema[applyRow](gen)
	require.NoError(t, err)
	ops := []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpReplace, patch.PatchOpRemove}
	for path, column := range map[string]string{"/meta/**": "meta", "/dims/*": "dims", "/labels/*": "labels"} {
		require.NoError(t, schema.AddPath(path, &patch.PathSchema{AllowedOps: ops, DataType: core.FieldTypeAny, SQLColumn: column}))
	}
	return schema
}

func applySeed() applyRow {
	note := "n"
	return applyRow{
		ID: 1, Name: "orig", Note: &note, Rank: 1, Tags: []string{"a"},
		Meta:   map[string]any{"k": 1.0, "nested": map[string]any{"x": 1.0, "y": 2.0}},
		Dims:   applyDims{W: 2, H: 7},
		Labels: map[string]string{"old": "v"},
	}
}

// An operation the document does not take fails the patch with its own
// index, in a run of operations as alone, and SQLite names the same one.
func TestApplier_FailingMemberOperationAgreesWithSQLite(t *testing.T) {
	gen := core.NewSchemaGenerator()
	schema := memberPatchSchema(t, gen)
	applier, err := patch.NewApplier[applyRow](gen, nil)
	require.NoError(t, err)
	seed := applySeed()
	parsed, err := patch.NewParser(schema, nil).Parse([]byte(`[{"op":"replace","path":"/name","value":"x"},{"op":"replace","path":"/meta/k","value":2},{"op":"remove","path":"/labels/gone"},{"op":"remove","path":"/meta/zz"}]`))
	require.NoError(t, err)

	_, err = applier.Apply(parsed, seed)
	pe, ok := errors.AsType[*core.ParseError](err)
	require.True(t, ok, "%v is no ParseError", err)
	assert.Equal(t, patch.ECodePatchValueInvalid.String(), pe.Code)
	require.NotNil(t, pe.Index)
	assert.Equal(t, 2, *pe.Index)

	db := dbtest.SQLite(t)
	_, err = db.Exec(`CREATE TABLE rows (id INTEGER PRIMARY KEY, name TEXT NOT NULL, note TEXT, rank INTEGER NOT NULL, tags TEXT NOT NULL, meta TEXT NOT NULL, dims TEXT NOT NULL, labels TEXT NOT NULL)`)
	require.NoError(t, err)
	insertRow(t, db, seed)
	res, err := patch.BuildPatchSQL(parsed, filter.SQLiteDialect{})
	require.NoError(t, err)
	result, err := db.Exec(fmt.Sprintf("UPDATE rows SET %s WHERE id = ?%d AND %s", res.SetClause, res.ParamCount+1, res.Precondition), append(res.Args, seed.ID)...)
	require.NoError(t, err)
	written, err := result.RowsAffected()
	require.NoError(t, err)
	assert.Zero(t, written)
	var args []any
	_, firstUnmet, err := patch.RenderPreconditions(filter.SQLiteDialect{}, res.Preconditions, func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("?%d", len(args))
	})
	require.NoError(t, err)
	var unmet int
	require.NoError(t, db.QueryRow("SELECT "+firstUnmet+" FROM rows WHERE id = 1", args...).Scan(&unmet))
	assert.Equal(t, *pe.Index, unmet)
}

// rowDocument is a row's patchable fields as one JSON document.
func rowDocument(t *testing.T, r applyRow) map[string]any {
	t.Helper()
	data, err := json.Marshal(r)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	delete(doc, "id")
	return doc
}

// storedDocument reads a row back as rowDocument writes it.
func storedDocument(t *testing.T, db *sql.DB, id int64) map[string]any {
	t.Helper()
	var (
		name                     string
		note                     sql.NullString
		rank                     int64
		tags, meta, dims, labels string
	)
	require.NoError(t, db.QueryRow(`SELECT name, note, rank, tags, meta, dims, labels FROM rows WHERE id = ?`, id).
		Scan(&name, &note, &rank, &tags, &meta, &dims, &labels))
	doc := map[string]any{"name": name, "note": nil, "rank": float64(rank)}
	if note.Valid {
		doc["note"] = note.String
	}
	for column, text := range map[string]string{"tags": tags, "meta": meta, "dims": dims, "labels": labels} {
		var v any
		require.NoError(t, json.Unmarshal([]byte(text), &v), column)
		doc[column] = v
	}
	return doc
}

type rawMemberDoc struct {
	R json.RawMessage `json:"r"`
	N int             `json:"n"`
}

type rawMemberRow struct {
	ID  int64        `json:"id" db:"id"`
	Doc rawMemberDoc `json:"doc" db:"doc" parse:"patch"`
}

// A json.RawMessage member is an untyped document: an object merges into it,
// in a declared JSON column as in an undeclared one, and Go and SQLite agree.
func TestApplier_MergesIntoARawMessageMember(t *testing.T) {
	for name, gen := range map[string]*core.SchemaGenerator{
		"undeclared": core.NewSchemaGenerator(),
		"declared":   core.NewSchemaGenerator(core.WithColumnTypes(map[string]core.SQLType{"doc": core.SQLJSONB})),
	} {
		t.Run(name, func(t *testing.T) {
			schema, err := patch.GeneratePatchSchema[rawMemberRow](gen)
			require.NoError(t, err)
			applier, err := patch.NewApplier[rawMemberRow](gen, nil)
			require.NoError(t, err)
			parsed, err := patch.ParseMergePatch([]byte(`{"doc":{"r":{"q":1,"k":null}}}`), schema, nil)
			require.NoError(t, err)

			seed := rawMemberRow{ID: 1, Doc: rawMemberDoc{R: json.RawMessage(`{"k":2,"z":3}`), N: 1}}
			applied, err := applier.Apply(parsed, seed)
			require.NoError(t, err)
			want := `{"r":{"q":1,"z":3},"n":1}`
			got, err := json.Marshal(applied.Doc)
			require.NoError(t, err)
			assert.JSONEq(t, want, string(got), "Go")

			db := dbtest.SQLite(t)
			_, err = db.Exec(`CREATE TABLE raw_member_rows (id INTEGER PRIMARY KEY, doc TEXT NOT NULL)`)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO raw_member_rows (id, doc) VALUES (1, ?)`, `{"r":{"k":2,"z":3},"n":1}`)
			require.NoError(t, err)
			res, err := patch.BuildPatchSQL(parsed, filter.SQLiteDialect{})
			require.NoError(t, err)
			_, err = db.Exec("UPDATE raw_member_rows SET "+res.SetClause+fmt.Sprintf(" WHERE id = ?%d", res.ParamCount+1), append(res.Args, 1)...)
			require.NoError(t, err)
			var stored string
			require.NoError(t, db.QueryRow(`SELECT doc FROM raw_member_rows WHERE id = 1`).Scan(&stored))
			assert.JSONEq(t, want, stored, "SQLite")
		})
	}
}
