//go:build unit || integration

package parse_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// runPathFacets counts every PathFacets facet over PathRecords in one
// facet query.
func runPathFacets(t *testing.T, p pathDB) {
	t.Helper()
	schema := loadPathRecords(t, p)
	var reqs []filter.FacetRequest
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for n := range filtertest.PathFacets {
			if !yield(n) {
				return
			}
		}
	}) {
		reqs = append(reqs, filter.FacetRequest{Field: name})
	}
	q, err := filter.BuildFacetQuery(schema, p.dialect, "1=1", nil, reqs)
	require.NoError(t, err)
	rows, err := p.db.Query(q.SQL, q.Args...)
	require.NoError(t, err, q.SQL)
	results, err := filter.ScanFacetUnion(rows)
	require.NoError(t, err)
	require.NoError(t, rows.Close())
	got := map[string]map[string]int64{}
	for _, r := range results {
		got[r.Field] = map[string]int64{}
		for _, b := range r.Buckets {
			got[r.Field][b.Key] = b.Count
		}
	}
	assert.Equal(t, filtertest.PathFacets, got)
}
