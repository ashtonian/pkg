//go:build integration

package parse_test

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
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

// Tenant 2's rows point at tenant 1's users, so a relation that lost its
// tenant floor would match them.
var reservedFixture = []string{
	`CREATE TABLE "user" (id BIGINT PRIMARY KEY, tenant_id BIGINT NOT NULL, "order" BIGINT NOT NULL, "group" TEXT NOT NULL, "createdAt" TEXT NOT NULL)`,
	`CREATE TABLE "check" (id BIGINT PRIMARY KEY, tenant_id BIGINT NOT NULL, "user" BIGINT NOT NULL, "limit" TEXT NOT NULL)`,
	`CREATE TABLE "table" (id BIGINT PRIMARY KEY, tenant_id BIGINT NOT NULL, "column" TEXT NOT NULL)`,
	`CREATE TABLE "grant" ("user" BIGINT NOT NULL, "table" BIGINT NOT NULL, tenant_id BIGINT NOT NULL, PRIMARY KEY ("user", "table"))`,
	`INSERT INTO "user" VALUES (1, 1, 3, 'beta', '2026-01-03'), (2, 1, 1, 'alpha', '2026-01-01'), (3, 1, 2, 'alpha', '2026-01-02'), (4, 2, 9, 'alpha', '2026-01-04')`,
	`INSERT INTO "check" VALUES (10, 1, 1, 'hard'), (11, 1, 2, 'soft'), (12, 2, 3, 'hard')`,
	`INSERT INTO "table" VALUES (20, 1, 'x'), (21, 1, 'y'), (22, 2, 'z')`,
	`INSERT INTO "grant" VALUES (1, 20, 1), (2, 20, 1), (2, 21, 1), (3, 22, 2)`,
}

func reservedUsers(t *testing.T) *parse.Resource[reservedUser] {
	t.Helper()
	users := parse.MustResource[reservedUser]("user", parse.TenantColumn("tenant_id"))
	checks := parse.MustResource[reservedCheck]("check", parse.TenantColumn("tenant_id"))
	tables := parse.MustResource[reservedTable]("table", parse.TenantColumn("tenant_id"))
	users.MustRelate("checks", parse.HasMany(checks, "user", parse.Indexed()))
	users.MustRelate("tables", parse.ManyToMany(tables,
		parse.Junction{Table: "grant", Source: []string{"user"}, Target: []string{"table"}, Tenant: "tenant_id"},
		parse.Indexed(), parse.Includable(), parse.Facetable(), parse.Writable()))
	return users
}

func TestReservedIdentifiers(t *testing.T) {
	runSQLBackends(t, runReservedIdentifiers)
}

func runReservedIdentifiers(t *testing.T, b sqlBackend) {
	db := b.open(t)
	d := b.dialect
	execStatements(t, db, reservedFixture...)
	users := reservedUsers(t)
	list := func(q url.Values) *parse.ListPlan {
		t.Helper()
		req, err := users.ParseList(q)
		require.NoError(t, err)
		plan, err := users.BuildList(d, req, parse.Scope{Tenant: "1"})
		require.NoError(t, err)
		return plan
	}
	page := func(plan *parse.ListPlan) *filter.Page[reservedUser] {
		t.Helper()
		return listPage(t, db, users, d, plan)
	}
	ids := func(p *filter.Page[reservedUser]) []int64 {
		return itemIDs(p.Items, func(u reservedUser) int64 { return u.ID })
	}

	t.Run("lists, pages and counts", func(t *testing.T) {
		q := url.Values{
			filter.ParamFilter:       {`{"group":{"$in":["alpha","beta"]},"createdAt":{"$ne":"2026-01-09"},"order":{"$gt":0}}`},
			filter.ParamSort:         {"-order"},
			filter.ParamPageSize:     {"2"},
			filter.ParamIncludeTotal: {"true"},
		}
		plan := list(q)
		first := page(plan)
		assert.Equal(t, []int64{1, 3}, ids(first))
		require.True(t, first.PageInfo.HasNextPage)
		require.NotNil(t, first.PageInfo.TotalCount)
		assert.Equal(t, int64(3), *first.PageInfo.TotalCount)

		q.Set(filter.ParamCursor, string(first.PageInfo.EndCursor))
		assert.Equal(t, []int64{2}, ids(page(list(q))))

		byCreated := url.Values{filter.ParamSort: {"createdAt"}, filter.ParamPageSize: {"1"}}
		p := page(list(byCreated))
		assert.Equal(t, []int64{2}, ids(p))
		byCreated.Set(filter.ParamCursor, string(p.PageInfo.EndCursor))
		assert.Equal(t, []int64{3}, ids(page(list(byCreated))), "the mixed-case key seeks")
	})

	t.Run("searches and facets", func(t *testing.T) {
		assert.Equal(t, []int64{2, 3}, ids(page(list(url.Values{filter.ParamSearch: {"alpha"}, filter.ParamSort: {"id"}}))))

		plan := list(url.Values{filter.ParamFacets: {"group,tables"}})
		require.NotNil(t, plan.Facets)
		counts := map[string]int64{}
		for field, buckets := range page(plan).Facets {
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
			got := ids(page(list(url.Values{filter.ParamFilter: {tc.filter}, filter.ParamSort: {"id"}})))
			assert.Equal(t, tc.want, got, tc.filter)
		}
	})

	t.Run("includes the tenant's pairs", func(t *testing.T) {
		got := page(list(url.Values{filter.ParamInclude: {"tables"}}))
		assert.Equal(t, map[string][]string{"1": {"20"}, "2": {"20", "21"}}, got.Relations["tables"])
	})

	t.Run("updates columns and memberships", func(t *testing.T) {
		p, err := users.ParseMergePatch([]byte(`{"order":7,"group":"gamma"}`))
		require.NoError(t, err)
		plan, err := users.BuildUpdate(d, p, parse.ByID(int64(1)), parse.Scope{Tenant: "1"}, parse.SkipUnchanged())
		require.NoError(t, err)
		res, err := applyUpdate(t, db, users, d, plan, false)
		require.NoError(t, err)
		require.Len(t, res.Changed, 1)
		assert.Equal(t, int64(7), res.Changed[0].Order)
		assert.Equal(t, "gamma", res.Changed[0].Group)
		res, err = applyUpdate(t, db, users, d, plan, false)
		require.NoError(t, err)
		assert.Empty(t, res.Changed, "the unchanged row is skipped")
		require.Len(t, res.Unchanged, 1)

		p, err = users.ParseMergePatch([]byte(`{"tables":[21]}`))
		require.NoError(t, err)
		plan, err = users.BuildUpdate(d, p, parse.ByID(int64(1)), parse.Scope{Tenant: "1"})
		require.NoError(t, err)
		res, err = applyUpdate(t, db, users, d, plan, false)
		require.NoError(t, err)
		require.Len(t, res.Unchanged, 1)
		assert.Equal(t, int64(2), res.RelationRows, "20 removed, 21 added")
		pairs := normalizeInts(t, queryRows[[]any](t, db, `SELECT "table" FROM "grant" WHERE "user" = 1 ORDER BY "table"`))
		assert.Equal(t, [][]any{{int64(21)}}, pairs)
	})

	t.Run("updates a batch", func(t *testing.T) {
		var rows []parse.RowUpdate
		for _, u := range []struct {
			id  int64
			raw string
		}{{2, `{"order":5}`}, {3, `{"group":"delta"}`}} {
			p, err := users.ParseMergePatch([]byte(u.raw))
			require.NoError(t, err)
			rows = append(rows, parse.RowUpdate{Key: parse.Key{u.id}, Patch: p})
		}
		plan, err := users.BuildUpdateBatch(d, rows, parse.Scope{Tenant: "1"}, parse.UpdateBatchLimits{}, parse.SkipUnchanged())
		require.NoError(t, err)
		require.Len(t, plan.Chunks, 1)
		res, err := applyUpdateBatch(t, db, users, d, plan)
		require.NoError(t, err)
		assert.Len(t, res.Changed, 2)
		assert.Empty(t, res.Unchanged)
		got := queryRows[[]any](t, db, `SELECT id, "order", "group" FROM "user" WHERE id IN (2, 3) ORDER BY id`)
		require.Len(t, got, 2)
		assert.Equal(t, []any{int64(2), int64(5), "alpha"}, []any{asInt64(t, got[0][0]), asInt64(t, got[0][1]), reservedText(t, got[0][2])})
		assert.Equal(t, []any{int64(3), int64(2), "delta"}, []any{asInt64(t, got[1][0]), asInt64(t, got[1][1]), reservedText(t, got[1][2])})
	})

	// A statement qualifies every column it reads: SQLite reads an
	// unqualified quoted name that matches no column as a string, which
	// would compare and sort a misspelt column as a constant.
	t.Run("a misspelt column fails", func(t *testing.T) {
		typo := parse.MustResource[reservedTypo]("user")
		for _, q := range []url.Values{
			{filter.ParamFilter: {`{"ordr":1}`}},
			{filter.ParamSort: {"ordr"}},
		} {
			req, err := typo.ParseList(q)
			require.NoError(t, err)
			plan, err := typo.BuildList(d, req, parse.Scope{})
			require.NoError(t, err)
			rows, err := db.Query(plan.Page.SQL, plan.Page.Args...)
			if err == nil {
				err = rows.Close()
			}
			assert.Error(t, err, plan.Page.SQL)
		}
	})
}

// reservedText is a text column's value as the driver returns it.
func reservedText(t *testing.T, v any) string {
	t.Helper()
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	case int64:
		return fmt.Sprint(s)
	}
	require.Fail(t, fmt.Sprintf("not text: %T %v", v, v))
	return ""
}
