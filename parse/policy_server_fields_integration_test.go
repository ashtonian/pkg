//go:build integration

package parse_test

import (
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
	p, db := b.open(t)
	execStatements(t, db, serverFieldFixture...)
	docs := serverFieldDocuments(t, p)
	req, err := docs.ParseList(url.Values{filter.ParamIncludeTotal: {"true"}, filter.ParamFacets: {"title"}})
	require.NoError(t, err)
	page := listPage(t, docs, req, ownerEUPolicy())

	assert.Equal(t, []int{1, 4}, itemIDs(page.Items, func(d serverFieldDocument) int { return d.ID }))
	require.NotNil(t, page.PageInfo.TotalCount)
	assert.Equal(t, int64(2), *page.PageInfo.TotalCount)
	counts := map[string]int64{}
	for _, bucket := range page.Facets["title"] {
		counts[bucket.Key] = bucket.Count
	}
	assert.Equal(t, map[string]int64{"alpha": 1, "gamma": 1}, counts, "the facet counts the policy's population")
}
