//go:build integration

package parse_test

import (
	"database/sql"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

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
func queryFacets(t *testing.T, db sqlQueryer, query string, args ...any) []filter.FacetResult {
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
