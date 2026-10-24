//go:build integration

package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsehttp"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := openDB(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	h, err := newServer(db, slog.New(slog.NewTextHandler(io.Discard, nil)), 5*time.Second)
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path string, body string, out any) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	res, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { assert.NoError(t, res.Body.Close()) }()
	require.NoError(t, json.NewDecoder(res.Body).Decode(out))
	return res.StatusCode
}

type page struct {
	Items    []Product `json:"items"`
	PageInfo struct {
		HasNextPage bool   `json:"has_next_page"`
		EndCursor   string `json:"end_cursor"`
		TotalCount  *int64 `json:"total_count"`
	} `json:"page_info"`
}

// The example walks the same contract the TypeScript SDK sends.
func TestServer_ListWalk(t *testing.T) {
	srv := testServer(t)
	var names []string
	cursor := ""
	for n := 0; ; n++ {
		require.Less(t, n, 10)
		q := url.Values{filter.ParamSort: {"-price"}, filter.ParamPageSize: {"2"}, "category": {"books"}, filter.ParamIncludeTotal: {"true"}}
		if cursor != "" {
			q.Set(filter.ParamCursor, cursor)
		}
		var p page
		require.Equal(t, http.StatusOK, call(t, srv, http.MethodGet, "/api/products?"+q.Encode(), "", &p))
		require.NotNil(t, p.PageInfo.TotalCount)
		assert.Equal(t, int64(3), *p.PageInfo.TotalCount)
		for _, it := range p.Items {
			names = append(names, it.Name)
		}
		if !p.PageInfo.HasNextPage {
			break
		}
		cursor = p.PageInfo.EndCursor
	}
	assert.Equal(t, []string{"Go Programming", "Clean Code", "Design Patterns"}, names)
}

func TestServer_JSONFilterAndFlatParamsCombine(t *testing.T) {
	srv := testServer(t)
	q := url.Values{
		filter.ParamFilter: {`{"labels":{"$contains":{"brand":"volt"}}}`},
		"price_gte":        {"50"},
	}
	var p page
	require.Equal(t, http.StatusOK, call(t, srv, http.MethodGet, "/api/products?"+q.Encode(), "", &p))
	require.Len(t, p.Items, 1)
	assert.Equal(t, "Coffee Maker", p.Items[0].Name)
}

func TestServer_Facets(t *testing.T) {
	srv := testServer(t)
	q := url.Values{filter.ParamFacets: {"category"}, filter.ParamFilter: {`{"price":{"$lte":50}}`}}
	var body struct {
		Facets map[string][]filter.FacetBucket `json:"facets"`
	}
	require.Equal(t, http.StatusOK, call(t, srv, http.MethodOptions, "/api/products?"+q.Encode(), "", &body))
	assert.ElementsMatch(t, []filter.FacetBucket{{Key: "books", Count: 3}, {Key: "electronics", Count: 2}, {Key: "clothing", Count: 1}}, body.Facets["category"])
}

func TestServer_ListCarriesFacetsForTheFilteredPopulation(t *testing.T) {
	srv := testServer(t)
	q := url.Values{
		filter.ParamFacets:       {"category"},
		filter.ParamFilter:       {`{"price":{"$lte":50}}`},
		filter.ParamPageSize:     {"2"},
		filter.ParamIncludeTotal: {"true"},
	}
	var body filter.Page[Product]
	require.Equal(t, http.StatusOK, call(t, srv, http.MethodGet, "/api/products?"+q.Encode(), "", &body))
	require.Len(t, body.Items, 2)
	assert.True(t, body.PageInfo.HasNextPage)
	require.NotNil(t, body.PageInfo.TotalCount)
	assert.Equal(t, int64(6), *body.PageInfo.TotalCount)
	assert.Equal(t, []filter.FacetBucket{{Key: "books", Count: 3}, {Key: "electronics", Count: 2}, {Key: "clothing", Count: 1}}, body.Facets["category"])
}

func TestServer_MergePatchKeepsSiblings(t *testing.T) {
	srv := testServer(t)
	var res struct {
		Updated []Product `json:"updated"`
	}
	require.Equal(t, http.StatusOK, call(t, srv, http.MethodPatch, "/api/products/p01", `{"price":999,"labels":{"refurb":true,"sale":"summer"}}`, &res))
	require.Len(t, res.Updated, 1)
	assert.InDelta(t, 999.0, res.Updated[0].Price, 0)
	assert.Equal(t, map[string]any{"brand": "acme", "refurb": true, "sale": "summer"}, res.Updated[0].Labels)
}

func TestServer_ErrorsCarryECodes(t *testing.T) {
	srv := testServer(t)
	var body parsehttp.Body
	status := call(t, srv, http.MethodGet, "/api/products?"+url.Values{filter.ParamFilter: {`{"nope":1}`}}.Encode(), "", &body)
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, filter.ECodeUnknownField.String(), body.Code)

	status = call(t, srv, http.MethodGet, "/api/products?colour=red", "", &body)
	assert.Equal(t, http.StatusUnprocessableEntity, status, "strict flat parameters reject unknown fields")
	assert.Equal(t, filter.ECodeUnknownField.String(), body.Code)

	status = call(t, srv, http.MethodPatch, "/api/products/p01", `{"category":"toys"}`, &body)
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "E2077", body.Code)

	status = call(t, srv, http.MethodGet, "/api/unknown/schema", "", &body)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, filter.ECodeSchemaNotFound.String(), body.Code)
}

func TestServer_CombinedFiltersShareOneCostBudget(t *testing.T) {
	srv := testServer(t)
	parts := url.Values{
		filter.ParamFilter: {`{"name":{"$contains":"Go"}}`},
		"category":         {"books"},
		"price_gte":        {"10"},
		"price_lte":        {"100"},
	}
	for _, q := range []url.Values{
		{filter.ParamFilter: parts[filter.ParamFilter]},
		{"category": parts["category"], "price_gte": parts["price_gte"], "price_lte": parts["price_lte"]},
	} {
		var p page
		require.Equal(t, http.StatusOK, call(t, srv, http.MethodGet, "/api/products?"+q.Encode(), "", &p))
	}
	var body parsehttp.Body
	status := call(t, srv, http.MethodGet, "/api/products?"+parts.Encode(), "", &body)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, filter.ECodeQueryTooExpensive.String(), body.Code)
}
