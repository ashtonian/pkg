//go:build unit || integration

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
	q, err := filter.BuildFacetQuery(schema, dialect, "1=1", nil, reqs)
	require.NoError(t, err)
	rows, err := db.Query(q.SQL, q.Args...)
	require.NoError(t, err, q.SQL)
	results, err := filter.ScanFacetUnion(rows)
	require.NoError(t, err)
	require.NoError(t, rows.Close())
	got := map[string][]string{}
	for _, r := range results {
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
