//go:build unit || integration

package parse_test

import (
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// numberKeyDB is a database and the column types the number key fixture's
// table takes on it.
type numberKeyDB struct {
	db                                           *sql.DB
	dialect                                      filter.Dialect
	docType, f64Type, f32Type, intType, listType string
	docCast                                      string // wraps the document placeholder, e.g. "%s::jsonb"
	// list is a float32 list's insert argument.
	list func([]float32) any
}

// runNumberKeys counts every facet of NumberKeyRecords on d in one facet
// query, and checks the keys BuildFacetQuery and FacetKeys give.
func runNumberKeys(t *testing.T, d numberKeyDB) {
	t.Helper()
	schema, err := filtertest.NumberKeySchema()
	require.NoError(t, err)
	_, err = d.db.Exec("CREATE TABLE " + schema.Name + " (id TEXT PRIMARY KEY, attrs " + d.docType + ", f64 " + d.f64Type +
		", f32 " + d.f32Type + ", i " + d.intType + ", ratios " + d.listType + ")")
	require.NoError(t, err)
	doc := d.dialect.Placeholder(2)
	if d.docCast != "" {
		doc = fmt.Sprintf(d.docCast, doc)
	}
	insert := "INSERT INTO " + schema.Name + " (id, attrs, f64, f32, i, ratios) VALUES (" + d.dialect.Placeholder(1) + ", " + doc + ", " +
		d.dialect.Placeholder(3) + ", " + d.dialect.Placeholder(4) + ", " + d.dialect.Placeholder(5) + ", " + d.dialect.Placeholder(6) + ")"
	for _, r := range filtertest.NumberKeyRecords() {
		_, err := d.db.Exec(insert, r.ID, jsonText(r.Attrs), r.F64, r.F32, r.I, d.list(r.Ratios))
		require.NoError(t, err, r.ID)
	}

	var reqs []filter.FacetRequest
	for _, name := range slices.Sorted(maps.Keys(filtertest.NumberKeyFacets)) {
		reqs = append(reqs, filter.FacetRequest{Field: name})
	}
	q, err := filter.BuildFacetQuery(schema, d.dialect, "1=1", nil, reqs)
	require.NoError(t, err)
	rows, err := d.db.Query(q.SQL, q.Args...)
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
	assert.Equal(t, filtertest.NumberKeyFacets, got)
}
