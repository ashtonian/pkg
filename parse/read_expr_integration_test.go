//go:build integration

package parse_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsepgx"
)

// computedAccountFixture: accounts 1–5 of t1 hold 2, 1, 2, 0 and 1
// memberships (ties on the count), account 2 has no email, and t2's
// account 6 has the most memberships of all. Account 1 is in groups 10 and
// 20, account 3 in group 30.
var computedAccountFixture = []string{
	`CREATE TABLE accounts (id INTEGER PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT NOT NULL, email TEXT)`,
	`CREATE TABLE memberships (id INTEGER PRIMARY KEY, account_id INTEGER NOT NULL)`,
	`CREATE TABLE access_groups (id INTEGER PRIMARY KEY, tenant_id TEXT NOT NULL)`,
	`CREATE TABLE account_groups (account_id INTEGER NOT NULL, group_id INTEGER NOT NULL, PRIMARY KEY (account_id, group_id))`,
	`INSERT INTO accounts VALUES (1, 't1', 'a', 'a@example.com'), (2, 't1', 'b', NULL), (3, 't1', 'c', 'c@example.com'), (4, 't1', 'd', 'd@example.com'), (5, 't1', 'e', 'e@example.com'), (6, 't2', 'f', 'f@example.com')`,
	`INSERT INTO memberships VALUES (1, 1), (2, 1), (3, 2), (4, 3), (5, 3), (6, 5), (7, 6), (8, 6), (9, 6)`,
	`INSERT INTO access_groups VALUES (10, 't1'), (20, 't1'), (30, 't1')`,
	`INSERT INTO account_groups VALUES (1, 20), (1, 10), (3, 30)`,
}

func TestReadExpr_SQLite(t *testing.T) {
	db := dbtest.SQLite(t)
	runReadExpr(t, db, filter.SQLiteDialect{}, func(t *testing.T, plan *parse.ListPlan) *filter.Page[computedAccount] {
		var items []computedAccount
		for _, row := range queryRows(t, db, plan.Page.SQL, plan.Page.Args) {
			items = append(items, computedAccount{
				ID: int(computedInt(t, row[0])), TenantID: fmt.Sprint(row[1]), Name: fmt.Sprint(row[2]), Email: fmt.Sprint(row[3]),
				MemberCount: int(computedInt(t, row[4])),
			})
			assert.IsType(t, "", row[5], "SQLite holds the array as JSON text")
		}
		page, err := computedAccounts.Page(items, plan)
		require.NoError(t, err)
		return page
	})
}

func TestReadExpr_Postgres(t *testing.T) {
	dsn := dbtest.PostgresDSN(t)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	runReadExpr(t, db, filter.PostgresDialect{}, func(t *testing.T, plan *parse.ListPlan) *filter.Page[computedAccount] {
		page, err := parsepgx.QueryPage(ctx, conn, computedAccounts, plan)
		require.NoError(t, err)
		return page
	})

	t.Run("parsepgx scans every computed column", func(t *testing.T) {
		req, err := computedAccounts.ParseList(url.Values{filter.ParamFilter: {`{"id":{"$in":[1,2]}}`}})
		require.NoError(t, err)
		plan, err := computedAccounts.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		page, err := parsepgx.QueryPage(ctx, conn, computedAccounts, plan)
		require.NoError(t, err)
		require.Len(t, page.Items, 2)
		assert.Equal(t, computedAccount{ID: 1, TenantID: "t1", Name: "a", Email: "a@example.com", MemberCount: 2, GroupIDs: []int64{10, 20}}, page.Items[0])
		assert.Equal(t, computedAccount{ID: 2, TenantID: "t1", Name: "b", Email: "", MemberCount: 1, GroupIDs: []int64{}}, page.Items[1])
	})
}

// runReadExpr serves the one package-level resource on d: SQLite and
// PostgreSQL read it through their own expressions.
func runReadExpr(t *testing.T, db *sql.DB, d parse.Dialect, page func(*testing.T, *parse.ListPlan) *filter.Page[computedAccount]) {
	for _, stmt := range computedAccountFixture {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	list := func(t *testing.T, q url.Values) []int {
		t.Helper()
		req, err := computedAccounts.ParseList(q)
		require.NoError(t, err)
		plan, err := computedAccounts.BuildList(d, req, parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		var ids []int
		for _, item := range page(t, plan).Items {
			ids = append(ids, item.ID)
		}
		return ids
	}

	t.Run("a keyset walk sorted by a computed column visits every row once", func(t *testing.T) {
		all, err := computedAccounts.ParseList(url.Values{filter.ParamSort: {"-member_count"}, filter.ParamPageSize: {"10"}})
		require.NoError(t, err)
		allPlan, err := computedAccounts.BuildList(d, all, parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		var want, counts []int
		for _, item := range page(t, allPlan).Items {
			want = append(want, item.ID)
			counts = append(counts, item.MemberCount)
		}
		require.Equal(t, []int{2, 2, 1, 1, 0}, counts, "one page holds t1's accounts by count, ties and all")

		var got []int
		cursor := ""
		for range 10 {
			q := url.Values{filter.ParamSort: {"-member_count"}}
			if cursor != "" {
				q.Set(filter.ParamCursor, cursor)
			}
			req, err := computedAccounts.ParseList(q)
			require.NoError(t, err)
			plan, err := computedAccounts.BuildList(d, req, parse.Scope{Tenant: "t1"})
			require.NoError(t, err)
			p := page(t, plan)
			for _, item := range p.Items {
				got = append(got, item.ID)
			}
			if !p.PageInfo.HasNextPage {
				break
			}
			cursor = string(p.PageInfo.EndCursor)
		}
		assert.Equal(t, want, got, "two rows a page, in the one page's order")
	})

	t.Run("filters and counts compare the computed value", func(t *testing.T) {
		req, err := computedAccounts.ParseList(url.Values{
			filter.ParamFilter:       {`{"member_count":{"$gte":2}}`},
			filter.ParamIncludeTotal: {"true"},
			filter.ParamPageSize:     {"10"},
		})
		require.NoError(t, err)
		plan, err := computedAccounts.BuildList(d, req, parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		var ids []int
		for _, item := range page(t, plan).Items {
			ids = append(ids, item.ID)
		}
		assert.Equal(t, []int{1, 3}, ids, "t2's account 6 has more, but is not t1's")
		assert.Equal(t, "2", fmt.Sprint(queryRows(t, db, plan.Count.SQL, plan.Count.Args)[0][0]))

		assert.Equal(t, []int{2}, list(t, url.Values{filter.ParamFilter: {`{"email":""}`}}), "the NULL email compares as the dialect's expression reads it")
	})

	t.Run("search and sorts read the dialect's expression", func(t *testing.T) {
		assert.Equal(t, []int{5}, list(t, url.Values{filter.ParamSearch: {"e@example.com"}}))
		assert.Equal(t, []int{2, 1, 3, 4, 5}, list(t, url.Values{filter.ParamSort: {"email"}, filter.ParamPageSize: {"10"}}), "the NULL email sorts as the empty string")
	})

	t.Run("an update returns the computed columns", func(t *testing.T) {
		p, err := computedAccounts.ParseMergePatch([]byte(`{"name":"renamed"}`))
		require.NoError(t, err)
		plan, err := computedAccounts.BuildUpdate(d, p, parse.ByID(3), parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		rows := queryRows(t, db, plan.Owner.SQL, plan.Owner.Args)
		require.Len(t, rows, 1)
		assert.Equal(t, "renamed", fmt.Sprint(rows[0][2]))
		assert.Equal(t, int64(2), computedInt(t, rows[0][4]), "member_count")
	})
}

func computedInt(t *testing.T, v any) int64 {
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
