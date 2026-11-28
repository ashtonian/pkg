//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsehttp"
)

// call sends body as application/json, as the SDK does: Content-Type goes
// only with a body.
func call(t *testing.T, srv *httptest.Server, method, path string, body string, out any) int {
	t.Helper()
	contentType := ""
	if body != "" {
		contentType = "application/json"
	}
	return send(t, srv, method, path, contentType, body, out)
}

// send is call with a Content-Type; empty sends none.
func send(t *testing.T, srv *httptest.Server, method, path, contentType, body string, out any) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
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
	eachBackend(t, func(t *testing.T, b backend) {
		srv := b.server(t)
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
	})
}

// A list sent as a POST body to the list's path reads the pages a GET
// reads, and each transport's cursors continue a walk on the other.
func TestServer_ListThroughAPOSTBody(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		srv := b.server(t)
		const doc = `{"price":{"$lte":50}}`
		get := func(cursor string) page {
			t.Helper()
			q := url.Values{filter.ParamFilter: {doc}, filter.ParamSort: {"-price"}, filter.ParamPageSize: {"2"}, filter.ParamIncludeTotal: {"true"}}
			if cursor != "" {
				q.Set(filter.ParamCursor, cursor)
			}
			var p page
			require.Equal(t, http.StatusOK, call(t, srv, http.MethodGet, "/api/products?"+q.Encode(), "", &p))
			return p
		}
		post := func(cursor string) page {
			t.Helper()
			members := map[string]any{filter.ParamFilter: json.RawMessage(doc), filter.ParamSort: "-price", filter.ParamPageSize: 2, filter.ParamIncludeTotal: true}
			if cursor != "" {
				members[filter.ParamCursor] = cursor
			}
			body, err := json.Marshal(members)
			require.NoError(t, err)
			var p page
			require.Equal(t, http.StatusOK, send(t, srv, http.MethodPost, "/api/products", "application/json", string(body), &p))
			return p
		}
		walk := func(transports ...func(cursor string) page) []page {
			t.Helper()
			var pages []page
			cursor := ""
			for n := 0; ; n++ {
				require.Less(t, n, 10)
				p := transports[n%len(transports)](cursor)
				pages = append(pages, p)
				if !p.PageInfo.HasNextPage {
					return pages
				}
				cursor = p.PageInfo.EndCursor
			}
		}

		want := walk(get)
		require.Len(t, want, 3)
		require.NotNil(t, want[0].PageInfo.TotalCount)
		assert.Equal(t, int64(6), *want[0].PageInfo.TotalCount)
		assert.Equal(t, want, walk(post), "a POST reads the pages a GET reads")
		assert.Equal(t, want, walk(post, get), "each transport continues the other's cursors")
	})
}

func TestServer_POSTListRefusals(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		srv := b.server(t)
		tests := []struct {
			name, path, contentType, body string
			code                          string
			param                         string
		}{
			{"a list parameter in the query", "/api/products?sort=-price", "application/json", `{}`, filter.ECodeInvalidListParam.String(), "sort"},
			{"a field filter in the query", "/api/products?category=books", "application/json", `{}`, filter.ECodeInvalidListParam.String(), "category"},
			{"a form body", "/api/products", "application/x-www-form-urlencoded", "filter=%7B%7D", filter.ECodeInvalidListParam.String(), "content-type"},
			{"an unknown member", "/api/products", "application/json", `{"filtr":{"category":"books"}}`, filter.ECodeInvalidListParam.String(), "filtr"},
			{"a body over the list body limit", "/api/products", "application/json", `{"cursor":"` + strings.Repeat("c", 80<<10) + `"}`, filter.ECodeInputTooLarge.String(), ""},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var body parsehttp.Body
				status := send(t, srv, http.MethodPost, tt.path, tt.contentType, tt.body, &body)
				assert.Equal(t, http.StatusUnprocessableEntity, status)
				assert.Equal(t, tt.code, body.Code)
				if tt.param != "" {
					assert.Equal(t, tt.param, body.Details["parameter"])
				}
			})
		}
	})
}

// maskedPage is a list answer with each item's members as the server wrote
// them.
type maskedPage struct {
	Items    []map[string]json.RawMessage `json:"items"`
	PageInfo json.RawMessage              `json:"page_info"`
	Facets   json.RawMessage              `json:"facets"`
}

// A list answers the members its field mask names, over GET and as a POST
// body alike. Its cursors, total and facets are the unmasked list's, and
// its cursor continues the walk.
func TestServer_ListAnswersItsFieldMask(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		srv := b.server(t)
		byGET := func(t *testing.T, doc string, fields []string, cursor string, out any) int {
			t.Helper()
			q := url.Values{filter.ParamFilter: {doc}, filter.ParamSort: {"-price"}, filter.ParamPageSize: {"2"}, filter.ParamIncludeTotal: {"true"}, filter.ParamFacets: {"category"}}
			if len(fields) > 0 {
				q.Set(filter.ParamFields, strings.Join(fields, ","))
			}
			if cursor != "" {
				q.Set(filter.ParamCursor, cursor)
			}
			return call(t, srv, http.MethodGet, "/api/products?"+q.Encode(), "", out)
		}
		byPOST := func(t *testing.T, doc string, fields []string, cursor string, out any) int {
			t.Helper()
			members := map[string]any{filter.ParamFilter: json.RawMessage(doc), filter.ParamSort: "-price", filter.ParamPageSize: 2, filter.ParamIncludeTotal: true, filter.ParamFacets: []string{"category"}}
			if len(fields) > 0 {
				members[filter.ParamFields] = fields
			}
			if cursor != "" {
				members[filter.ParamCursor] = cursor
			}
			body, err := json.Marshal(members)
			require.NoError(t, err)
			return call(t, srv, http.MethodPost, "/api/products", string(body), out)
		}
		const cheap = `{"price":{"$lte":50}}`
		mask := []string{"price", "name"}
		for _, tt := range []struct {
			name string
			list func(t *testing.T, doc string, fields []string, cursor string, out any) int
		}{
			{"GET", byGET},
			{"POST", byPOST},
		} {
			t.Run(tt.name, func(t *testing.T) {
				var whole, masked maskedPage
				require.Equal(t, http.StatusOK, tt.list(t, cheap, nil, "", &whole))
				require.Equal(t, http.StatusOK, tt.list(t, cheap, mask, "", &masked))
				require.Len(t, whole.Items, 2)
				require.Len(t, masked.Items, 2)
				for i, item := range masked.Items {
					assert.Equal(t, []string{"category", "created_at", "id", "labels", "name", "price"}, slices.Sorted(maps.Keys(whole.Items[i])), "an unmasked item has every member")
					assert.Equal(t, []string{"name", "price"}, slices.Sorted(maps.Keys(item)))
					assert.Equal(t, whole.Items[i]["name"], item["name"])
					assert.Equal(t, whole.Items[i]["price"], item["price"])
				}
				assert.JSONEq(t, string(whole.PageInfo), string(masked.PageInfo), "cursors and total do not depend on the mask")
				assert.JSONEq(t, string(whole.Facets), string(masked.Facets))

				var cursor struct {
					EndCursor string `json:"end_cursor"`
				}
				require.NoError(t, json.Unmarshal(masked.PageInfo, &cursor))
				var wholeNext, maskedNext maskedPage
				require.Equal(t, http.StatusOK, tt.list(t, cheap, nil, cursor.EndCursor, &wholeNext))
				require.Equal(t, http.StatusOK, tt.list(t, cheap, mask, cursor.EndCursor, &maskedNext))
				require.Len(t, maskedNext.Items, len(wholeNext.Items))
				for i, item := range maskedNext.Items {
					assert.Equal(t, wholeNext.Items[i]["name"], item["name"], "a masked page's cursor continues the walk")
				}

				var none maskedPage
				require.Equal(t, http.StatusOK, tt.list(t, `{"price":{"$lte":0}}`, mask, "", &none))
				assert.NotNil(t, none.Items, "items is [], not null")
				assert.Empty(t, none.Items)

				var refused parsehttp.Body
				assert.Equal(t, http.StatusUnprocessableEntity, tt.list(t, cheap, []string{"name", "nmae"}, "", &refused))
				assert.Equal(t, filter.ECodeUnknownField.String(), refused.Code)
				assert.Equal(t, map[string]any{"field": "nmae"}, refused.Details)
			})
		}
	})
}

func TestServer_JSONFilterAndFlatParamsCombine(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		srv := b.server(t)
		q := url.Values{
			filter.ParamFilter: {`{"labels":{"$contains":{"brand":"volt"}}}`},
			"price_gte":        {"50"},
		}
		var p page
		require.Equal(t, http.StatusOK, call(t, srv, http.MethodGet, "/api/products?"+q.Encode(), "", &p))
		require.Len(t, p.Items, 1)
		assert.Equal(t, "Coffee Maker", p.Items[0].Name)
	})
}

func TestServer_Facets(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		srv := b.server(t)
		q := url.Values{filter.ParamFacets: {"category"}, filter.ParamFilter: {`{"price":{"$lte":50}}`}}
		var body struct {
			Facets map[string][]filter.FacetBucket `json:"facets"`
		}
		require.Equal(t, http.StatusOK, call(t, srv, http.MethodOptions, "/api/products?"+q.Encode(), "", &body))
		assert.ElementsMatch(t, []filter.FacetBucket{{Key: "books", Count: 3}, {Key: "electronics", Count: 2}, {Key: "clothing", Count: 1}}, body.Facets["category"])
	})
}

func TestServer_ListCarriesFacetsForTheFilteredPopulation(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		srv := b.server(t)
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
	})
}

func TestServer_MergePatchKeepsSiblings(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		srv := b.server(t)
		var res struct {
			Updated []Product `json:"updated"`
		}
		require.Equal(t, http.StatusOK, call(t, srv, http.MethodPatch, "/api/products/p01", `{"price":999,"labels":{"refurb":true,"sale":"summer"}}`, &res))
		require.Len(t, res.Updated, 1)
		assert.InDelta(t, 999.0, res.Updated[0].Price, 0)
		assert.Equal(t, map[string]any{"brand": "acme", "refurb": true, "sale": "summer"}, res.Updated[0].Labels)
	})
}

// patchMany sends PATCH /{resource}?id[]=…: one merge patch for every key.
func TestServer_BatchMergePatch(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		srv := b.server(t)
		var res struct {
			Updated []Product `json:"updated"`
			Failed  []struct {
				ID, Code, Error string
			} `json:"failed"`
		}
		// "\x00" and "\xff" are texts no key column holds: ParseKey reads them
		// as naming no product, so each is a failed item, not a failed request.
		q := url.Values{"id[]": {"p02", "p03", "nope", "p02", "\x00", "\xff"}}
		require.Equal(t, http.StatusOK, call(t, srv, http.MethodPatch, "/api/products?"+q.Encode(), `{"price":7}`, &res))
		ids := make([]string, len(res.Updated))
		for i, p := range res.Updated {
			ids[i] = p.ID
			assert.InDelta(t, 7.0, p.Price, 0)
		}
		assert.ElementsMatch(t, []string{"p02", "p03"}, ids)
		failed := make([]string, len(res.Failed))
		for i, f := range res.Failed {
			failed[i] = f.ID
			assert.Equal(t, codeNotFound, f.Code)
		}
		// encoding/json writes invalid UTF-8 as U+FFFD.
		assert.ElementsMatch(t, []string{"nope", "\x00", "\ufffd"}, failed, "a key without a product fails once, however often it is named")

		// A batch of keys no product can have writes nothing and fails each.
		var none struct {
			Updated []Product                   `json:"updated"`
			Failed  []struct{ ID, Code string } `json:"failed"`
		}
		require.Equal(t, http.StatusOK, call(t, srv, http.MethodPatch, "/api/products?id[]=%FF", `{"price":8}`, &none))
		assert.Empty(t, none.Updated)
		require.Len(t, none.Failed, 1)
		assert.Equal(t, codeNotFound, none.Failed[0].Code)

		var single struct {
			Updated []Product `json:"updated"`
		}
		require.Equal(t, http.StatusOK, call(t, srv, http.MethodPatch, "/api/products/p03", `{}`, &single))
		require.Len(t, single.Updated, 1)
		assert.InDelta(t, 7.0, single.Updated[0].Price, 0, "the batch wrote the row")
	})
}

func TestServer_BatchMergePatchRefusals(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		srv := b.server(t)
		var body parsehttp.Body
		assert.Equal(t, http.StatusBadRequest, call(t, srv, http.MethodPatch, "/api/products", `{"price":7}`, &body), "no keys")
		assert.Equal(t, codeBadRequest, body.Code)

		many := url.Values{}
		for i := range maxPatchIDs + 1 {
			many.Add("id[]", fmt.Sprintf("p%03d", i))
		}
		assert.Equal(t, http.StatusBadRequest, call(t, srv, http.MethodPatch, "/api/products?"+many.Encode(), `{"price":7}`, &body), "too many keys")
		assert.Equal(t, codeBadRequest, body.Code)

		for _, path := range []string{
			"/api/products?id[]=p01&filter=" + url.QueryEscape(`{"category":"books"}`),
			"/api/products/p01?category=books",
		} {
			assert.Equal(t, http.StatusBadRequest, call(t, srv, http.MethodPatch, path, `{"price":7}`, &body), "a parameter the route does not read could have meant to narrow it: %s", path)
			assert.Equal(t, codeBadRequest, body.Code)
		}

		assert.Equal(t, http.StatusUnprocessableEntity, call(t, srv, http.MethodPatch, "/api/products?id[]=p01", `{"category":"toys"}`, &body))
		assert.Equal(t, "E2077", body.Code, "the patch is validated as a single patch is")

		var res struct {
			Updated []Product `json:"updated"`
		}
		require.Equal(t, http.StatusOK, call(t, srv, http.MethodPatch, "/api/products/p01", `{}`, &res))
		assert.InDelta(t, 1299.99, res.Updated[0].Price, 0, "a refused batch writes nothing")
	})
}

func TestServer_ErrorsCarryECodes(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		srv := b.server(t)
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

		status = call(t, srv, http.MethodPatch, "/api/products/nope", `{"price":1}`, &body)
		assert.Equal(t, http.StatusNotFound, status)
		assert.Equal(t, codeNotFound, body.Code, "a missing row is the application's code, not a patch path error")

		// The key's segment is escaped on its own: an escaped slash is part of
		// the key the item route reads, not another segment.
		status = call(t, srv, http.MethodPatch, "/api/products/p01%2Fx", `{"price":1}`, &body)
		assert.Equal(t, http.StatusNotFound, status)
		assert.Equal(t, codeNotFound, body.Code)
		assert.Equal(t, "product p01%2Fx not found", body.Message)

		status = call(t, srv, http.MethodGet, "/api/unknown/schema", "", &body)
		assert.Equal(t, http.StatusNotFound, status)
		assert.Equal(t, parse.ECodeSchemaNotFound.String(), body.Code)
	})
}

func TestServer_CombinedFiltersShareOneCostBudget(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		srv := b.server(t)
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
	})
}

// A query string that does not decode, or a list parameter given twice, is
// refused as E2067 on every route that reads one: dropping the pair would
// answer a list with every row, or update rows the client did not name.
func TestServer_QueriesThatLoseAParameterAreRefused(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		srv := b.server(t)
		for _, tt := range []struct{ method, path, contentType, body string }{
			{http.MethodGet, "/api/products?category=%zz", "", ""},
			{http.MethodGet, "/api/products?category=books%", "", ""},
			{http.MethodGet, "/api/products?category=books;x=1", "", ""},
			{http.MethodGet, "/api/products?filter=%7B%22category%22%3A%22books%22%7D%", "", ""},
			{http.MethodPost, "/api/products?filter=%zz", "application/json", `{"filter":{"category":"books"}}`},
			{http.MethodOptions, "/api/products?facets=category&category=%zz", "", ""},
			{http.MethodPatch, "/api/products?id[]=p02&id[]=%zz", "", `{"price":7}`},
			{http.MethodPatch, "/api/products/p03?category=%zz", "", `{"price":7}`},
			{http.MethodGet, "/api/products?sort=name&sort=-name", "", ""},
			{http.MethodGet, "/api/products?" + url.Values{filter.ParamFilter: {`{"category":"books"}`, `{"category":"toys"}`}}.Encode(), "", ""},
			{http.MethodGet, "/api/products?page_size=2&page_size=500", "", ""},
		} {
			var body parsehttp.Body
			status := send(t, srv, tt.method, tt.path, tt.contentType, tt.body, &body)
			assert.Equal(t, http.StatusUnprocessableEntity, status, "%s %s", tt.method, tt.path)
			assert.Equal(t, filter.ECodeInvalidListParam.String(), body.Code, "%s %s: %s", tt.method, tt.path, body.Message)
		}

		var res struct {
			Updated []Product `json:"updated"`
		}
		require.Equal(t, http.StatusOK, call(t, srv, http.MethodPatch, "/api/products/p02", `{}`, &res))
		assert.NotEqual(t, 7.0, res.Updated[0].Price, "a refused batch writes nothing")
	})
}

// Patch bodies are read as list bodies are: bounded by the parser's input
// size, in the grammar their media type names, and a body the client did
// not finish sending is a bad request logged at WARN, on both routes.
func TestServer_RequestBodies(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		srv := b.server(t)
		for _, tt := range []struct {
			method, path, contentType, body string
			status                          int
			code                            string
		}{
			{http.MethodPatch, "/api/products/p03", "application/json", `{"name":"` + strings.Repeat("x", 70000) + `"}`, http.StatusUnprocessableEntity, "E2063"},
			{http.MethodPatch, "/api/products/p03", "text/plain", `{"price":7}`, http.StatusUnprocessableEntity, "E2086"},
			{http.MethodPatch, "/api/products/p03", "", `{"price":7}`, http.StatusUnprocessableEntity, "E2086"},
			{http.MethodPatch, "/api/products?id[]=p03", "text/plain", `{"price":7}`, http.StatusUnprocessableEntity, "E2086"},
			{http.MethodPost, "/api/products", "application/json", `{"search":"` + strings.Repeat("x", 90000) + `"}`, http.StatusUnprocessableEntity, "E2063"},
		} {
			var body parsehttp.Body
			assert.Equal(t, tt.status, send(t, srv, tt.method, tt.path, tt.contentType, tt.body, &body), "%s %s %s", tt.method, tt.path, tt.contentType)
			assert.Equal(t, tt.code, body.Code, "%s %s %s: %s", tt.method, tt.path, tt.contentType, body.Message)
		}
		var res struct {
			Updated []Product `json:"updated"`
		}
		require.Equal(t, http.StatusOK, call(t, srv, http.MethodPatch, "/api/products/p03", `{}`, &res))
		assert.NotEqual(t, 7.0, res.Updated[0].Price, "a refused patch writes nothing")
		require.Equal(t, http.StatusOK, send(t, srv, http.MethodPatch, "/api/products/p03", parse.MediaTypeMergePatch, `{"price":7}`, &res))
		assert.InDelta(t, 7.0, res.Updated[0].Price, 0, "the merge patch media type is a merge patch")

		db := b.open(t)
		var logs bytes.Buffer
		h, err := newServer(db, db.products, slog.New(slog.NewJSONHandler(&logs, nil)), 5*time.Second, rand.Text)
		require.NoError(t, err)
		for _, tt := range []struct{ method, path string }{
			{http.MethodPatch, "/api/products/p03"},
			{http.MethodPost, "/api/products"},
		} {
			logs.Reset()
			req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path,
				io.MultiReader(strings.NewReader(`{"price":`), iotest.ErrReader(io.ErrUnexpectedEOF)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code, tt.method)
			var body parsehttp.Body
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, codeBadRequest, body.Code, tt.method)
			assert.Contains(t, logs.String(), `"level":"WARN"`, tt.method)
			assert.NotContains(t, logs.String(), `"level":"ERROR"`, tt.method)
		}
	})
}

// A facets request refuses the list parameters that shape a page instead of
// validating and ignoring them.
func TestServer_FacetsRefusePageParameters(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		srv := b.server(t)
		for _, query := range []string{"facets=category&cursor=x", "facets=category&sort=name", "facets=category&page_size=2"} {
			var body parsehttp.Body
			assert.Equal(t, http.StatusUnprocessableEntity, call(t, srv, http.MethodOptions, "/api/products?"+query, "", &body), query)
			assert.Equal(t, filter.ECodeInvalidListParam.String(), body.Code, "%s: %s", query, body.Message)
		}
	})
}

// A request that runs out of time is 504 at WARN; a request whose client
// left is not answered and is logged at DEBUG. Neither is a server fault, on
// any route that reaches the database.
func TestServer_DeadlinesAndCancellationsAreNoServerFaults(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		db := b.open(t)
		var logs bytes.Buffer
		h, err := newServer(db, db.products, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), 5*time.Second, rand.Text)
		require.NoError(t, err)
		expired, cancelExpired := context.WithDeadline(t.Context(), time.Unix(0, 0))
		t.Cleanup(cancelExpired)
		cancelled, cancel := context.WithCancel(t.Context())
		cancel()

		routes := []struct{ method, path, body string }{
			{http.MethodGet, "/api/products", ""},
			{http.MethodPost, "/api/products", `{"page_size":2}`},
			{http.MethodOptions, "/api/products?facets=category", ""},
			{http.MethodPatch, "/api/products/p03", `{"price":7}`},
			{http.MethodPatch, "/api/products?id[]=p03", `{"price":7}`},
		}
		serve := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
			logs.Reset()
			req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec
		}
		for _, rt := range routes {
			t.Run("deadline "+rt.method+" "+rt.path, func(t *testing.T) {
				rec := serve(expired, rt.method, rt.path, rt.body)
				assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
				var body parsehttp.Body
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				assert.Equal(t, codeTimeout, body.Code)
				assert.Contains(t, logs.String(), `"level":"WARN"`)
				assert.NotContains(t, logs.String(), `"level":"ERROR"`)
			})
			t.Run("cancelled "+rt.method+" "+rt.path, func(t *testing.T) {
				rec := serve(cancelled, rt.method, rt.path, rt.body)
				assert.Empty(t, rec.Body.String(), "no answer is written")
				assert.Empty(t, rec.Header().Get("Content-Type"))
				assert.Contains(t, logs.String(), `"level":"DEBUG"`)
				assert.NotContains(t, logs.String(), `"level":"WARN"`)
				assert.NotContains(t, logs.String(), `"level":"ERROR"`)
			})
		}
	})
}

// The README's route table is the example's half of the SDK contract: every
// route it lists answers the request the SDK sends, and every request here
// is a row of it.
func TestServer_ServesEveryRouteItsREADMEDocuments(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		readme, err := os.ReadFile("README.md")
		require.NoError(t, err)
		requests := map[string]struct{ method, path, body string }{
			"GET /api/products":              {http.MethodGet, "/api/products?page_size=2", ""},
			"POST /api/products":             {http.MethodPost, "/api/products", `{"page_size":2}`},
			"OPTIONS /api/products?facets=…": {http.MethodOptions, "/api/products?facets=category", ""},
			"PATCH /api/products/{id}":       {http.MethodPatch, "/api/products/p01", `{}`},
			"PATCH /api/products?id[]=…":     {http.MethodPatch, "/api/products?id[]=p01&id[]=p02", `{}`},
			"GET /api/{resource}/schema":     {http.MethodGet, "/api/products/schema", ""},
			"GET /api/schemas":               {http.MethodGet, "/api/schemas", ""},
			"GET /readyz":                    {http.MethodGet, "/readyz", ""},
		}
		assert.ElementsMatch(t, slices.Collect(maps.Keys(requests)), readmeRoutes(t, string(readme)))

		srv := b.server(t)
		for route, req := range requests {
			var body json.RawMessage
			assert.Equal(t, http.StatusOK, call(t, srv, req.method, req.path, req.body, &body), "%s: %s", route, body)
		}
	})
}

// readmeRoutes is the first cell of each row of the README's Routes table.
func readmeRoutes(t *testing.T, readme string) []string {
	t.Helper()
	_, section, ok := strings.Cut(readme, "\n## Routes\n")
	require.True(t, ok, "the README has a Routes section")
	section, _, _ = strings.Cut(section, "\n## ")
	var routes []string
	for line := range strings.Lines(section) {
		cell, ok := strings.CutPrefix(line, "| `")
		if !ok {
			continue
		}
		route, _, ok := strings.Cut(cell, "`")
		require.True(t, ok, line)
		routes = append(routes, route)
	}
	require.NotEmpty(t, routes)
	return routes
}
