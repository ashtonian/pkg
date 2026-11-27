//go:build integration

package corpus

import (
	"database/sql"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Array members and columns on each backend: SQLite holds the document and
// both arrays as JSON text, PostgreSQL a jsonb document, a text[] and a
// bigint[] column.
func arrayMatrix(t *testing.T, engine Engine) {
	runOn(t, engine, runArrayMatrix)
}

// runArrayMatrix runs the array cases and facets on b.
func runArrayMatrix(t *testing.T, b sqlBackend) {
	_, db := b.open(t)
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

// assertFacetOrders requests every order's facet with its limit in one
// facet query and checks the keys each returns, in order. FacetBuckets must
// order the fixture's counts the same way.
func assertFacetOrders(t *testing.T, db *sql.DB, dialect filter.Dialect, schema *filter.ResourceSchema,
	orders map[string]filtertest.FacetOrder, counts map[string]map[string]int64) {
	t.Helper()
	var reqs []filter.FacetRequest
	for _, name := range slices.Sorted(maps.Keys(orders)) {
		reqs = append(reqs, filter.FacetRequest{Field: name, Limit: orders[name].Limit})
	}
	got := map[string][]string{}
	for _, r := range facetQuery(t, db, dialect, schema, reqs) {
		for _, b := range r.Buckets {
			got[r.Field] = append(got[r.Field], b.Key)
		}
	}
	want := map[string][]string{}
	for name, o := range orders {
		want[name] = o.Keys
		var inGo []string
		for _, b := range filter.FacetBuckets(schema.Fields[name], counts[name], o.Limit) {
			inGo = append(inGo, b.Key)
		}
		assert.Equal(t, o.Keys, inGo, "FacetBuckets(%s)", name)
	}
	assert.Equal(t, want, got)
}

// facetCounts counts fields' facets over schema's whole table on db in one
// facet query: each field's bucket keys and their counts.
func facetCounts(t *testing.T, db *sql.DB, dialect filter.Dialect, schema *filter.ResourceSchema, fields ...string) map[string]map[string]int64 {
	t.Helper()
	reqs := make([]filter.FacetRequest, len(fields))
	for i, f := range fields {
		reqs[i] = filter.FacetRequest{Field: f}
	}
	return bucketCounts(facetQuery(t, db, dialect, schema, reqs))
}

// facetQuery runs reqs over schema's whole table on db in one facet query.
func facetQuery(t *testing.T, db *sql.DB, dialect filter.Dialect, schema *filter.ResourceSchema, reqs []filter.FacetRequest) []filter.FacetResult {
	t.Helper()
	q, err := filter.BuildFacetQuery(schema, dialect, "1=1", nil, reqs)
	require.NoError(t, err)
	return queryFacets(t, db, q.SQL, q.Args...)
}

// queryFacets runs a facet query on db and reads its facets.
func queryFacets(t *testing.T, db dbtest.Queryer, query string, args ...any) []filter.FacetResult {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), query, args...)
	require.NoError(t, err, query)
	defer func() { require.NoError(t, rows.Close()) }()
	results, err := filter.ScanFacetUnion(rows)
	require.NoError(t, err, query)
	require.NoError(t, rows.Err(), query)
	return results
}

// bucketCounts is each facet's bucket keys and their counts.
func bucketCounts(results []filter.FacetResult) map[string]map[string]int64 {
	got := map[string]map[string]int64{}
	for _, r := range results {
		got[r.Field] = map[string]int64{}
		for _, b := range r.Buckets {
			got[r.Field][b.Key] = b.Count
		}
	}
	return got
}

// Numeric facet keys on each backend: REAL columns, a JSON text document and
// a JSON text list on SQLite; a jsonb document, double precision, real,
// bigint and real[] columns on PostgreSQL. Every facet counts in one facet
// query, keyed as BuildFacetQuery and FacetKeys give them.
func numberKeys(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		_, db := b.open(t)
		schema, err := filtertest.NumberKeySchema()
		require.NoError(t, err)
		table := schemaTable(schema)
		b.load(t, db, table, recordRows(t, table, filtertest.NumberKeyRecords()))
		got := facetCounts(t, db, b.dialect, schema, slices.Sorted(maps.Keys(filtertest.NumberKeyFacets))...)
		assert.Equal(t, filtertest.NumberKeyFacets, got)
	})
}

// Facets over paths on each backend: every PathFacets facet over
// PathRecords in one facet query, and each order's keys.
func pathFacets(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		schema := pathSchema(t, "")
		_, db := loadPathRecords(t, b, schema)
		got := facetCounts(t, db, b.dialect, schema, slices.Sorted(maps.Keys(filtertest.PathFacets))...)
		assert.Equal(t, filtertest.PathFacets, got)
		assertFacetOrders(t, db, b.dialect, schema, filtertest.PathFacetOrders, filtertest.PathFacets)
	})
}
