//go:build integration

package parse_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// Exact decimals on each backend: SQLite holds amounts as decimal text and
// prices in JSON text, compared through parsesqlite's functions;
// PostgreSQL numeric amounts and jsonb prices.
func TestDecimalMatrix(t *testing.T) {
	runSQLBackends(t, runDecimalMatrix)
}

// runDecimalMatrix writes DecimalRecords, each amount as its decimal text
// with its scale, runs the decimal cases, sorts, keyset walks and facets on
// b, and checks that each row's price reads in SQL as filter.PathValue reads
// it.
func runDecimalMatrix(t *testing.T, b sqlBackend) {
	db := b.open(t)
	schema, err := filtertest.DecimalSchema()
	require.NoError(t, err)
	table := schemaTable(schema)
	b.load(t, db, table, recordRows(t, table, filtertest.DecimalRecords()))
	t.Run("cases", func(t *testing.T) {
		filtertest.RunDecimals(t, schema, b.filterIDs(db, schema))
	})
	t.Run("walks", func(t *testing.T) {
		r, err := parse.NewResource[filtertest.DecimalRecord](schema.Name, parse.PageSize(walkPage, 10))
		require.NoError(t, err)
		walkOrders(t, walker[filtertest.DecimalRecord]{db: db, dialect: b.dialect, resource: r,
			id: func(r filtertest.DecimalRecord) string { return r.ID }}, filtertest.DecimalOrders)
	})
	t.Run("facets", func(t *testing.T) {
		got := facetCounts(t, db, b.dialect, schema, slices.Sorted(maps.Keys(filtertest.DecimalFacets))...)
		assert.Equal(t, filtertest.DecimalFacets, got)
	})
	t.Run("facet orders", func(t *testing.T) {
		assertFacetOrders(t, db, b.dialect, schema, filtertest.DecimalFacetOrders, filtertest.DecimalFacets)
	})
	t.Run("values match PathValue", func(t *testing.T) {
		f := schema.Fields["doc.price"]
		for _, row := range queryRows[[]any](t, db, "SELECT id, doc, "+filter.FieldExpr(b.dialect, f)+" FROM "+schema.Name+" ORDER BY id") {
			id, doc, got := row[0], row[1], row[2]
			want, present, err := filter.PathValue(textOf(doc), f)
			require.NoError(t, err, id)
			if !present {
				want = nil
			}
			assert.True(t, sameDecimal(got, want), "row %v: SQL %v (%T), PathValue %v", id, got, got, want)
		}
	})
}
