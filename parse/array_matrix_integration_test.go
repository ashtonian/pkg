//go:build integration

package parse_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// Array members and columns on each backend: SQLite holds the document and
// both arrays as JSON text, PostgreSQL a jsonb document, a text[] and a
// bigint[] column.
func TestArrayMatrix(t *testing.T) {
	runSQLBackends(t, runArrayMatrix)
}

// runArrayMatrix runs the array cases and facets on b.
func runArrayMatrix(t *testing.T, b sqlBackend) {
	db := b.open(t)
	schema, err := filtertest.ArraySchema()
	require.NoError(t, err)
	table := schemaTable(schema)
	b.load(t, db, table, recordRows(t, table, filtertest.ArrayRecords()))
	t.Run("cases", func(t *testing.T) {
		filtertest.RunArrays(t, schema, b.filterIDs(db, schema))
	})
	t.Run("facets", func(t *testing.T) {
		got := facetCounts(t, db, b.dialect, schema, slices.Sorted(maps.Keys(filtertest.ArrayFacets))...)
		assert.Equal(t, filtertest.ArrayFacets, got)
	})
	t.Run("facet orders", func(t *testing.T) {
		assertFacetOrders(t, db, b.dialect, schema, filtertest.ArrayFacetOrders, filtertest.ArrayFacets)
	})
}
