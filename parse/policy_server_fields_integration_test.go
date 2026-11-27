//go:build integration

package parse_test

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
)

// serverFieldFixture holds the rows serverFieldRows holds for the memory
// backend.
var serverFieldFixture = []string{
	`CREATE TABLE documents (id INTEGER PRIMARY KEY, owner_id INTEGER NOT NULL, region_code TEXT NOT NULL, title TEXT NOT NULL, folder_id INTEGER NOT NULL)`,
	`INSERT INTO documents VALUES (1, 7, 'eu', 'alpha', 1), (2, 7, 'us', 'beta', 1), (3, 8, 'eu', 'alpha', 2), (4, 7, 'eu', 'gamma', 2)`,
}

func TestPolicyServerFields(t *testing.T) {
	runSQLBackends(t, runPolicyServerFields)
}

func runPolicyServerFields(t *testing.T, b sqlBackend) {
	db := b.open(t)
	d := b.dialect
	execStatements(t, db, serverFieldFixture...)
	docs := serverFieldDocuments(t)
	req, err := docs.ParseList(url.Values{filter.ParamIncludeTotal: {"true"}, filter.ParamFacets: {"title"}})
	require.NoError(t, err)
	plan, err := docs.BuildList(d, req, ownerEUPolicy())
	require.NoError(t, err)

	var ids []string
	for _, row := range queryRows[[]any](t, db, plan.Page.SQL, plan.Page.Args...) {
		ids = append(ids, fmt.Sprint(row[0]))
	}
	assert.Equal(t, []string{"1", "4"}, ids)
	assert.Equal(t, "2", fmt.Sprint(queryRows[[]any](t, db, plan.Count.SQL, plan.Count.Args...)[0][0]))

	rows, err := db.Query(plan.Facets.SQL, plan.Facets.Args...)
	require.NoError(t, err)
	facets, err := filter.ScanFacetUnion(rows)
	require.NoError(t, err)
	counts := map[string]int64{}
	for _, f := range facets {
		for _, b := range f.Buckets {
			counts[fmt.Sprint(b.Key)] = b.Count
		}
	}
	assert.Equal(t, map[string]int64{"alpha": 1, "gamma": 1}, counts, "the facet counts the policy's population")
}
