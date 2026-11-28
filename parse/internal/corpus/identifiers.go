//go:build integration

package corpus

import (
	"cmp"
	"database/sql"
	"net/url"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

// The tables and columns below are reserved words (user, order, group,
// check, limit, table, column, grant) or mixed case (createdAt): every
// statement must quote them to read them at all, and PostgreSQL folds an
// unquoted createdAt to a column that does not exist.
type reservedUser struct {
	ID        int64  `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID  int64  `json:"-"         db:"tenant_id"`
	Order     int64  `json:"order"     db:"order"     parse:"filter,sort,patch"`
	Group     string `json:"group"     db:"group"     parse:"filter,sort,patch,facet,search"`
	CreatedAt string `json:"createdAt" db:"createdAt" parse:"filter,sort"`
}

type reservedCheck struct {
	ID       int64  `json:"id"    db:"id"    parse:"filter,sort"`
	TenantID int64  `json:"-"     db:"tenant_id"`
	User     int64  `json:"user"  db:"user"  parse:"filter"`
	Limit    string `json:"limit" db:"limit" parse:"filter"`
}

type reservedTable struct {
	ID       int64  `json:"id"     db:"id"     parse:"filter,sort"`
	TenantID int64  `json:"-"      db:"tenant_id"`
	Column   string `json:"column" db:"column" parse:"filter"`
}

// reservedTypo reads the user table through a column it does not have.
type reservedTypo struct {
	ID    int64 `json:"id"   db:"id"   parse:"filter,sort"`
	Order int64 `json:"ordr" db:"ordr" parse:"filter,sort"`
}

// loadReserved loads the reserved tables. Tenant 2's rows point at tenant
// 1's users, so a relation that lost its tenant floor would match them.
func loadReserved(t *testing.T, b sqlBackend, db *sql.DB) {
	t.Helper()
	id, tenant := column[int64]("id").required(), column[int64]("tenant_id").required()
	b.load(t, db, sqlTable{name: "user", key: []string{"id"}, columns: []sqlColumn{
		id, tenant, column[int64]("order").required(), column[string]("group").required(), column[string]("createdAt").required(),
	}}, [][]any{
		{int64(1), int64(1), int64(3), "beta", "2026-01-03"},
		{int64(2), int64(1), int64(1), "alpha", "2026-01-01"},
		{int64(3), int64(1), int64(2), "alpha", "2026-01-02"},
		{int64(4), int64(2), int64(9), "alpha", "2026-01-04"},
	})
	b.load(t, db, sqlTable{name: "check", key: []string{"id"}, columns: []sqlColumn{
		id, tenant, column[int64]("user").required(), column[string]("limit").required(),
	}}, [][]any{{int64(10), int64(1), int64(1), "hard"}, {int64(11), int64(1), int64(2), "soft"}, {int64(12), int64(2), int64(3), "hard"}})
	b.load(t, db, sqlTable{name: "table", key: []string{"id"}, columns: []sqlColumn{
		id, tenant, column[string]("column").required(),
	}}, [][]any{{int64(20), int64(1), "x"}, {int64(21), int64(1), "y"}, {int64(22), int64(2), "z"}})
	b.load(t, db, sqlTable{name: "grant", key: []string{"user", "table"}, columns: []sqlColumn{
		column[int64]("user").required(), column[int64]("table").required(), tenant,
	}}, [][]any{{int64(1), int64(20), int64(1)}, {int64(2), int64(20), int64(1)}, {int64(2), int64(21), int64(1)}, {int64(3), int64(22), int64(2)}})
}

func reservedUsers(t *testing.T, p *parse.Provider) *parse.Resource[reservedUser] {
	t.Helper()
	users := parse.MustResource[reservedUser](p, "user", parse.TenantColumn("tenant_id"))
	checks := parse.MustResource[reservedCheck](p, "check", parse.TenantColumn("tenant_id"))
	tables := parse.MustResource[reservedTable](p, "table", parse.TenantColumn("tenant_id"))
	users.MustRelate("checks", parse.HasMany(checks, "user", parse.Indexed()))
	users.MustRelate("tables", parse.ManyToMany(tables,
		parse.Junction{Table: "grant", Source: []string{"user"}, Target: []string{"table"}, Tenant: "tenant_id"},
		parse.Indexed(), parse.Includable(), parse.Facetable(), parse.Writable()))
	return users
}

func reservedIdentifiers(t *testing.T, engine Engine) {
	runOn(t, engine, runReservedIdentifiers)
}

func runReservedIdentifiers(t *testing.T, b sqlBackend) {
	p, db := b.open(t)
	users := reservedUsers(t, p)
	loadReserved(t, b, db)
	list := func(q url.Values) *filter.Page[reservedUser] {
		t.Helper()
		req, err := users.ParseList(q)
		require.NoError(t, err)
		return listPage(t, users, req, parse.Scope{Tenant: "1"})
	}
	ids := func(p *filter.Page[reservedUser]) []int64 {
		return parsetest.ItemIDs(p.Items, func(u reservedUser) int64 { return u.ID })
	}

	t.Run("lists, pages and counts", func(t *testing.T) {
		q := url.Values{
			filter.ParamFilter:       {`{"group":{"$in":["alpha","beta"]},"createdAt":{"$ne":"2026-01-09"},"order":{"$gt":0}}`},
			filter.ParamSort:         {"-order"},
			filter.ParamPageSize:     {"2"},
			filter.ParamIncludeTotal: {"true"},
		}
		first := list(q)
		assert.Equal(t, []int64{1, 3}, ids(first))
		require.True(t, first.PageInfo.HasNextPage)
		require.NotNil(t, first.PageInfo.TotalCount)
		assert.Equal(t, int64(3), *first.PageInfo.TotalCount)

		q.Set(filter.ParamCursor, string(first.PageInfo.EndCursor))
		assert.Equal(t, []int64{2}, ids(list(q)))

		byCreated := url.Values{filter.ParamSort: {"createdAt"}, filter.ParamPageSize: {"1"}}
		created := list(byCreated)
		assert.Equal(t, []int64{2}, ids(created))
		byCreated.Set(filter.ParamCursor, string(created.PageInfo.EndCursor))
		assert.Equal(t, []int64{3}, ids(list(byCreated)), "the mixed-case key seeks")
	})

	t.Run("searches and facets", func(t *testing.T) {
		if b.engine == Memory {
			req, err := users.ParseList(url.Values{filter.ParamSearch: {"alpha"}})
			require.NoError(t, err)
			_, err = users.List(t.Context(), req, parse.Scope{Tenant: "1"})
			require.ErrorIs(t, err, parse.ErrUnsupported, "the memory provider does not search")
		} else {
			assert.Equal(t, []int64{2, 3}, ids(list(url.Values{filter.ParamSearch: {"alpha"}, filter.ParamSort: {"id"}})))
		}

		counts := map[string]int64{}
		for field, buckets := range list(url.Values{filter.ParamFacets: {"group,tables"}}).Facets {
			for _, b := range buckets {
				counts[field+"="+b.Key] = b.Count
			}
		}
		assert.Equal(t, map[string]int64{"group=alpha": 2, "group=beta": 1, "tables=20": 2, "tables=21": 1}, counts)
	})

	t.Run("filters through relations", func(t *testing.T) {
		for _, tc := range []struct {
			filter string
			want   []int64
		}{
			{`{"checks":{"$some":{"limit":"hard"}}}`, []int64{1}},
			{`{"checks":{"$none":{}}}`, []int64{3}},
			{`{"tables":21}`, []int64{2}},
			{`{"tables":{"$some":{"column":"x"}}}`, []int64{1, 2}},
		} {
			got := ids(list(url.Values{filter.ParamFilter: {tc.filter}, filter.ParamSort: {"id"}}))
			assert.Equal(t, tc.want, got, tc.filter)
		}
	})

	t.Run("includes the tenant's pairs", func(t *testing.T) {
		got := list(url.Values{filter.ParamInclude: {"tables"}})
		assert.Equal(t, map[string][]string{"1": {"20"}, "2": {"20", "21"}}, got.Relations["tables"])
	})

	t.Run("updates columns and memberships", func(t *testing.T) {
		parsed, err := users.ParseMergePatch([]byte(`{"order":7,"group":"gamma"}`))
		require.NoError(t, err)
		res, err := users.Update(t.Context(), parsed, parse.ByID(int64(1)), parse.Scope{Tenant: "1"}, parse.SkipUnchanged())
		require.NoError(t, err)
		require.Len(t, res.Changed, 1)
		assert.Equal(t, int64(7), res.Changed[0].Order)
		assert.Equal(t, "gamma", res.Changed[0].Group)
		res, err = users.Update(t.Context(), parsed, parse.ByID(int64(1)), parse.Scope{Tenant: "1"}, parse.SkipUnchanged())
		require.NoError(t, err)
		assert.Empty(t, res.Changed, "the unchanged row is skipped")
		require.Len(t, res.Unchanged, 1)

		parsed, err = users.ParseMergePatch([]byte(`{"tables":[21]}`))
		require.NoError(t, err)
		res, err = users.Update(t.Context(), parsed, parse.ByID(int64(1)), parse.Scope{Tenant: "1"})
		require.NoError(t, err)
		require.Len(t, res.Unchanged, 1)
		assert.Equal(t, int64(2), res.RelationRows, "20 removed, 21 added")
		var pairs []int64
		for _, row := range b.tableRows(t, db, "grant", "user", "table") {
			if asInt64(t, row[0]) == 1 {
				pairs = append(pairs, asInt64(t, row[1]))
			}
		}
		assert.Equal(t, []int64{21}, pairs)
	})

	t.Run("updates a batch", func(t *testing.T) {
		var rows []parse.RowUpdate
		for _, u := range []struct {
			id  int64
			raw string
		}{{2, `{"order":5}`}, {3, `{"group":"delta"}`}} {
			parsed, err := users.ParseMergePatch([]byte(u.raw))
			require.NoError(t, err)
			rows = append(rows, parse.RowUpdate{Key: parse.Key{u.id}, Patch: parsed})
		}
		res, err := users.UpdateBatch(t.Context(), rows, parse.Scope{Tenant: "1"}, parse.UpdateBatchLimits{}, parse.SkipUnchanged())
		require.NoError(t, err)
		assert.Len(t, res.Changed, 2)
		assert.Empty(t, res.Unchanged)
		var got [][]any
		for _, row := range b.tableRows(t, db, "user", "id", "order", "group") {
			id := asInt64(t, row[0])
			if id == 2 || id == 3 {
				got = append(got, row)
			}
		}
		slices.SortFunc(got, func(a, c []any) int { return cmp.Compare(asInt64(t, a[0]), asInt64(t, c[0])) })
		require.Len(t, got, 2)
		assert.Equal(t, []any{int64(2), int64(5), "alpha"}, []any{asInt64(t, got[0][0]), asInt64(t, got[0][1]), columnText(t, got[0][2])})
		assert.Equal(t, []any{int64(3), int64(2), "delta"}, []any{asInt64(t, got[1][0]), asInt64(t, got[1][1]), columnText(t, got[1][2])})
	})

	// A statement qualifies every column it reads: SQLite reads an
	// unqualified quoted name that matches no column as a string, which
	// would compare and sort a misspelt column as a constant.
	t.Run("a misspelt column fails", func(t *testing.T) {
		b.sqlOnly(t, "reads a column the table lacks, where a memory resource declares the columns it reads")
		typo := parse.MustResource[reservedTypo](p, "user")
		for _, q := range []url.Values{
			{filter.ParamFilter: {`{"ordr":1}`}},
			{filter.ParamSort: {"ordr"}},
		} {
			req, err := typo.ParseList(q)
			require.NoError(t, err)
			_, err = typo.List(t.Context(), req, parse.Scope{})
			assert.ErrorContains(t, err, "ordr", q.Encode())
		}
	})
}
