//go:build unit || integration

package parse_test

import (
	"database/sql"
	"maps"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// timestampDB is a database under the timestamp fixture, the dialect that
// writes its rows and the type its at column takes there.
type timestampDB struct {
	db      *sql.DB
	dialect interface {
		filter.Dialect
		MarshalValue(value any, t core.TypeInfo) (any, error)
	}
	atType string
}

// runTimestamps writes TimestampRecords through the dialect's MarshalValue,
// each row in its own offset, then checks that facets, filters, sorts and
// keyset walks on the column read instants: the buckets, rows and orders
// the fixture names, the same on every backend.
func runTimestamps(t *testing.T, d timestampDB) {
	t.Helper()
	schema, err := filtertest.TimestampSchema()
	require.NoError(t, err)
	_, err = d.db.Exec("CREATE TABLE " + schema.Name + " (id TEXT PRIMARY KEY, at " + d.atType + ")")
	require.NoError(t, err)
	insert := "INSERT INTO " + schema.Name + " (id, at) VALUES (" + d.dialect.Placeholder(1) + ", " + d.dialect.Placeholder(2) + ")"
	for _, r := range filtertest.TimestampRecords() {
		at, err := d.dialect.MarshalValue(r.At, core.TypeInfo{Type: core.FieldTypeTimestamp})
		require.NoError(t, err, r.ID)
		_, err = d.db.Exec(insert, r.ID, at)
		require.NoError(t, err, r.ID)
	}
	res, err := parse.NewResource[filtertest.TimestampRecord](schema.Name, parse.PageSize(trackLimit, trackLimit))
	require.NoError(t, err)

	t.Run("facet", func(t *testing.T) {
		q, err := filter.BuildFacetQuery(schema, d.dialect, "1=1", nil, []filter.FacetRequest{{Field: "at"}})
		require.NoError(t, err)
		rows, err := d.db.Query(q.SQL, q.Args...)
		require.NoError(t, err, q.SQL)
		results, err := filter.ScanFacetUnion(rows)
		require.NoError(t, err)
		require.NoError(t, rows.Close())
		require.Len(t, results, 1)
		got := map[string]int64{}
		for _, b := range results[0].Buckets {
			got[b.Key] = b.Count
		}
		assert.Equal(t, filtertest.TimestampFacets, got)
	})

	for _, doc := range slices.Sorted(maps.Keys(filtertest.TimestampFilters)) {
		t.Run("filter "+doc, func(t *testing.T) {
			f, err := res.ParseFilter(doc)
			require.NoError(t, err)
			where, err := filter.NewSQLBuilder(schema, d.dialect).Build(f.AST)
			require.NoError(t, err)
			rows, err := d.db.Query("SELECT id FROM "+schema.Name+" WHERE "+where.Where+" ORDER BY id", where.Args...)
			require.NoError(t, err)
			defer func() { require.NoError(t, rows.Close()) }()
			var ids strings.Builder
			for rows.Next() {
				var id string
				require.NoError(t, rows.Scan(&id))
				ids.WriteString(id)
			}
			require.NoError(t, rows.Err())
			assert.Equal(t, filtertest.TimestampFilters[doc], ids.String())
		})
	}

	for _, sort := range slices.Sorted(maps.Keys(filtertest.TimestampOrders)) {
		t.Run("sort="+sort, func(t *testing.T) {
			want := filtertest.TimestampOrders[sort]
			forward, lastStart := walkTimestamps(t, d, res, sort, "", false)
			assert.Equal(t, want, forward, "forward walk")
			backward, _ := walkTimestamps(t, d, res, sort, lastStart, true)
			assert.Equal(t, want, backward+want[len(want)-lastPageLen(len(want)):], "backward walk from the last page")
		})
	}
}

// walkTimestamps pages through the list from cursor, following end cursors
// forwards or start cursors backwards, and returns the ids in presentation
// order (and, going forwards, the last page's start cursor).
func walkTimestamps(t *testing.T, d timestampDB, res *parse.Resource[filtertest.TimestampRecord], sort, cursor string, backward bool) (string, string) {
	t.Helper()
	var pages []string
	var lastStart string
	for n := 0; ; n++ {
		require.Less(t, n, 10, "walk did not terminate")
		q := url.Values{filter.ParamSort: {sort}}
		if cursor != "" {
			q.Set(filter.ParamCursor, cursor)
		}
		req, err := res.ParseList(q)
		require.NoError(t, err)
		plan, err := res.BuildList(d.dialect, req, parse.Scope{})
		require.NoError(t, err)
		page, err := res.Page(resourceRows(t, d.db, res, d.dialect, plan.Page), plan)
		require.NoError(t, err)
		var ids strings.Builder
		for _, r := range page.Items {
			ids.WriteString(r.ID)
		}
		pages = append(pages, ids.String())
		lastStart = string(page.PageInfo.StartCursor)
		if backward && page.PageInfo.HasPrevPage {
			cursor = string(page.PageInfo.StartCursor)
			continue
		}
		if !backward && page.PageInfo.HasNextPage {
			cursor = string(page.PageInfo.EndCursor)
			continue
		}
		break
	}
	if backward {
		slices.Reverse(pages)
	}
	return strings.Join(pages, ""), lastStart
}
