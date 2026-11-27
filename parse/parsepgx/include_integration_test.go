//go:build integration

package parsepgx_test

import (
	"context"
	"testing"

	"github.com/ashtonian/pkg/parse/parsepgx/internal/pgtest"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// Through a native pgx connection — the production driver — UUID
// include keys come back in the canonical text items carry.
func TestIncludeQuery_NativeUUIDKeysAreCanonicalText(t *testing.T) {
	ctx := t.Context()
	conn, err := pgx.ConnectConfig(ctx, pgtest.PostgresConfig(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(ctx)) })
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
