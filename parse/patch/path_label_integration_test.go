//go:build integration

package patch_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/patch"
)

// labelDB is a docs table on one database, its doc column holding JSON
// documents, and the label a member of them is named.
type labelDB struct {
	db      *sql.DB
	d       filter.Dialect
	label   string
	storage core.SQLType
	path    filter.JSONStorage
}

// TestPathLabels_ReachTheMemberEncodingJSONNames seeds a document that
// encoding/json wrote, with a member named label, then reads that member
// through a filter, which inlines its path, and replaces, removes and adds
// it through patches, which bind theirs: each finds the member the stored
// text names, and the member the add writes decodes to the label exactly,
// once. A label escaped wrongly on both sides alike would pass a test that
// wrote and read through parse alone. A filter's declared members are
// names alone (filter.MaxPathMemberLen), so its path is built by hand.
//
// The labels hold what a path or a literal must escape somewhere: a
// single quote (an SQL literal), "." and "[" (SQLite's path steps), "$",
// and "<", ">" and "&", which encoding/json writes as \u003c, \u003e and
// \u0026. On PostgreSQL the label also holds a double quote and a
// backslash, which an escape string literal doubles; SQLite refuses a
// member holding either
// (TestBuildPatchSQL_SQLiteRefusesAQuoteOrBackslashInAMember).
func TestPathLabels_ReachTheMemberEncodingJSONNames(t *testing.T) {
	for _, tc := range []struct {
		name   string
		open   func(testing.TB) *sql.DB
		schema string
		db     labelDB
	}{
		{"sqlite", dbtest.SQLite, `CREATE TABLE docs (id INTEGER PRIMARY KEY, doc TEXT)`,
			labelDB{d: filter.SQLiteDialect{}, label: `it's a.b[0]$<&>`, storage: core.SQLJSON, path: filter.StorageText}},
		{"postgres", dbtest.Postgres, `CREATE TABLE docs (id BIGINT PRIMARY KEY, doc JSONB)`,
			labelDB{d: filter.PostgresDialect{}, label: `q"u\o'te a.b[0]$<&>`, storage: core.SQLJSONB, path: filter.StorageJSONB}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ldb := tc.db
			ldb.db = tc.open(t)
			_, err := ldb.db.ExecContext(t.Context(), tc.schema)
			require.NoError(t, err)
			seeded, err := json.Marshal(map[string]any{ldb.label: "seeded"})
			require.NoError(t, err)
			_, err = ldb.db.ExecContext(t.Context(), `INSERT INTO docs (id, doc) VALUES (1, `+ldb.d.Placeholder(1)+`), (2, '{"other":1}')`, string(seeded))
			require.NoError(t, err)

			assert.Equal(t, []int{1}, ldb.selecting(t, "seeded"))

			ldb.patch(t, 1, `"replace"`, `"replaced"`)
			assert.Equal(t, []member{{ldb.label, "replaced"}}, ldb.members(t, 1))

			ldb.patch(t, 1, `"remove"`, "")
			assert.Empty(t, ldb.members(t, 1))

			ldb.patch(t, 2, `"add"`, `"added"`)
			assert.ElementsMatch(t, []member{{"other", float64(1)}, {ldb.label, "added"}}, ldb.members(t, 2))
			assert.Equal(t, []int{2}, ldb.selecting(t, "added"))
		})
	}
}

// patch applies the operation op on the member to the row id, its value
// value when not empty, and requires it to write the row: the member's
// precondition held.
func (l labelDB) patch(t *testing.T, id int, op, value string) {
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
func (l labelDB) selecting(t *testing.T, value string) []int {
	t.Helper()
	field := &filter.FieldSchema{ExternalName: "member", DataType: core.FieldTypeString,
		Path: &filter.JSONPath{Table: "docs", Column: "doc", Segments: []string{l.label}, Storage: l.path}}
	var args []any
	cond, err := l.d.Condition(field, filter.OpEq, value, func(v any) string {
		args = append(args, v)
		return l.d.Placeholder(len(args))
	})
	require.NoError(t, err)
	rows, err := l.db.QueryContext(t.Context(), "SELECT id FROM docs WHERE "+cond+" ORDER BY id", args...)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var ids []int
	for rows.Next() {
		var id int
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

// member is a stored document's member, its name as the stored text
// decodes and its value.
type member struct {
	name  string
	value any
}

// members are row id's document members, each as often as the text holds
// it: a member written beside one it failed to find appears twice.
func (l labelDB) members(t *testing.T, id int) []member {
	t.Helper()
	var text string
	require.NoError(t, l.db.QueryRowContext(t.Context(), "SELECT doc FROM docs WHERE id = "+strconv.Itoa(id)).Scan(&text))
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	tok, err := dec.Token()
	require.NoError(t, err)
	require.Equal(t, json.Delim('{'), tok, text)
	var out []member
	for dec.More() {
		tok, err := dec.Token()
		require.NoError(t, err)
		name, ok := tok.(string)
		require.True(t, ok, "member name %v in %s", tok, text)
		var value any
		require.NoError(t, dec.Decode(&value))
		out = append(out, member{name, value})
	}
	return out
}
