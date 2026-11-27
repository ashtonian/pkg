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

func sqliteScores(t *testing.T, column string, field core.FieldType) scoreDB {
	scores := scoreDB{db: dbtest.SQLite(t), dialect: filter.SQLiteDialect{}, column: column, field: field}
	return scores
}

// SQLite orders a REAL column -Infinity, the finite values, +Infinity,
// and stores NaN as NULL: pages end on the infinities and walk on, and
// the NaN rows page as the NULL ones they are.
func TestNonFiniteFloatWalk_SQLite(t *testing.T) {
	t.Run("nullable", func(t *testing.T) {
		runNonFiniteWalks(t, sqliteScores(t, "REAL", core.FieldTypeFloat), true, []int64{3, 7, 9, 4, 2, 8, 1, 5, 6, 10})
	})
	t.Run("not null", func(t *testing.T) {
		runNonFiniteWalks(t, sqliteScores(t, "REAL", core.FieldTypeFloat), false, []int64{3, 7, 9, 4, 2, 8})
	})
}

// Evaluate reads a REAL column's infinities as SQLite compares them, and
// its NaN, which SQLite stores as NULL, as the NULL it reads back.
func TestNonFiniteFloatFilter_SQLite(t *testing.T) {
	runNonFiniteFilters(t, sqliteScores(t, "REAL", core.FieldTypeFloat))
}

// No NaN or infinity is a facet key, in the statement or in Go.
func TestNonFiniteFloatFacet_SQLite(t *testing.T) {
	runNonFiniteFacets(t, sqliteScores(t, "REAL", core.FieldTypeFloat), []filter.FacetBucket{{Key: "0", Count: 1}, {Key: "1.5", Count: 1}})
}

// SQLite has no decimal NaN or infinity: a decimal column holds a number
// literal, and parse_decimal reads PostgreSQL's NaN and infinity texts,
// which only a table written outside parse holds, as NULL. A page ending
// on one is a NULL boundary there (the cursor carries the text), so the
// rows page as the NULL ones the statement orders them as, and no facet
// keys them.
func TestNonFiniteDecimal_SQLiteReadsThemAsNull(t *testing.T) {
	t.Run("walk", func(t *testing.T) {
		runNonFiniteWalks(t, sqliteScores(t, "TEXT", core.FieldTypeDecimal), true, []int64{9, 4, 1, 2, 3, 5, 6, 7, 8, 10})
	})
	t.Run("facet", func(t *testing.T) {
		runNonFiniteFacets(t, sqliteScores(t, "TEXT", core.FieldTypeDecimal), []filter.FacetBucket{{Key: "0", Count: 1}, {Key: "1.5", Count: 1}})
	})
}

// A NaN boundary, which a database that stores NaN mints, is a NULL one
// where SQLite stores NaN as NULL: the seek resumes after the NULL rows'
// boundary, never at a NULL bound in its place.
func TestNonFiniteFloatSeek_SQLiteNaNIsNull(t *testing.T) {
	db := dbtest.SQLite(t)
	_, err := db.ExecContext(t.Context(), "CREATE TABLE scores (id INTEGER PRIMARY KEY, score REAL)")
	require.NoError(t, err)
	for _, seed := range floatSeeds() {
		_, err = db.ExecContext(t.Context(), "INSERT INTO scores (id, score) VALUES (?, ?)", seed.id, seed.score)
		require.NoError(t, err)
	}
	key := filter.CursorKey{Name: "score", Table: "scores", Column: "score", DataType: core.FieldTypeFloat, NumberKind: reflect.Float64, Nullable: true, Direction: filter.SortAsc}
	id := filter.CursorKey{Name: "id", Table: "scores", Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64, Direction: filter.SortAsc}
	keys := []filter.CursorKey{key, id}
	for _, tc := range []struct {
		name    string
		reverse bool
		want    []int64
	}{
		{"forward", false, []int64{5, 6, 10}},
		{"reverse", true, []int64{8, 2, 4, 9, 7, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			defer func() { require.NoError(t, rows.Close()) }()
			var got []int64
			for rows.Next() {
				var n int64
				require.NoError(t, rows.Scan(&n))
				got = append(got, n)
			}
			require.NoError(t, rows.Err())
			assert.Equal(t, tc.want, got)
		})
	}
}
