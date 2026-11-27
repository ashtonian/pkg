//go:build integration

package parse_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsehttp"
)

type gizmo struct {
	ID   int    `json:"id"   db:"id"   parse:"filter,sort"`
	Name string `json:"name" db:"name" parse:"filter,sort"`
	Rank int    `json:"rank" db:"rank" parse:"filter,sort"`
}

var gizmoFixture = []string{
	`CREATE TABLE gizmos (id INTEGER PRIMARY KEY, name TEXT NOT NULL, rank INTEGER NOT NULL)`,
	`INSERT INTO gizmos VALUES (1, 'b', 3), (2, 'a', 1), (3, 'b', 2), (4, 'c', 5), (5, 'a', 4), (6, 'b', 9), (7, 'z', 0)`,
}

func TestListBodyTransport(t *testing.T) {
	runSQLBackends(t, runListBodyTransport)
}

func runListBodyTransport(t *testing.T, b sqlBackend) {
	p, db := b.open(t)
	execStatements(t, db, gizmoFixture...)
	gizmos := parse.MustResource[gizmo](p, "gizmos", parse.PageSize(2, 10))
	const doc = `{"name":{"$in":["a","b","c"]}}`

	page := func(r *http.Request) *filter.Page[gizmo] {
		t.Helper()
		req, err := parsehttp.ParseList(r, gizmos)
		require.NoError(t, err)
		return listPage(t, gizmos, req, parse.Scope{})
	}
	get := func(cursor string) *http.Request {
		q := url.Values{filter.ParamFilter: {doc}, filter.ParamSort: {"name,-rank"}}
		if cursor != "" {
			q.Set(filter.ParamCursor, cursor)
		}
		return httptest.NewRequest(http.MethodGet, "/gizmos?"+q.Encode(), nil)
	}
	post := func(cursor string) *http.Request {
		body := map[string]any{"filter": json.RawMessage(doc), "sort": "name,-rank"}
		if cursor != "" {
			body["cursor"] = cursor
		}
		b, err := json.Marshal(body)
		require.NoError(t, err)
		r := httptest.NewRequest(http.MethodPost, "/gizmos", strings.NewReader(string(b)))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	ids := func(p *filter.Page[gizmo]) []int {
		return itemIDs(p.Items, func(g gizmo) int { return g.ID })
	}

	first := page(get(""))
	assert.Equal(t, ids(first), ids(page(post(""))), "both transports read the same first page")
	assert.Equal(t, first.PageInfo, page(post("")).PageInfo, "and mint the same cursors")

	// The walk alternates transports: each page continues the cursor the
	// other transport minted.
	var walked []int
	cursor, transports := "", []func(string) *http.Request{get, post}
	for i := range 10 {
		p := page(transports[i%2](cursor))
		walked = append(walked, ids(p)...)
		if !p.PageInfo.HasNextPage {
			break
		}
		cursor = string(p.PageInfo.EndCursor)
	}
	assert.Equal(t, []int{5, 2, 6, 1, 3, 4}, walked, "name ascending, then rank descending; z is filtered out")
}
