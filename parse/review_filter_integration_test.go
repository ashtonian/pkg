//go:build integration

package parse_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// N02: JSON integers at the edge of the portable domain select exactly the
// matching document on SQLite and PostgreSQL — no neighbour rounds onto it.
func TestJSONIntegerEdges_SameRowsOnEveryBackend(t *testing.T) {
	schema := &filter.ResourceSchema{Name: "docs", Fields: map[string]*filter.FieldSchema{
		"doc": {ExternalName: "doc", SQLExpr: "docs.doc", DataType: core.FieldTypeJSON, AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpContains}},
	}}
	stored := []string{`{"n":9223372036854775807}`, `{"n":9223372036854775806}`, `{"n":18446744073709551615}`, `{"n":-9223372036854775808}`}
	queries := map[string][]int{
		`{"doc":{"n":9223372036854775807}}`:                {1},
		`{"doc":{"$contains":{"n":-9223372036854775808}}}`: {4},
	}
	for name, open := range map[string]func(*testing.T) (*sql.DB, filter.Dialect, string){
		"sqlite": func(t *testing.T) (*sql.DB, filter.Dialect, string) {
			return dbtest.SQLite(t), filter.SQLiteDialect{}, "TEXT"
		},
		"postgres": func(t *testing.T) (*sql.DB, filter.Dialect, string) {
			return dbtest.Postgres(t), filter.PostgresDialect{}, "JSONB"
		},
	} {
		t.Run(name, func(t *testing.T) {
			db, d, docType := open(t)
			_, err := db.Exec("CREATE TABLE docs (id INT, doc " + docType + ")")
			require.NoError(t, err)
			for i, doc := range stored {
				_, err := db.Exec(fmt.Sprintf("INSERT INTO docs VALUES (%s, %s)", d.Placeholder(1), d.Placeholder(2)), i+1, doc)
				require.NoError(t, err)
			}
			for q, want := range queries {
				ast, err := filter.NewFilterParser(schema, nil).Parse(q)
				require.NoError(t, err, q)
				res, err := filter.NewSQLBuilder(schema, d).Build(ast)
				require.NoError(t, err, q)
				assert.Equal(t, want, queryInts(t, db, "SELECT id FROM docs WHERE "+res.Where+" ORDER BY id", res.Args), q)
			}
		})
	}
}

func queryInts(t *testing.T, db *sql.DB, q string, args []any) []int {
	t.Helper()
	rows, err := db.Query(q, args...)
	require.NoError(t, err)
	defer rows.Close()
	var out []int
	for rows.Next() {
		var id int
		require.NoError(t, rows.Scan(&id))
		out = append(out, id)
	}
	require.NoError(t, rows.Err())
	return out
}

// C04: through a native pgx connection — the production driver — UUID
// include keys come back in the canonical text items carry.
func TestIncludeQuery_NativePgxUUIDKeysAreCanonicalText(t *testing.T) {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbtest.PostgresDSN(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(ctx) })
	_, err = conn.Exec(ctx, `CREATE TABLE widget_tags (widget_id UUID, tag_id UUID)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO widget_tags VALUES
		('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-00000000000a'),
		('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-00000000000b')`)
	require.NoError(t, err)

	field := &filter.FieldSchema{ExternalName: "tags", Includable: true, Relation: &core.RelationSchema{
		JoinTable: "widget_tags", SourceColumn: "widget_id", TargetColumn: "tag_id",
	}}
	q, err := filter.BuildIncludeQuery(field, filter.PostgresDialect{}, []any{"00000000-0000-0000-0000-000000000001"}, filter.WithIncludeUnscoped())
	require.NoError(t, err)

	rows, err := conn.Query(ctx, q.SQL, q.Args...)
	require.NoError(t, err)
	defer rows.Close()

	res, err := q.Scan(rows)
	require.NoError(t, err)
	got := res.IDs
	assert.Equal(t, map[string][]string{
		"00000000-0000-0000-0000-000000000001": {"00000000-0000-0000-0000-00000000000a", "00000000-0000-0000-0000-00000000000b"},
	}, got)
}
