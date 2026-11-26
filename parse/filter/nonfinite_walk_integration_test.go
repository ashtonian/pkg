//go:build integration

package filter_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// PostgreSQL orders float8 -Infinity, the finite values, +Infinity, NaN,
// and holds NaN equal to NaN: pages end on each and walk on, a nullable
// column by the expanded seek, a NOT NULL one by the row comparison.
func TestNonFiniteFloatWalk_Postgres(t *testing.T) {
	t.Run("nullable", func(t *testing.T) {
		runNonFiniteWalks(t, scoreDB{db: dbtest.Postgres(t), dialect: filter.PostgresDialect{}, column: "double precision"}, true,
			[]int64{3, 7, 9, 4, 2, 8, 1, 6, 5, 10})
	})
	t.Run("not null", func(t *testing.T) {
		runNonFiniteWalks(t, scoreDB{db: dbtest.Postgres(t), dialect: filter.PostgresDialect{}, column: "double precision"}, false,
			[]int64{3, 7, 9, 4, 2, 8, 1, 6})
	})
}

// Evaluate reads a float8 column's NaN and infinities as PostgreSQL
// compares them: NaN after +Infinity, so greater than every number.
func TestNonFiniteFloatFilter_Postgres(t *testing.T) {
	runNonFiniteFilters(t, scoreDB{db: dbtest.Postgres(t), dialect: filter.PostgresDialect{}, column: "double precision"})
}
