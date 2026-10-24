//go:build unit

package parse_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

type customItem struct {
	ID     string          `json:"id"     db:"id"     parse:"filter,sort"`
	Custom json.RawMessage `json:"custom" db:"custom" parse:"filter"`
}

// A request declares its tenant's custom fields as paths on a view of the
// resource; the shared resource never learns them.
func TestResource_WithPathsIsARequestView(t *testing.T) {
	r, err := parse.NewResource[customItem]("custom_items", parse.PageSize(10, 10), parse.AcceptParams("facets"))
	require.NoError(t, err)
	view, err := r.WithPaths(filter.PathDecl{Path: "custom.tier", Type: core.FieldTypeString, Sort: true, Facet: &filter.FacetSchema{Type: filter.FacetTerms}})
	require.NoError(t, err)

	q := url.Values{"filter": {`{"custom.tier":{"$ne":"bronze"}}`}, "sort": {"-custom.tier"}, "facets": {"custom.tier"}}
	_, err = r.ParseList(q)
	var pe *core.ParseError
	require.True(t, errors.As(err, &pe), "the shared resource does not know the path: %v", err)
	assert.Equal(t, "E2071", pe.Code)

	req, err := view.ParseList(q)
	require.NoError(t, err)
	plan, err := view.BuildList(filter.SQLiteDialect{}, req, parse.Scope{})
	require.NoError(t, err)

	db := dbtest.SQLite(t)
	_, err = db.Exec(`CREATE TABLE custom_items (id TEXT PRIMARY KEY, custom TEXT)`)
	require.NoError(t, err)
	for id, doc := range map[string]any{"a": `{"tier":"gold"}`, "b": `{"tier":"silver"}`, "c": `{"tier":"bronze"}`, "d": `{}`, "e": nil} {
		_, insertErr := db.Exec(`INSERT INTO custom_items VALUES (?1, ?2)`, id, doc)
		require.NoError(t, insertErr)
	}
	rows, err := db.Query(plan.Page.SQL, plan.Page.Args...)
	require.NoError(t, err, plan.Page.SQL)
	var items []customItem
	for rows.Next() {
		var it customItem
		var doc *string
		require.NoError(t, rows.Scan(&it.ID, &doc))
		if doc != nil {
			it.Custom = json.RawMessage(*doc)
		}
		items = append(items, it)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	page, err := view.Page(items, plan)
	require.NoError(t, err)
	var ids []string
	for _, it := range page.Items {
		ids = append(ids, it.ID)
	}
	assert.Equal(t, []string{"b", "a"}, ids, "silver then gold; bronze filtered out, absent tiers never match $ne")

	require.NotNil(t, plan.Facets)
	frows, err := db.Query(plan.Facets.SQL, plan.Facets.Args...)
	require.NoError(t, err, plan.Facets.SQL)
	facets, err := filter.ScanFacetUnion(frows)
	require.NoError(t, err)
	require.NoError(t, frows.Close())
	require.Len(t, facets, 1)
	assert.ElementsMatch(t, []filter.FacetBucket{{Key: "gold", Count: 1}, {Key: "silver", Count: 1}}, facets[0].Buckets)

	_, err = r.WithPaths(filter.PathDecl{Path: "id.x", Type: core.FieldTypeString})
	assert.Error(t, err, "a path needs a document field")
}
