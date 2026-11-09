//go:build unit || integration

package parse_test

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// walkPage is the page size walks take: small enough that every fixture
// order crosses several pages.
const walkPage = 2

// runPathWalks pages through PathRecords by every PathOrders sort.
func runPathWalks(t *testing.T, p pathDB) {
	t.Helper()
	loadPathRecords(t, p)
	r, err := parse.NewResource[filtertest.PathRecord]("path_records", parse.PageSize(walkPage, 10))
	require.NoError(t, err)
	walkOrders(t, walker[filtertest.PathRecord]{db: p.db, dialect: p.dialect, resource: r, scan: scanPathRecord,
		id: func(r filtertest.PathRecord) string { return r.ID }}, filtertest.PathOrders)
}

func scanPathRecord(rows *sql.Rows) (filtertest.PathRecord, error) {
	var rec filtertest.PathRecord
	var attrs, settings any
	if err := rows.Scan(&rec.ID, &attrs, &settings); err != nil {
		return rec, err
	}
	if s, ok := textOf(attrs).(string); ok {
		rec.Attrs = json.RawMessage(s)
	}
	if s, ok := textOf(settings).(string); ok {
		rec.Settings = &filtertest.Settings{}
		if err := json.Unmarshal([]byte(s), rec.Settings); err != nil {
			return rec, err
		}
	}
	return rec, nil
}

// walker lists a resource's rows from db: scan reads one row of the
// resource's SELECT, id names it.
type walker[T any] struct {
	db       *sql.DB
	dialect  filter.Dialect
	resource *parse.Resource[T]
	scan     func(*sql.Rows) (T, error)
	id       func(T) string
}

// walkOrders pages through w's rows by every sort in orders, walkPage rows
// at a time, forwards and then backwards from the last page's start
// cursor; both walks must give the order (row ids) the sort promises.
func walkOrders[T any](t *testing.T, w walker[T], orders map[string]string) {
	t.Helper()
	for sortParam, want := range orders {
		t.Run("sort="+sortParam, func(t *testing.T) {
			forward, lastStart := w.walk(t, sortParam, "", false)
			assert.Equal(t, want, forward, "forward walk")
			last := len(forward) % walkPage
			if last == 0 {
				last = walkPage
			}
			backward, _ := w.walk(t, sortParam, lastStart, true)
			assert.Equal(t, want, backward+forward[len(forward)-last:], "backward walk")
		})
	}
}

func (w walker[T]) walk(t *testing.T, sortParam, cursor string, backward bool) (string, string) {
	t.Helper()
	var pages []string
	var lastStart string
	for n := 0; ; n++ {
		require.Less(t, n, 20, "walk did not terminate")
		q := url.Values{"sort": {sortParam}, "page_size": {strconv.Itoa(walkPage)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		req, err := w.resource.ParseList(q)
		require.NoError(t, err)
		plan, err := w.resource.BuildList(w.dialect, req, parse.Scope{})
		require.NoError(t, err)
		rows, err := w.db.Query(plan.Page.SQL, plan.Page.Args...)
		require.NoError(t, err, plan.Page.SQL)
		var items []T
		for rows.Next() {
			item, err := w.scan(rows)
			require.NoError(t, err)
			items = append(items, item)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		page, err := w.resource.Page(items, plan)
		require.NoError(t, err)
		ids := ""
		for _, it := range page.Items {
			ids += w.id(it)
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
