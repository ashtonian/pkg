//go:build unit || integration

package parse_test

import (
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

const pathPage = 2

// runPathWalks pages through PathRecords by every PathOrders sort, two rows
// at a time, forwards and then backwards from the last page's start
// cursor; both walks must give the order the sort promises.
func runPathWalks(t *testing.T, p pathDB) {
	t.Helper()
	loadPathRecords(t, p)
	r, err := parse.NewResource[filtertest.PathRecord]("path_records", parse.PageSize(pathPage, 10))
	require.NoError(t, err)
	for sortParam, want := range filtertest.PathOrders {
		t.Run("sort="+sortParam, func(t *testing.T) {
			forward, lastStart := walkPaths(t, p, r, sortParam, "", false)
			assert.Equal(t, want, forward, "forward walk")
			last := len(forward) % pathPage
			if last == 0 {
				last = pathPage
			}
			backward, _ := walkPaths(t, p, r, sortParam, lastStart, true)
			assert.Equal(t, want, backward+forward[len(forward)-last:], "backward walk")
		})
	}
}

func walkPaths(t *testing.T, p pathDB, r *parse.Resource[filtertest.PathRecord], sortParam, cursor string, backward bool) (string, string) {
	t.Helper()
	var pages []string
	var lastStart string
	for n := 0; ; n++ {
		require.Less(t, n, 10, "walk did not terminate")
		q := url.Values{"sort": {sortParam}, "page_size": {strconv.Itoa(pathPage)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		req, err := r.ParseList(q)
		require.NoError(t, err)
		plan, err := r.BuildList(p.dialect, req, parse.Scope{})
		require.NoError(t, err)
		rows, err := p.db.Query(plan.Page.SQL, plan.Page.Args...)
		require.NoError(t, err, plan.Page.SQL)
		var items []filtertest.PathRecord
		for rows.Next() {
			var rec filtertest.PathRecord
			var attrs, settings any
			require.NoError(t, rows.Scan(&rec.ID, &attrs, &settings))
			if s, ok := textOf(attrs).(string); ok {
				rec.Attrs = json.RawMessage(s)
			}
			if s, ok := textOf(settings).(string); ok {
				rec.Settings = &filtertest.Settings{}
				require.NoError(t, json.Unmarshal([]byte(s), rec.Settings))
			}
			items = append(items, rec)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		page, err := r.Page(items, plan)
		require.NoError(t, err)
		ids := ""
		for _, it := range page.Items {
			ids += it.ID
		}
		pages = append(pages, ids)
		lastStart = string(page.PageInfo.StartCursor)
		if backward {
			if !page.PageInfo.HasPrevPage {
				break
			}
			cursor = string(page.PageInfo.StartCursor)
		} else {
			if !page.PageInfo.HasNextPage {
				break
			}
			cursor = string(page.PageInfo.EndCursor)
		}
	}
	out := ""
	if backward {
		for _, page := range slices.Backward(pages) {
			out += page
		}
		return out, lastStart
	}
	for _, s := range pages {
		out += s
	}
	return out, lastStart
}
