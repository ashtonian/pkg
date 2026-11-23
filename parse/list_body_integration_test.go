//go:build integration

package parse_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
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

func TestListBodyTransport_SQLite(t *testing.T) {
	runListBodyTransport(t, dbtest.SQLite(t), filter.SQLiteDialect{})
}

func TestListBodyTransport_Postgres(t *testing.T) {
	runListBodyTransport(t, dbtest.Postgres(t), filter.PostgresDialect{})
}

func runListBodyTransport(t *testing.T, db *sql.DB, d filter.Dialect) {
	for _, stmt := range gizmoFixture {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	gizmos := parse.MustResource[gizmo]("gizmos", parse.PageSize(2, 10))
	const doc = `{"name":{"$in":["a","b","c"]}}`

	page := func(r *http.Request) *filter.Page[gizmo] {
		t.Helper()
		req, err := parsehttp.ParseList(r, gizmos)
		require.NoError(t, err)
		plan, err := gizmos.BuildList(d, req, parse.Scope{})
		require.NoError(t, err)
		var items []gizmo
		for _, row := range queryRows(t, db, plan.Page.SQL, plan.Page.Args) {
			items = append(items, gizmo{ID: int(listInt(t, row[0])), Name: fmt.Sprint(row[1]), Rank: int(listInt(t, row[2]))})
		}
		p, err := gizmos.Page(items, plan)
		require.NoError(t, err)
		return p
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
		var out []int
		for _, item := range p.Items {
			out = append(out, item.ID)
		}
		return out
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

func listInt(t *testing.T, v any) int64 {
	t.Helper()
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	}
	require.Fail(t, fmt.Sprintf("not an integer: %T %v", v, v))
	return 0
}
