//go:build integration

package filter_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

func TestBuildIncludeQuery_TextKeysUseCodePointOrder(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(t.Context(), `CREATE TABLE pairs (owner TEXT, target TEXT COLLATE NOCASE);
		INSERT INTO pairs VALUES ('owner', 'a'), ('owner', 'Z'), ('owner', 'B'), ('owner', 'b');`)
	require.NoError(t, err)
	field := &filter.FieldSchema{
		ExternalName: "tags", DataType: core.FieldTypeString, Includable: true,
		Relation: &core.RelationSchema{JoinTable: "pairs", SourceColumn: "owner", TargetColumn: "target"},
	}
	query, err := filter.BuildIncludeQuery(field, filter.SQLiteDialect{}, []any{"owner"}, filter.WithMaxIncludePerRelation(3))
	require.NoError(t, err)
	rows, err := db.QueryContext(t.Context(), query.SQL, query.Args...)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	got, err := query.Scan(rows)
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"owner": {"B", "Z", "a"}}, got.IDs)
	require.Equal(t, []string{"owner"}, got.Truncated)
}
