//go:build unit

package parse_test

import (
	"database/sql"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

type widget struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,sort,facet,search" facet:"terms:10"`
	Rank      int64      `json:"rank"       db:"rank"       parse:"filter,sort"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	Tags      []string   `json:"tags"       db:"-"          parse:"filter,include" relation:"widget_tags,widget_id,tag_id,id,tenant_id" indexed:"true"`
}

func widgets(t *testing.T) *parse.Resource[widget] {
	t.Helper()
	r, err := parse.NewResource[widget]("widgets", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"), parse.PageSize(2, 3))
	require.NoError(t, err)
	return r
}

// widgetDB: tenant t1 holds w1–w5 (w4 deleted; w1–w3 share rank 1), t2 holds
// x1; tags are pairs in each row's tenant.
func widgetDB(t *testing.T) *sql.DB {
	t.Helper()
	db := dbtest.SQLite(t)
	for _, stmt := range []string{
		`CREATE TABLE widgets (id TEXT PRIMARY KEY, tenant_id TEXT, name TEXT, rank INTEGER, deleted_at TEXT)`,
		`CREATE TABLE widget_tags (widget_id TEXT, tag_id TEXT, tenant_id TEXT)`,
		`INSERT INTO widgets VALUES ('w1','t1','alpha',1,NULL), ('w2','t1','beta',1,NULL), ('w3','t1','gamma',1,NULL),
			('w4','t1','delta',2,'2026-01-01T00:00:00Z'), ('w5','t1','beta',2,NULL), ('x1','t2','alpha',1,NULL)`,
		`INSERT INTO widget_tags VALUES ('w1','red','t1'), ('w1','blue','t1'), ('w2','red','t1'), ('x1','red','t2'), ('w1','green','t2')`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	return db
}

func scanWidgets(t *testing.T, db *sql.DB, st parse.Statement) []widget {
	t.Helper()
	rows, err := db.Query(st.SQL, st.Args...)
	require.NoError(t, err, st.SQL)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []widget
	for rows.Next() {
		var w widget
		var deleted sql.NullString
		require.NoError(t, rows.Scan(&w.ID, &w.TenantID, &w.Name, &w.Rank, &deleted))
		if deleted.Valid {
			at, err := time.Parse(time.RFC3339, deleted.String)
			require.NoError(t, err)
			w.DeletedAt = &at
		}
		out = append(out, w)
	}
	require.NoError(t, rows.Err())
	return out
}

func ids(ws []widget) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.ID
	}
	return out
}

func list(t *testing.T, r *parse.Resource[widget], db *sql.DB, query string, s parse.Scope) (*filter.Page[widget], *parse.ListPlan) {
	t.Helper()
	q, err := url.ParseQuery(query)
	require.NoError(t, err)
	req, err := r.ParseList(q)
	require.NoError(t, err)
	plan, err := r.BuildList(filter.SQLiteDialect{}, req, s)
	require.NoError(t, err)
	page, err := r.Page(scanWidgets(t, db, plan.Page), plan)
	require.NoError(t, err)
	return page, plan
}

func TestNewResource_RefusesBadDeclarations(t *testing.T) {
	for name, build := range map[string]func() error{
		"table":         func() error { _, err := parse.NewResource[widget]("widgets; DROP"); return err },
		"key column":    func() error { _, err := parse.NewResource[widget]("widgets", parse.PrimaryKey("uuid")); return err },
		"tenant column": func() error { _, err := parse.NewResource[widget]("widgets", parse.TenantColumn("org_id")); return err },
		"deleted column": func() error {
			_, err := parse.NewResource[widget]("widgets", parse.DeletedColumn("removed_at"))
			return err
		},
		"zero page":       func() error { _, err := parse.NewResource[widget]("widgets", parse.PageSize(0, 10)); return err },
		"max below def":   func() error { _, err := parse.NewResource[widget]("widgets", parse.PageSize(20, 10)); return err },
		"relation column": func() error { _, err := parse.NewResource[widget]("widgets", parse.PrimaryKey("tags")); return err },
	} {
		t.Run(name, func(t *testing.T) { assert.Error(t, build()) })
	}
	assert.Panics(t, func() { parse.MustResource[widget]("widgets", parse.PrimaryKey("nope")) })
}

func TestResource_DeclaresItsIdentity(t *testing.T) {
	r := widgets(t)
	assert.Equal(t, "widgets", r.Table())
	assert.Equal(t, "id", r.PrimaryKeyColumn())
	assert.Equal(t, "tenant_id", r.TenantColumnName())
	assert.Equal(t, "deleted_at", r.DeletedColumnName())
	assert.Equal(t, []string{"widgets.id", "widgets.tenant_id", "widgets.name", "widgets.rank", "widgets.deleted_at"}, r.Columns(filter.PostgresDialect{}))
	for _, item := range []any{widget{ID: "w9"}, &widget{ID: "w9"}} {
		key, err := r.Key(item)
		require.NoError(t, err)
		assert.Equal(t, "w9", key)
	}
	sys := r.FilterSchema().SystemFields
	assert.Equal(t, core.FieldTypeTimestamp, sys["deleted_at"].DataType, "system fields are typed from T")
	assert.Equal(t, "widgets.tenant_id", sys["tenant_id"].SQLExpr)
}

func TestParseList_ReadsEveryListParameter(t *testing.T) {
	r := widgets(t)
	q, err := url.ParseQuery(`filter={"rank":1}&name=beta&sort=-rank&cursor=abc&page_size=9&include_total=true` +
		`&include_deleted=1&search=b&facets=name,name&include=tags&fields=id,name,id`)
	require.NoError(t, err)
	req, err := r.ParseList(q)
	require.NoError(t, err)
	assert.NotNil(t, req.Filter, "flat and JSON filters are one filter")
	assert.Equal(t, 2, req.Filter.ConditionCount)
	assert.Equal(t, "rank", req.Sort.Fields[0].Name)
	assert.Equal(t, filter.SortDesc, req.Sort.Fields[0].Direction)
	assert.Equal(t, "abc", req.Cursor)
	assert.Equal(t, 3, req.PageSize, "clamped to the maximum")
	assert.True(t, req.IncludeTotal)
	assert.True(t, req.IncludeDeleted)
	assert.Equal(t, "b", req.Search)
	assert.Equal(t, []string{"name"}, req.Facets)
	assert.Equal(t, []string{"tags"}, req.Include)
	assert.Equal(t, []string{"id", "name"}, req.Fields)

	empty, err := r.ParseList(url.Values{})
	require.NoError(t, err)
	assert.Equal(t, 2, empty.PageSize, "the default page size")
	assert.Nil(t, empty.Filter)
}

func TestParseList_RefusesWhatItCannotHonor(t *testing.T) {
	r := widgets(t)
	bare, err := parse.NewResource[widget]("widgets")
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		r     *parse.Resource[widget]
		query string
		code  string
	}{
		"non-numeric page size":           {r, "page_size=ten", filter.ECodeInvalidListParam.String()},
		"zero page size":                  {r, "page_size=0", filter.ECodeInvalidListParam.String()},
		"negative page size":              {r, "page_size=-1", filter.ECodeInvalidListParam.String()},
		"malformed flag":                  {r, "include_total=yes", filter.ECodeInvalidListParam.String()},
		"deleted rows of a table without": {bare, "include_deleted=true", filter.ECodeInvalidListParam.String()},
		"misspelt filter":                 {r, "nmae=beta", filter.ECodeUnknownField.String()},
		"unknown facet":                   {r, "facets=colour", filter.ECodeUnknownField.String()},
		"field that is not facetable":     {r, "facets=rank", filter.ECodeUnknownField.String()},
		"field that is not a relation":    {r, "include=name", filter.ECodeInvalidRelation.String()},
		"unknown sort":                    {r, "sort=colour", filter.ECodeSortFieldNotAllowed.String()},
	} {
		t.Run(name, func(t *testing.T) {
			q, buildErr := url.ParseQuery(tc.query)
			require.NoError(t, buildErr)
			_, buildErr = tc.r.ParseList(q)
			var pe *core.ParseError
			require.ErrorAs(t, buildErr, &pe)
			assert.Equal(t, tc.code, pe.Code)
		})
	}

	type plain struct {
		ID   string `db:"id" parse:"filter,sort"`
		Name string `db:"name" parse:"filter"`
	}
	unsearchable, err := parse.NewResource[plain]("plains")
	require.NoError(t, err)
	_, err = unsearchable.ParseList(url.Values{"search": {"x"}})
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, filter.ECodeInvalidListParam.String(), pe.Code)

	accepting, err := parse.NewResource[widget]("widgets", parse.AcceptParams("_", "view"))
	require.NoError(t, err)
	req, err := accepting.ParseList(url.Values{"_": {"123"}, "view": {"grid"}})
	require.NoError(t, err, "accepted parameters are the endpoint's")
	assert.Nil(t, req.Filter)
}

// GEN-05: a misspelt flat filter cannot widen a list through any entry
// point; list parameters and the names an endpoint accepts still pass.
func TestEveryEntryPoint_RefusesAMisspeltFilter(t *testing.T) {
	r, err := parse.NewResource[widget]("widgets", parse.AcceptParams("view"))
	require.NoError(t, err)
	entryPoints := map[string]func(url.Values) error{
		"Resource.ParseList": func(q url.Values) error { _, err := r.ParseList(q); return err },
		"Resource.ParseRequestFilter": func(q url.Values) error {
			_, err := r.ParseRequestFilter(q, filter.QueryParamAccept("view"))
			return err
		},
		"filter.QueryParamParser": func(q url.Values) error {
			_, err := filter.NewQueryParamParser(r.FilterSchema(), filter.QueryParamAccept("view")).Parse(q)
			return err
		},
	}
	for name, parseQuery := range entryPoints {
		t.Run(name, func(t *testing.T) {
			var pe *core.ParseError
			require.ErrorAs(t, parseQuery(url.Values{"nam": {"x"}}), &pe)
			assert.Equal(t, filter.ECodeUnknownField.String(), pe.Code)
			assert.NoError(t, parseQuery(url.Values{"name": {"x"}, "sort": {"-rank"}, "page_size": {"5"}, "view": {"grid"}}))
		})
	}
}

func TestBuildList_HoldsRowsToTheScope(t *testing.T) {
	r, db := widgets(t), widgetDB(t)
	_, err := r.BuildList(filter.SQLiteDialect{}, nil, parse.Scope{})
	assert.True(t, errors.Is(err, parse.ErrTenantRequired), "a tenant-scoped list needs its tenant")

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
		"search": {"search=gam", parse.Scope{Tenant: "t1"}, []string{"w3"}},
	} {
		t.Run(name, func(t *testing.T) {
			page, _ := list(t, r, db, tc.query, tc.scope)
			assert.Equal(t, tc.want, ids(page.Items))
		})
	}
}

// The key is the final keyset column: rows tied on the sort are neither
// skipped nor repeated across pages, walking forward or back.
func TestBuildList_WalksTiedSortValuesExactly(t *testing.T) {
	r, db := widgets(t), widgetDB(t)
	scope := parse.Scope{Tenant: "t1"}
	var seen []string
	query := "sort=rank&include_total=true"
	var last *filter.Page[widget]
	for range 5 {
		page, plan := list(t, r, db, query, scope)
		require.NotNil(t, plan.Count)
		var total int64
		require.NoError(t, db.QueryRow(plan.Count.SQL, plan.Count.Args...).Scan(&total))
		assert.Equal(t, int64(4), total, "the total counts the population, not the page")
		seen = append(seen, ids(page.Items)...)
		last = page
		if !page.PageInfo.HasNextPage {
			break
		}
		query = "sort=rank&include_total=true&cursor=" + url.QueryEscape(string(page.PageInfo.EndCursor))
	}
	assert.Equal(t, []string{"w1", "w2", "w3", "w5"}, seen)

	back, _ := list(t, r, db, "sort=rank&cursor="+url.QueryEscape(string(last.PageInfo.StartCursor)), scope)
	assert.Equal(t, []string{"w1", "w2"}, ids(back.Items), "the page before the last, in presentation order")
}

func TestBuildList_UsesTheDeclaredKeyOnRequestViews(t *testing.T) {
	type entry struct {
		Code string         `json:"code" db:"code"`
		Data map[string]any `json:"data" db:"data" parse:"filter"`
	}
	r := parse.MustResource[entry]("entries", parse.PrimaryKey("code"))
	view, err := r.WithPaths(filter.PathDecl{Path: "data.rank", Type: core.FieldTypeInt, Sort: true})
	require.NoError(t, err)
	for _, resource := range []*parse.Resource[entry]{r, view} {
		plan, buildErr := resource.BuildList(filter.SQLiteDialect{}, nil, parse.Scope{})
		require.NoError(t, buildErr)
		require.Len(t, plan.Keyset.Keys, 1)
		assert.Equal(t, "code", plan.Keyset.Keys[0].Name)
		assert.Contains(t, plan.Page.SQL, "ORDER BY entries.code COLLATE BINARY ASC")
	}
	req, err := view.ParseList(url.Values{filter.ParamSort: {"data.rank"}})
	require.NoError(t, err)
	plan, err := view.BuildList(filter.SQLiteDialect{}, req, parse.Scope{})
	require.NoError(t, err)
	require.Len(t, plan.Keyset.Keys, 2)
	assert.Equal(t, "data.rank", plan.Keyset.Keys[0].Name)
	assert.Equal(t, "code", plan.Keyset.Keys[1].Name)
}

func TestBuildList_FacetsCountThePopulation(t *testing.T) {
	r, db := widgets(t), widgetDB(t)
	_, plan := list(t, r, db, "facets=name&rank=1", parse.Scope{Tenant: "t1"})
	require.NotNil(t, plan.Facets)
	rows, err := db.Query(plan.Facets.SQL, plan.Facets.Args...)
	require.NoError(t, err)
	results, err := filter.ScanFacetUnion(rows)
	require.NoError(t, errors.Join(err, rows.Close()))
	require.Len(t, results, 1)
	counts := map[string]int64{}
	for _, b := range results[0].Buckets {
		counts[b.Key] = b.Count
	}
	assert.Equal(t, map[string]int64{"alpha": 1, "beta": 1, "gamma": 1}, counts)
}

func TestIncludeQueries_ReadThePagesRelationsInItsTenant(t *testing.T) {
	r, db := widgets(t), widgetDB(t)
	page, plan := list(t, r, db, "include=tags&page_size=3", parse.Scope{Tenant: "t1"})
	keys := make([]any, len(page.Items))
	for i, w := range page.Items {
		var err error
		keys[i], err = r.Key(w)
		require.NoError(t, err)
	}
	queries, err := r.IncludeQueries(plan, keys)
	require.NoError(t, err)
	require.Len(t, queries, 1)
	rows, err := db.Query(queries[0].SQL, queries[0].Args...)
	require.NoError(t, err)
	res, err := queries[0].Scan(rows)
	require.NoError(t, errors.Join(err, rows.Close()))
	grouped := res.IDs
	for k := range grouped {
		slices.Sort(grouped[k])
	}
	assert.Equal(t, map[string][]string{"w1": {"blue", "red"}, "w2": {"red"}}, grouped, "w1's t2 pair is not t1's")

	none, err := r.IncludeQueries(plan, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestPagePointers(t *testing.T) {
	r, db := widgets(t), widgetDB(t)
	q := url.Values{"page_size": {"2"}}
	req, err := r.ParseList(q)
	require.NoError(t, err)
	plan, err := r.BuildList(filter.SQLiteDialect{}, req, parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	var ptrs []*widget
	for _, w := range scanWidgets(t, db, plan.Page) {
		ptrs = append(ptrs, &w)
	}
	page, err := r.PagePointers(ptrs, plan)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.True(t, page.PageInfo.HasNextPage)
	assert.NotEmpty(t, page.PageInfo.EndCursor)
}

// A plan carries the table it was built for: another resource's keyset and
// includes name columns this one does not have.
func TestResource_RefusesAnotherResourcesPlan(t *testing.T) {
	r := widgets(t)
	other, err := parse.NewResource[widget]("other_widgets", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	plan, err := other.BuildList(filter.SQLiteDialect{}, nil, parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	assert.Equal(t, filter.SQLiteDialect{}, plan.Dialect())

	_, err = r.Page(nil, plan)
	assert.ErrorContains(t, err, `list plan built for "other_widgets"`)
	_, err = r.PagePointers(nil, plan)
	assert.Error(t, err)
	_, err = r.IncludeQueries(plan, []any{"w1"})
	assert.Error(t, err)
	_, err = r.Page(nil, nil)
	assert.ErrorContains(t, err, "nil list plan")
}

type account struct {
	ID         string `json:"id"          db:"id"          parse:"filter,sort"`
	TenantID   string `json:"tenant_id"   db:"tenant_id"`
	Email      string `json:"email"       db:"email"       parse:"filter,sort,patch"`
	SecretHash string `json:"-"           db:"secret_hash"`
	UpdatedAt  string `json:"updated_at"  db:"updated_at"`
}

// ReadColumns keeps a resource's sensitive columns in the database: lists
// and updates read only the declared columns, while stamps and validation
// still know every column of T.
func TestReadColumns_LimitWhatTheResourceReads(t *testing.T) {
	r, err := parse.NewResource[account]("accounts", parse.TenantColumn("tenant_id"),
		parse.ReadColumns("updated_at", "email", "id", "tenant_id"))
	require.NoError(t, err)
	assert.Equal(t, []string{"accounts.id", "accounts.tenant_id", "accounts.email", "accounts.updated_at"}, r.Columns(filter.PostgresDialect{}), "in T's field order")

	plan, err := r.BuildList(filter.PostgresDialect{}, nil, parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(plan.Page.SQL, "SELECT accounts.id, accounts.tenant_id, accounts.email, accounts.updated_at FROM accounts "), plan.Page.SQL)

	p, err := r.ParseMergePatch([]byte(`{"email":"a@example.com"}`))
	require.NoError(t, err)
	upd, err := r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID("a1"), parse.Scope{Tenant: "t1"}, parse.StampSQL("updated_at", "NOW()"))
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(upd.Owner.SQL, " RETURNING accounts.id, accounts.tenant_id, accounts.email, accounts.updated_at"), upd.Owner.SQL)
	assert.NotContains(t, upd.Owner.SQL+plan.Page.SQL, "secret_hash")
}

func TestReadColumns_RefusesWhatPagesCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		read []string
		want string
	}{
		{"not a column", []string{"id", "nope"}, `read column "nope" is not a column`},
		{"listed twice", []string{"id", "email", "id"}, `read column "id" is listed twice`},
		{"without the key", []string{"email"}, `omit the key "id"`},
		{"without a sort key", []string{"id", "tenant_id"}, `sort key "email" reads column "email"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse.NewResource[account]("accounts", parse.ReadColumns(tc.read...))
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

type profile struct {
	ID    string         `json:"id"    db:"id"    parse:"filter,sort"`
	Attrs map[string]any `json:"attrs" db:"attrs" parse:"filter"`
}

// A request's path sort keys read their document column, so a view cannot
// sort by a document the resource does not read.
func TestReadColumns_RefusesAPathSortOnAnUnreadDocument(t *testing.T) {
	r, err := parse.NewResource[profile]("profiles", parse.ReadColumns("id"))
	require.NoError(t, err)
	_, err = r.WithPaths(filter.PathDecl{Path: "attrs.rank", Type: core.FieldTypeInt, Sort: true})
	assert.ErrorContains(t, err, `reads column "attrs"`)
	_, err = r.WithPaths(filter.PathDecl{Path: "attrs.rank", Type: core.FieldTypeInt})
	assert.NoError(t, err, "a filter-only path reads nothing")
}
