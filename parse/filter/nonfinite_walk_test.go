//go:build unit

package filter_test

import (
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// SQLite orders a REAL column -Infinity, the finite values, +Infinity,
// and stores NaN as NULL: pages end on the infinities and walk on, and
// the NaN rows page as the NULL ones they are.
func TestNonFiniteFloatWalk_SQLite(t *testing.T) {
	t.Run("nullable", func(t *testing.T) {
		runNonFiniteWalks(t, scoreDB{db: dbtest.SQLite(t), dialect: filter.SQLiteDialect{}, column: "REAL"}, true,
			[]int64{3, 7, 9, 4, 2, 8, 1, 5, 6, 10})
	})
	t.Run("not null", func(t *testing.T) {
		runNonFiniteWalks(t, scoreDB{db: dbtest.SQLite(t), dialect: filter.SQLiteDialect{}, column: "REAL"}, false,
			[]int64{3, 7, 9, 4, 2, 8})
	})
}

// Evaluate reads a REAL column's infinities as SQLite compares them, and
// its NaN, which SQLite stores as NULL, as the NULL it reads back.
func TestNonFiniteFloatFilter_SQLite(t *testing.T) {
	runNonFiniteFilters(t, scoreDB{db: dbtest.SQLite(t), dialect: filter.SQLiteDialect{}, column: "REAL"})
}

// A NaN boundary, which a database that stores NaN mints, is a NULL one
// where SQLite stores NaN as NULL: the seek resumes after the NULL rows'
// boundary, never at a NULL bound in its place.
func TestNonFiniteFloatSeek_SQLiteNaNIsNull(t *testing.T) {
	db := dbtest.SQLite(t)
	_, err := db.ExecContext(t.Context(), "CREATE TABLE scores (id INTEGER PRIMARY KEY, score REAL)")
	require.NoError(t, err)
	for _, seed := range scoreSeeds {
		_, err = db.ExecContext(t.Context(), "INSERT INTO scores (id, score) VALUES (?, ?)", seed.ID, seed.Score)
		require.NoError(t, err)
	}
	key := filter.CursorKey{Name: "score", Table: "scores", Column: "score", DataType: core.FieldTypeFloat, NumberKind: reflect.Float64, Nullable: true, Direction: filter.SortAsc}
	id := filter.CursorKey{Name: "id", Table: "scores", Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64, Direction: filter.SortAsc}
	keys := []filter.CursorKey{key, id}
	for _, tc := range []struct {
		reverse bool
		want    []int64
	}{
		{false, []int64{5, 6, 10}},
		{true, []int64{8, 2, 4, 9, 7, 3}},
	} {
		token := &filter.PageToken{Reverse: tc.reverse, Fields: []filter.CursorField{
			{Name: "score", Value: math.NaN(), Direction: filter.SortAsc},
			{Name: "id", Value: int64(1), Direction: filter.SortAsc},
		}}
		var args []any
		where, err := filter.SeekPredicate(filter.SQLiteDialect{}, keys, token, func(v any) string {
			args = append(args, v)
			return filter.SQLiteDialect{}.Placeholder(len(args))
		})
		require.NoError(t, err)
		rows, err := db.QueryContext(t.Context(), "SELECT id FROM scores WHERE "+where+" "+filter.OrderByKeys(filter.SQLiteDialect{}, keys, tc.reverse), args...)
		require.NoError(t, err)
		var got []int64
		for rows.Next() {
			var n int64
			require.NoError(t, rows.Scan(&n))
			got = append(got, n)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		assert.Equal(t, tc.want, got, "reverse %v", tc.reverse)
	}
}
