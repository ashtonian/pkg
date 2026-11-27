//go:build integration

package parse_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// JSON integers at the edge of the portable domain select exactly the
// matching document on SQLite and PostgreSQL — no neighbour rounds onto it.
func TestJSONIntegerEdges_SameRowsOnEveryBackend(t *testing.T) {
	schema := &filter.ResourceSchema{Name: "docs", Fields: map[string]*filter.FieldSchema{
		"doc": {ExternalName: "doc", Table: "docs", Column: "doc", DataType: core.FieldTypeJSON, AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpContains}},
	}}
	stored := []string{`{"n":9223372036854775807}`, `{"n":9223372036854775806}`, `{"n":18446744073709551615}`, `{"n":-9223372036854775808}`}
	queries := map[string][]int{
		`{"doc":{"n":9223372036854775807}}`:                {1},
		`{"doc":{"$contains":{"n":-9223372036854775808}}}`: {4},
	}
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		db := b.open(t)
		rows := make([][]any, len(stored))
		for i, doc := range stored {
			rows[i] = []any{i + 1, doc}
		}
		b.load(t, db, sqlTable{name: "docs", columns: []sqlColumn{column[int]("id"), column[json.RawMessage]("doc")}}, rows)
		for q, want := range queries {
			ast, err := filter.NewFilterParser(schema, nil).Parse(q)
			require.NoError(t, err, q)
			res, err := filter.NewSQLBuilder(schema, b.dialect).Build(ast)
			require.NoError(t, err, q)
			assert.Equal(t, want, queryRows[int](t, db, "SELECT id FROM docs WHERE "+res.Where+" ORDER BY id", res.Args...), q)
		}
	})
}

// Through a native pgx connection — the production driver — UUID
// include keys come back in the canonical text items carry.
func TestIncludeQuery_NativePgxUUIDKeysAreCanonicalText(t *testing.T) {
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, dbtest.PostgresConfig(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(ctx) })
	_, err = conn.Exec(ctx, `CREATE TABLE widget_tags (widget_id UUID, tag_id UUID)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO widget_tags VALUES
		('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-00000000000a'),
		('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-00000000000b')`)
	require.NoError(t, err)

	field := &filter.FieldSchema{ExternalName: "tags", Includable: true, Relation: &core.RelationSchema{
		JoinTable: "widget_tags", SourceColumns: []string{"widget_id"}, TargetColumns: []string{"tag_id"},
		SourceTypes: []core.TypeInfo{{Type: core.FieldTypeUUID}}, TargetTypes: []core.TypeInfo{{Type: core.FieldTypeUUID}},
	}, RelatedBy: []*filter.FieldSchema{{ExternalName: "id", Table: "widgets", Column: "id", DataType: core.FieldTypeUUID}}}
	q, err := filter.BuildIncludeQuery(field, filter.PostgresDialect{}, []core.Key{{"00000000-0000-0000-0000-000000000001"}}, filter.WithIncludeRelationScope(filter.RelationScope{Unscoped: true}))
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
