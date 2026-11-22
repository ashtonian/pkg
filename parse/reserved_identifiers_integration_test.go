//go:build integration

package parse_test

import (
	"database/sql"
	"fmt"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
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
		parse.Junction{Table: "grant", Source: "user", Target: "table", Tenant: "tenant_id"},
		parse.Indexed(), parse.Includable(), parse.Facetable(), parse.Writable()))
	return users
}

func TestReservedIdentifiers_SQLite(t *testing.T) {
	runReservedIdentifiers(t, dbtest.SQLite(t), filter.SQLiteDialect{})
}

func TestReservedIdentifiers_Postgres(t *testing.T) {
	runReservedIdentifiers(t, dbtest.Postgres(t), filter.PostgresDialect{})
}

func runReservedIdentifiers(t *testing.T, db *sql.DB, d parse.Dialect) {
	for _, stmt := range reservedFixture {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
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
		var items []reservedUser
		for _, row := range queryRows(t, db, plan.Page.SQL, plan.Page.Args) {
			items = append(items, reservedUser{
				ID: asInt64(t, row[0]), TenantID: asInt64(t, row[1]), Order: asInt64(t, row[2]),
				Group: reservedText(t, row[3]), CreatedAt: reservedText(t, row[4]),
			})
		}
		p, err := users.Page(items, plan)
		require.NoError(t, err)
		return p
	}
	ids := func(p *filter.Page[reservedUser]) []int64 {
		out := make([]int64, len(p.Items))
		for i, u := range p.Items {
			out[i] = u.ID
		}
		return out
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
		assert.Equal(t, [][]any{{int64(3)}}, normalizeInts(t, queryRows(t, db, plan.Count.SQL, plan.Count.Args)))

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
		for _, row := range queryRows(t, db, plan.Facets.SQL, plan.Facets.Args) {
			counts[fmt.Sprint(reservedText(t, row[0]), "=", reservedText(t, row[1]))] = asInt64(t, row[2])
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
		plan := list(url.Values{filter.ParamInclude: {"tables"}})
		queries, err := users.IncludeQueries(plan, []any{int64(1), int64(2)})
		require.NoError(t, err)
		require.Len(t, queries, 1)
		var pairs []string
		for _, row := range queryRows(t, db, queries[0].SQL, queries[0].Args) {
			pairs = append(pairs, reservedText(t, row[0])+":"+reservedText(t, row[1]))
		}
		assert.Equal(t, []string{"1:20", "2:20", "2:21"}, pairs)
	})

	t.Run("updates columns and memberships", func(t *testing.T) {
		p, err := users.ParseMergePatch([]byte(`{"order":7,"group":"gamma"}`))
		require.NoError(t, err)
		plan, err := users.BuildUpdate(d, p, parse.ByID(int64(1)), parse.Scope{Tenant: "1"}, parse.SkipUnchanged())
		require.NoError(t, err)
		rows := queryRows(t, db, plan.Owner.SQL, plan.Owner.Args)
		require.Len(t, rows, 1)
		assert.Equal(t, int64(7), asInt64(t, rows[0][2]))
		assert.Equal(t, "gamma", reservedText(t, rows[0][3]))
		assert.Empty(t, queryRows(t, db, plan.Owner.SQL, plan.Owner.Args), "the unchanged row is skipped")

		p, err = users.ParseMergePatch([]byte(`{"tables":[21]}`))
		require.NoError(t, err)
		plan, err = users.BuildUpdate(d, p, parse.ByID(int64(1)), parse.Scope{Tenant: "1"})
		require.NoError(t, err)
		owners := queryRows(t, db, plan.Owner.SQL, plan.Owner.Args)
		require.Len(t, owners, 1)
		stmts, err := users.RelationStatements(plan, []any{owners[0][0]})
		require.NoError(t, err)
		for _, st := range stmts {
			_, err := db.Exec(st.SQL, st.Args...)
			require.NoError(t, err, st.SQL)
		}
		pairs := normalizeInts(t, queryRows(t, db, `SELECT "table" FROM "grant" WHERE "user" = 1 ORDER BY "table"`, nil))
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
			rows = append(rows, parse.RowUpdate{Key: u.id, Patch: p})
		}
		plan, err := users.BuildUpdateBatch(d, rows, parse.Scope{Tenant: "1"}, parse.UpdateBatchLimits{}, parse.SkipUnchanged())
		require.NoError(t, err)
		require.Len(t, plan.Chunks, 1)
		chunk := plan.Chunks[0]
		assert.Len(t, queryRows(t, db, chunk.Current.SQL, chunk.Current.Args), 2)
		require.NotNil(t, chunk.Owner)
		assert.Len(t, queryRows(t, db, chunk.Owner.SQL, chunk.Owner.Args), 2)
		got := queryRows(t, db, `SELECT id, "order", "group" FROM "user" WHERE id IN (2, 3) ORDER BY id`, nil)
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
