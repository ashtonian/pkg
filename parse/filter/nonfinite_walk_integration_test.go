//go:build integration

package filter_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func postgresScores(t *testing.T, column string, field core.FieldType) scoreDB {
	scores := scoreDB{db: dbtest.Postgres(t), dialect: filter.PostgresDialect{}, column: column, field: field}
	return scores
}

// PostgreSQL orders float8 and numeric -Infinity, the finite values,
// +Infinity, NaN, and holds NaN equal to NaN: pages end on each and walk
// on, a nullable column by the expanded seek, a NOT NULL one by the row
// comparison.
func TestNonFiniteWalk_Postgres(t *testing.T) {
	for _, c := range []struct {
		column string
		field  core.FieldType
	}{
		{"double precision", core.FieldTypeFloat},
		{"numeric", core.FieldTypeDecimal},
	} {
		t.Run(c.column+" nullable", func(t *testing.T) {
			runNonFiniteWalks(t, postgresScores(t, c.column, c.field), true, []int64{3, 7, 9, 4, 2, 8, 1, 6, 5, 10})
		})
		t.Run(c.column+" not null", func(t *testing.T) {
			runNonFiniteWalks(t, postgresScores(t, c.column, c.field), false, []int64{3, 7, 9, 4, 2, 8, 1, 6})
		})
	}
}

// Evaluate reads a float8 or numeric column's NaN and infinities as
// PostgreSQL compares them: NaN after +Infinity, so greater than every
// number; and no facet keys one, in the statement or in Go.
func TestNonFiniteFilterAndFacet_Postgres(t *testing.T) {
	for _, c := range []struct {
		column string
		field  core.FieldType
	}{
		{"double precision", core.FieldTypeFloat},
		{"numeric", core.FieldTypeDecimal},
	} {
		t.Run(c.column, func(t *testing.T) {
			runNonFiniteFilters(t, postgresScores(t, c.column, c.field))
		})
		t.Run(c.column+" facet", func(t *testing.T) {
			runNonFiniteFacets(t, postgresScores(t, c.column, c.field), []filter.FacetBucket{{Key: "0", Count: 1}, {Key: "1.5", Count: 1}})
		})
	}
}
