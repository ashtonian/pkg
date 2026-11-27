//go:build integration

package parse_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// Facets over paths on each backend: every PathFacets facet over
// PathRecords in one facet query, and each order's keys.
func TestPathFacets(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		schema := pathSchema(t, "")
		_, db := loadPathRecords(t, b, schema)
		got := facetCounts(t, db, b.dialect, schema, slices.Sorted(maps.Keys(filtertest.PathFacets))...)
		assert.Equal(t, filtertest.PathFacets, got)
		assertFacetOrders(t, db, b.dialect, schema, filtertest.PathFacetOrders, filtertest.PathFacets)
	})
}
