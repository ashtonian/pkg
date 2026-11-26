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

// Numeric facet keys on each backend: REAL columns, a JSON text document and
// a JSON text list on SQLite; a jsonb document, double precision, real,
// bigint and real[] columns on PostgreSQL. Every facet counts in one facet
// query, keyed as BuildFacetQuery and FacetKeys give them.
func TestNumberKeys(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		db := b.open(t)
		schema, err := filtertest.NumberKeySchema()
		require.NoError(t, err)
		table := schemaTable(schema)
		b.load(t, db, table, recordRows(t, table, filtertest.NumberKeyRecords()))
		got := facetCounts(t, db, b.dialect, schema, slices.Sorted(maps.Keys(filtertest.NumberKeyFacets))...)
		assert.Equal(t, filtertest.NumberKeyFacets, got)
	})
}
