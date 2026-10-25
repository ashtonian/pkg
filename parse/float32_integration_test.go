//go:build integration

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/patch"
)

func TestFloat32FilterAndPatchSQLiteParity(t *testing.T) {
	type row struct {
		ID    int64   `json:"id" db:"id" parse:"filter,sort"`
		Ratio float32 `json:"ratio" db:"ratio" parse:"filter,patch"`
	}
	p, err := parse.NewResource[row]("ratios")
	require.NoError(t, err)
	db := dbtest.SQLite(t)
	_, err = db.ExecContext(t.Context(), "CREATE TABLE ratios (id INTEGER PRIMARY KEY, ratio REAL)")
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), "INSERT INTO ratios VALUES (1, ?)", float64(float32(0.1)))
	require.NoError(t, err)

	// A request literal must select a value stored from the declared Go type.
	parsed, err := p.ParseFilter(`{"ratio":0.1}`)
	require.NoError(t, err)
	where, err := filter.NewSQLBuilder(p.FilterSchema(), filter.SQLiteDialect{}).Build(parsed.AST)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM ratios WHERE "+where.Where, where.Args...).Scan(&count))
	require.Equal(t, 1, count)

	// SQL writes and in-memory application must keep the same precision.
	change, err := p.ParseMergePatch([]byte(`{"ratio":0.2}`))
	require.NoError(t, err)
	got, err := p.ApplyValidatedPatch(change, row{ID: 1})
	require.NoError(t, err)
	update, err := patch.BuildPatchSQL(change, filter.SQLiteDialect{})
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), "UPDATE ratios SET "+update.SetClause+" WHERE id = 1", update.Args...)
	require.NoError(t, err)
	var stored float64
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT ratio FROM ratios WHERE id = 1").Scan(&stored))
	require.Equal(t, float64(got.Ratio), stored)
}
