//go:build integration

package corpus

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

// pagedWidget is the row the list cases page, facet and include through.
type pagedWidget struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,sort,facet,search" facet:"terms:10"`
	Rank      int64      `json:"rank"       db:"rank"       parse:"filter,sort"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	Tags      []string   `json:"tags"       db:"-"          parse:"filter,include" relation:"widget_tags,widget_id,tag_id,id,tenant_id" indexed:"true"`
}

func widgetID(w pagedWidget) string { return w.ID }

// widgetStore is the widget resource on a fresh database of b: tenant t1
// holds w1–w5 (w4 deleted; w1–w3 share rank 1), t2 holds x1; tags are pairs
// in each row's tenant. The resource's clock reads one instant.
func widgetStore(t *testing.T, b sqlBackend) (*parse.Resource[pagedWidget], *sql.DB) {
	t.Helper()
	p, db := b.open(t)
	deleted := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b.load(t, db, sqlTable{name: "widgets", key: []string{"id"}, columns: []sqlColumn{
		column[string]("id"), column[string]("tenant_id"), column[string]("name"), column[int64]("rank"), column[*time.Time]("deleted_at"),
	}}, [][]any{
		{"w1", "t1", "alpha", 1, nil}, {"w2", "t1", "beta", 1, nil}, {"w3", "t1", "gamma", 1, nil},
		{"w4", "t1", "delta", 2, deleted}, {"w5", "t1", "beta", 2, nil}, {"x1", "t2", "alpha", 1, nil},
	})
	b.load(t, db, sqlTable{name: "widget_tags", columns: []sqlColumn{column[string]("widget_id"), column[string]("tag_id"), column[string]("tenant_id")}},
		[][]any{{"w1", "red", "t1"}, {"w1", "blue", "t1"}, {"w2", "red", "t1"}, {"x1", "red", "t2"}, {"w1", "green", "t2"}})
	clock := core.WithClock(func() time.Time { return deleted })
	r, err := parse.NewResource[pagedWidget](p, "widgets", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"), parse.PageSize(2, 3), parse.ParserOptions(clock))
	require.NoError(t, err)
	return r, db
}

// widgetList builds query under s and reads the plan's page by hand on r's
// database, for a case that reads the plan's other statements itself.
func widgetList(t *testing.T, r *parse.Resource[pagedWidget], query string, s parse.Scope) (*filter.Page[pagedWidget], *parse.ListPlan) {
	t.Helper()
	q, err := url.ParseQuery(query)
	require.NoError(t, err)
	req, err := r.ParseList(q)
	require.NoError(t, err)
	plan, err := r.BuildList(req, s)
	require.NoError(t, err)
	page, err := r.Page(parsetest.ResourceRows(t, r, plan.Page), plan)
	require.NoError(t, err)
	return page, plan
}

// A list plan's statements, each read by hand, hold its rows to the scope,
// walk tied sort values exactly, count and facet the population and include
// the page's relations in its tenant; a page of the rows read, or of none,
// encodes as the handlers write it.
func widgetLists(t *testing.T, engine Engine) {
	runOn(t, engine, runWidgetLists)
}

func runWidgetLists(t *testing.T, b sqlBackend) {
	t.Run("rows are held to the scope", func(t *testing.T) {
		r, _ := widgetStore(t, b)
		_, err := r.BuildList(nil, parse.Scope{})
		require.ErrorIs(t, err, parse.ErrTenantRequired, "a tenant-scoped list needs its tenant")

		for name, tc := range map[string]struct {
			query string
			scope parse.Scope
			want  []string
		}{
			"tenant floor and deleted rows masked": {"page_size=3", parse.Scope{Tenant: "t1"}, []string{"w1", "w2", "w3"}},
			"another tenant":                       {"", parse.Scope{Tenant: "t2"}, []string{"x1"}},
			"deleted rows when the server says so": {"rank=2", parse.Scope{Tenant: "t1", IncludeDeleted: true}, []string{"w4", "w5"}},
			"a request cannot ask for them alone":  {"rank=2&include_deleted=true", parse.Scope{Tenant: "t1"}, []string{"w5"}},
			"policy ANDed with the request": {"page_size=3", parse.Scope{Tenant: "t1", Policy: &filter.PolicyConstraints{
				Constraints: []*filter.PolicyConstraint{{Field: "name", Operator: filter.OpIn, Value: []any{"alpha", "gamma", "beta"}}},
			}}, []string{"w1", "w2", "w3"}},
			"policy excludes": {"name=beta", parse.Scope{Tenant: "t1", Policy: &filter.PolicyConstraints{
				Constraints: []*filter.PolicyConstraint{{Field: "id", Operator: filter.OpNe, Value: "w2"}},
			}}, []string{"w5"}},
			// A whole word: SQLite searches by substring, PostgreSQL by
			// full-text match.
			"search": {"search=gamma", parse.Scope{Tenant: "t1"}, []string{"w3"}},
		} {
			t.Run(name, func(t *testing.T) {
				page, _ := widgetList(t, r, tc.query, tc.scope)
				assert.Equal(t, tc.want, parsetest.ItemIDs(page.Items, widgetID))
			})
		}
	})

	// The key is the final keyset column: rows tied on the sort are neither
	// skipped nor repeated across pages, walking forward or back.
	t.Run("tied sort values walk exactly", func(t *testing.T) {
		r, db := widgetStore(t, b)
		scope := parse.Scope{Tenant: "t1"}
		var seen []string
		query := "sort=rank&include_total=true"
		var last *filter.Page[pagedWidget]
		for range 5 {
			page, plan := widgetList(t, r, query, scope)
			require.NotNil(t, plan.Count)
			var total int64
			require.NoError(t, db.QueryRowContext(t.Context(), plan.Count.SQL, plan.Count.Args...).Scan(&total))
			assert.Equal(t, int64(4), total, "the total counts the population, not the page")
			seen = append(seen, parsetest.ItemIDs(page.Items, widgetID)...)
			last = page
			if !page.PageInfo.HasNextPage {
				break
			}
			query = "sort=rank&include_total=true&cursor=" + url.QueryEscape(string(page.PageInfo.EndCursor))
		}
		assert.Equal(t, []string{"w1", "w2", "w3", "w5"}, seen)

		back, _ := widgetList(t, r, "sort=rank&cursor="+url.QueryEscape(string(last.PageInfo.StartCursor)), scope)
		assert.Equal(t, []string{"w1", "w2"}, parsetest.ItemIDs(back.Items, widgetID), "the page before the last, in presentation order")
	})

	t.Run("facets count the population", func(t *testing.T) {
		r, db := widgetStore(t, b)
		_, plan := widgetList(t, r, "facets=name&rank=1", parse.Scope{Tenant: "t1"})
		require.NotNil(t, plan.Facets)
		rows, err := db.QueryContext(t.Context(), plan.Facets.SQL, plan.Facets.Args...)
		require.NoError(t, err)
		defer func() { require.NoError(t, rows.Close()) }()
		results, err := filter.ScanFacetUnion(rows)
		require.NoError(t, err)
		require.NoError(t, rows.Err())
		require.Len(t, results, 1)
		counts := map[string]int64{}
		for _, bucket := range results[0].Buckets {
			counts[bucket.Key] = bucket.Count
		}
		assert.Equal(t, map[string]int64{"alpha": 1, "beta": 1, "gamma": 1}, counts)
	})

	t.Run("includes read the page's relations in its tenant", func(t *testing.T) {
		r, db := widgetStore(t, b)
		page, plan := widgetList(t, r, "include=tags&page_size=3", parse.Scope{Tenant: "t1"})
		keys := make([]parse.Key, len(page.Items))
		for i, w := range page.Items {
			var err error
			keys[i], err = r.Key(w)
			require.NoError(t, err)
		}
		queries, err := r.IncludeQueries(plan, keys)
		require.NoError(t, err)
		require.Len(t, queries, 1)
		rows, err := db.QueryContext(t.Context(), queries[0].SQL, queries[0].Args...)
		require.NoError(t, err)
		defer func() { require.NoError(t, rows.Close()) }()
		res, err := queries[0].Scan(rows)
		require.NoError(t, err)
		require.NoError(t, rows.Err())
		grouped := res.IDs
		for k := range grouped {
			slices.Sort(grouped[k])
		}
		assert.Equal(t, map[string][]string{"w1": {"blue", "red"}, "w2": {"red"}}, grouped, "w1's t2 pair is not t1's")

		none, err := r.IncludeQueries(plan, nil)
		require.NoError(t, err)
		assert.Empty(t, none)
	})

	t.Run("a page of pointers", func(t *testing.T) {
		r, _ := widgetStore(t, b)
		req, err := r.ParseList(url.Values{"page_size": {"2"}})
		require.NoError(t, err)
		plan, err := r.BuildList(req, parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		var ptrs []*pagedWidget
		for _, w := range parsetest.ResourceRows(t, r, plan.Page) {
			ptrs = append(ptrs, &w)
		}
		page, err := r.PagePointers(ptrs, plan)
		require.NoError(t, err)
		require.Len(t, page.Items, 2)
		assert.True(t, page.PageInfo.HasNextPage)
		assert.NotEmpty(t, page.PageInfo.EndCursor)
	})

	t.Run("a page of no rows encodes its items as an array", func(t *testing.T) {
		r, _ := widgetStore(t, b)
		req, err := r.ParseList(url.Values{"name": {"no such widget"}})
		require.NoError(t, err)
		plan, err := r.BuildList(req, parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		scanned := parsetest.ResourceRows(t, r, plan.Page)
		require.Empty(t, scanned)

		rows := func(items []pagedWidget) func() (any, error) {
			return func() (any, error) { return r.Page(items, plan) }
		}
		pointers := func(items []*pagedWidget) func() (any, error) {
			return func() (any, error) { return r.PagePointers(items, plan) }
		}
		for _, tt := range []struct {
			name string
			page func() (any, error)
		}{
			{"the scanned rows", rows(scanned)},
			{"nil rows", rows(nil)},
			{"no rows", rows([]pagedWidget{})},
			{"nil pointers", pointers(nil)},
			{"no pointers", pointers([]*pagedWidget{})},
		} {
			t.Run(tt.name, func(t *testing.T) {
				page, err := tt.page()
				require.NoError(t, err)
				got, err := json.Marshal(page)
				require.NoError(t, err)
				assert.JSONEq(t, `{"items":[],"page_info":{"has_next_page":false,"has_prev_page":false}}`, string(got))
			})
		}
	})
}

type customItem struct {
	ID     string          `json:"id"     db:"id"     parse:"filter,sort"`
	Custom json.RawMessage `json:"custom" db:"custom" parse:"filter"`
}

// A request declares its tenant's custom fields as paths on a view of the
// resource; the shared resource never learns them.
func requestViewPaths(t *testing.T, engine Engine) {
	runOn(t, engine, runRequestViewPaths)
}

func runRequestViewPaths(t *testing.T, b sqlBackend) {
	p, db := b.open(t)
	r, err := parse.NewResource[customItem](p, "custom_items", parse.PageSize(10, 10))
	require.NoError(t, err)
	view, err := r.WithPaths(filter.PathDecl{Path: "custom.tier", Type: core.FieldTypeString, Sort: true, Facet: &filter.FacetSchema{Type: filter.FacetTerms}})
	require.NoError(t, err)

	q := url.Values{"filter": {`{"custom.tier":{"$ne":"bronze"}}`}, "sort": {"-custom.tier"}, "facets": {"custom.tier"}}
	_, err = r.ParseList(q)
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe, "the shared resource does not know the path: %v", err)
	assert.Equal(t, "E2071", pe.Code)

	req, err := view.ParseList(q)
	require.NoError(t, err)
	plan, err := view.BuildList(req, parse.Scope{})
	require.NoError(t, err)

	b.load(t, db, sqlTable{name: "custom_items", key: []string{"id"}, columns: []sqlColumn{column[string]("id"), column[json.RawMessage]("custom")}},
		[][]any{{"a", `{"tier":"gold"}`}, {"b", `{"tier":"silver"}`}, {"c", `{"tier":"bronze"}`}, {"d", `{}`}, {"e", nil}})
	page, err := view.List(t.Context(), req, parse.Scope{})
	require.NoError(t, err)
	assert.Equal(t, []string{"b", "a"}, parsetest.ItemIDs(page.Items, func(it customItem) string { return it.ID }), "silver then gold; bronze filtered out, absent tiers never match $ne")

	require.NotNil(t, plan.Facets)
	require.Len(t, page.Facets, 1)
	assert.ElementsMatch(t, []filter.FacetBucket{{Key: "gold", Count: 1}, {Key: "silver", Count: 1}}, page.Facets["custom.tier"])

	_, err = r.WithPaths(filter.PathDecl{Path: "id.x", Type: core.FieldTypeString})
	assert.Error(t, err, "a path needs a document field")
}
