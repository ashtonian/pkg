//go:build integration

package corpus

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/patchtest"
	"github.com/ashtonian/pkg/parse/patch"
)

// The cases holding patch's Applier, which patches a Go value, to the row
// the same patch's SQL leaves.

// applyTable is the table a patchtest.ApplyRow is stored in.
func applyTable() sqlTable {
	table := sqlTable{name: "apply_rows", key: []string{"id"}, columns: []sqlColumn{
		column[int64]("id"),
		column[string]("name").required(),
		column[*string]("note"),
		column[int32]("rank").required(),
		column[[]string]("tags").required(),
		column[map[string]any]("meta").required(),
		column[patchtest.ApplyDims]("dims").required(),
		column[map[string]string]("labels").required(),
	}}
	return table
}

func applySeed() patchtest.ApplyRow {
	note := "n"
	row := patchtest.ApplyRow{
		ID: 1, Name: "orig", Note: &note, Rank: 1, Tags: []string{"a"},
		Meta:   map[string]any{"k": 1.0, "nested": map[string]any{"x": 1.0, "y": 2.0}},
		Dims:   patchtest.ApplyDims{W: 2, H: 7},
		Labels: map[string]string{"old": "v"},
	}
	return row
}

// loadApplySeed creates the apply_rows table on b's database db holding
// applySeed.
func loadApplySeed(t *testing.T, b sqlBackend, db *sql.DB) {
	t.Helper()
	table := applyTable()
	b.load(t, db, table, recordRows(t, table, []patchtest.ApplyRow{applySeed()}))
}

// Every patch that compiles to SQL leaves the same row whether it is applied
// to a Go value or executed on the database.
func applierAgreesWithTheDatabase(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		d := patchDialectOf(t, b)
		gen := core.NewSchemaGenerator()
		schema := patchtest.MemberPatchSchema(t, gen)
		applier, err := patch.NewApplier[patchtest.ApplyRow](gen, nil)
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
			{"members of a removed map", false, `[{"op":"remove","path":"/meta"},{"op":"add","path":"/meta/x","value":1}]`},
			{"members of a map cleared with null", false, `[{"op":"replace","path":"/meta","value":null},{"op":"add","path":"/meta/x","value":1}]`},
			{"members of a removed typed map", false, `[{"op":"remove","path":"/labels"},{"op":"add","path":"/labels/a","value":"b"}]`},
			{"a member, then the map cleared with null", false, `[{"op":"add","path":"/meta/x","value":1},{"op":"replace","path":"/meta","value":null}]`},
			{"a member, then the map removed", false, `[{"op":"add","path":"/meta/x","value":1},{"op":"remove","path":"/meta"}]`},
			{"remove struct members the type omits when zero", false, `[{"op":"add","path":"/dims/note","value":"x"},{"op":"add","path":"/dims/at","value":3},{"op":"remove","path":"/dims/note"},{"op":"remove","path":"/dims/at"}]`},
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

				db := b.db(t)
				loadApplySeed(t, b, db)
				res, err := patch.BuildPatchSQL(parsed, d)
				require.NoError(t, err)
				stmt := "UPDATE apply_rows SET " + res.SetClause + " WHERE id = " + d.Placeholder(res.ParamCount+1)
				if res.Precondition != "" {
					stmt += " AND " + res.Precondition
				}
				_, err = db.ExecContext(t.Context(), stmt, append(res.Args, seed.ID)...)
				require.NoError(t, err, stmt)

				assert.Equal(t, goRow, storedDocument(t, b, db, seed.ID))
			})
		}
	})
}

// An operation the document does not take fails the patch with its own
// index, in a run of operations as alone, and the database names the same
// one.
func applierFailingMemberOperationAgreesWithTheDatabase(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		d := patchDialectOf(t, b)
		gen := core.NewSchemaGenerator()
		schema := patchtest.MemberPatchSchema(t, gen)
		applier, err := patch.NewApplier[patchtest.ApplyRow](gen, nil)
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

		ctx := t.Context()
		db := b.db(t)
		loadApplySeed(t, b, db)
		res, err := patch.BuildPatchSQL(parsed, d)
		require.NoError(t, err)
		stmt := "UPDATE apply_rows SET " + res.SetClause + " WHERE id = " + d.Placeholder(res.ParamCount+1) + " AND " + res.Precondition
		result, err := db.ExecContext(ctx, stmt, append(res.Args, seed.ID)...)
		require.NoError(t, err, stmt)
		written, err := result.RowsAffected()
		require.NoError(t, err)
		assert.Zero(t, written)
		var args []any
		_, firstUnmet, err := patch.RenderPreconditions(d, res.Preconditions, func(v any) string {
			args = append(args, v)
			return d.Placeholder(len(args))
		})
		require.NoError(t, err)
		var unmet int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT "+firstUnmet+" FROM apply_rows WHERE id = 1", args...).Scan(&unmet))
		assert.Equal(t, *pe.Index, unmet)
	})
}

// rowDocument is a row's patchable fields as one JSON document.
func rowDocument(t *testing.T, r patchtest.ApplyRow) map[string]any {
	t.Helper()
	data, err := json.Marshal(r)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	delete(doc, "id")
	return doc
}

// storedDocument reads a row back as rowDocument writes it.
func storedDocument(t *testing.T, b sqlBackend, db *sql.DB, id int64) map[string]any {
	t.Helper()
	query := `SELECT name, note, rank, tags, meta, dims, labels FROM apply_rows WHERE id = ` + b.dialect.Placeholder(1)
	if b.engine == Postgres {
		query = `SELECT name, note, rank, array_to_json(tags)::text, meta::text, dims::text, labels::text FROM apply_rows WHERE id = $1`
	}
	var (
		name                     string
		note                     sql.NullString
		rank                     int64
		tags, meta, dims, labels string
	)
	require.NoError(t, db.QueryRowContext(t.Context(), query, id).Scan(&name, &note, &rank, &tags, &meta, &dims, &labels))
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
// in a declared JSON column as in an undeclared one, and Go and the
// database agree.
func applierMergesIntoARawMessageMember(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		d := patchDialectOf(t, b)
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

				ctx := t.Context()
				db := b.db(t)
				table := sqlTable{name: "raw_member_rows", key: []string{"id"}, columns: []sqlColumn{column[int64]("id"), column[json.RawMessage]("doc").required()}}
				b.load(t, db, table, [][]any{{1, json.RawMessage(`{"r":{"k":2,"z":3},"n":1}`)}})
				res, err := patch.BuildPatchSQL(parsed, d)
				require.NoError(t, err)
				_, err = db.ExecContext(ctx, "UPDATE raw_member_rows SET "+res.SetClause+" WHERE id = "+d.Placeholder(res.ParamCount+1), append(res.Args, 1)...)
				require.NoError(t, err)
				read := `SELECT doc FROM raw_member_rows WHERE id = 1`
				if b.engine == Postgres {
					read = `SELECT doc::text FROM raw_member_rows WHERE id = 1`
				}
				var stored string
				require.NoError(t, db.QueryRowContext(ctx, read).Scan(&stored))
				assert.JSONEq(t, want, stored, "the database")
			})
		}
	})
}
