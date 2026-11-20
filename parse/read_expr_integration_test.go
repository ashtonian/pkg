//go:build integration

package parse_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
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
// 20, account 2 in group 20 and account 3 in group 30, so group 20 alone
// has two accounts.
var computedAccountFixture = []string{
	`CREATE TABLE tenants (id TEXT PRIMARY KEY)`,
	`INSERT INTO tenants VALUES ('t1'), ('t2')`,
	`CREATE TABLE accounts (id INTEGER PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT NOT NULL, email TEXT)`,
	`CREATE TABLE memberships (id INTEGER PRIMARY KEY, account_id INTEGER NOT NULL)`,
	`CREATE TABLE access_groups (id INTEGER PRIMARY KEY, tenant_id TEXT NOT NULL)`,
	`CREATE TABLE account_groups (account_id INTEGER NOT NULL, group_id INTEGER NOT NULL, PRIMARY KEY (account_id, group_id))`,
	`INSERT INTO accounts VALUES (1, 't1', 'a', 'a@example.com'), (2, 't1', 'b', NULL), (3, 't1', 'c', 'c@example.com'), (4, 't1', 'd', 'd@example.com'), (5, 't1', 'e', 'e@example.com'), (6, 't2', 'f', 'f@example.com')`,
	`INSERT INTO memberships VALUES (1, 1), (2, 1), (3, 2), (4, 3), (5, 3), (6, 5), (7, 6), (8, 6), (9, 6)`,
	`INSERT INTO access_groups VALUES (10, 't1'), (20, 't1'), (30, 't1')`,
	`INSERT INTO account_groups VALUES (1, 20), (1, 10), (2, 20), (3, 30)`,
}

func TestReadExpr_SQLite(t *testing.T) {
	db := dbtest.SQLite(t)
	runReadExpr(t, db, filter.SQLiteDialect{}, computedDB{
		page: func(t *testing.T, plan *parse.ListPlan) *filter.Page[computedAccount] {
			page, err := computedAccounts.Page(sqliteAccounts(t, queryRows(t, db, plan.Page.SQL, plan.Page.Args)), plan)
			require.NoError(t, err)
			return page
		},
		// The database/sql recipe: Current, Owner, the relation statements
		// and the reread, in one transaction.
		update: func(t *testing.T, plan *parse.UpdatePlan) []computedAccount {
			tx, err := db.Begin()
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			var current []computedAccount
			if plan.Current != nil {
				current = sqliteAccounts(t, queryRows(t, tx, plan.Current.SQL, plan.Current.Args))
			}
			changed := sqliteAccounts(t, queryRows(t, tx, plan.Owner.SQL, plan.Owner.Args))
			var unchanged []computedAccount
			if plan.Current != nil {
				unchanged, err = computedAccounts.Unchanged(changed, current)
				require.NoError(t, err)
			}
			keys := computedKeys(t, slices.Concat(changed, unchanged))
			stmts, err := computedAccounts.RelationStatements(plan, keys)
			require.NoError(t, err)
			for _, stmt := range stmts {
				_, err := tx.Exec(stmt.SQL, stmt.Args...)
				require.NoError(t, err, stmt.SQL)
			}
			// Read the changed and the unchanged rows again apart, to keep
			// the changed ones first.
			for i, part := range [][]any{keys[:len(changed)], keys[len(changed):]} {
				reread, err := computedAccounts.Reread(plan, part)
				require.NoError(t, err)
				if reread == nil {
					continue
				}
				fresh := sqliteAccounts(t, queryRows(t, tx, reread.SQL, reread.Args))
				if i == 0 {
					changed = fresh
				} else {
					unchanged = fresh
				}
			}
			require.NoError(t, tx.Commit())
			return slices.Concat(changed, unchanged)
		},
	})
}

// sqliteAccounts scans SQLite rows of computedAccounts.Columns, the group
// keys held as JSON text.
func sqliteAccounts(t *testing.T, rows [][]any) []computedAccount {
	t.Helper()
	var out []computedAccount
	for _, row := range rows {
		groups, ok := row[5].(string)
		require.True(t, ok, "SQLite holds the array as JSON text, got %T", row[5])
		account := computedAccount{
			ID: int(computedInt(t, row[0])), TenantID: fmt.Sprint(row[1]), Name: fmt.Sprint(row[2]), Email: fmt.Sprint(row[3]),
			MemberCount: int(computedInt(t, row[4])), Contact: fmt.Sprint(row[6]),
		}
		require.NoError(t, json.Unmarshal([]byte(groups), &account.GroupIDs))
		out = append(out, account)
	}
	return out
}

func computedKeys(t *testing.T, rows []computedAccount) []any {
	t.Helper()
	keys := make([]any, len(rows))
	for i, row := range rows {
		key, err := computedAccounts.Key(row)
		require.NoError(t, err)
		keys[i] = key
	}
	return keys
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

	runReadExpr(t, db, filter.PostgresDialect{}, computedDB{
		page: func(t *testing.T, plan *parse.ListPlan) *filter.Page[computedAccount] {
			page, err := parsepgx.QueryPage(ctx, conn, computedAccounts, plan)
			require.NoError(t, err)
			return page
		},
		update: func(t *testing.T, plan *parse.UpdatePlan) []computedAccount {
			rows, err := parsepgx.Update(ctx, conn, computedAccounts, plan)
			require.NoError(t, err)
			return rows
		},
	})
}

// computedDB runs computedAccounts' plans on one database.
type computedDB struct {
	page   func(*testing.T, *parse.ListPlan) *filter.Page[computedAccount]
	update func(*testing.T, *parse.UpdatePlan) []computedAccount
}

// runReadExpr serves the one package-level resource on d: SQLite and
// PostgreSQL read it through their own expressions.
func runReadExpr(t *testing.T, db *sql.DB, d parse.Dialect, run computedDB) {
	page := run.page
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

	t.Run("a page scans every computed column", func(t *testing.T) {
		req, err := computedAccounts.ParseList(url.Values{filter.ParamFilter: {`{"id":{"$in":[1,2]}}`}})
		require.NoError(t, err)
		plan, err := computedAccounts.BuildList(d, req, parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		items := page(t, plan).Items
		require.Len(t, items, 2)
		assert.Equal(t, computedAccount{ID: 1, TenantID: "t1", Name: "a", Email: "a@example.com", MemberCount: 2, GroupIDs: []int64{10, 20}, Contact: "a@example.com"}, items[0])
		assert.Equal(t, computedAccount{ID: 2, TenantID: "t1", Name: "b", Email: "", MemberCount: 1, GroupIDs: []int64{20}, Contact: "b"}, items[1])
	})

	t.Run("a policy compares a computed column requests cannot filter by", func(t *testing.T) {
		contact := func(value string) *filter.PolicyConstraints {
			return &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "contact", Operator: filter.OpEq, Value: value}}}
		}
		assert.Equal(t, []string{"2"}, keysOf(t, db, computedAccounts, d, `{}`, parse.Scope{Tenant: "t1", Policy: contact("b")}), "the name stands in for the NULL email")
		assert.Equal(t, []string{"t1"}, keysOf(t, db, accountTenants, d, `{"shared_accounts":{"$some":{}}}`, parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{"accounts": contact("b")}}), "the target's policy")
		assert.Equal(t, []string{"t2"}, keysOf(t, db, accountTenants, d, `{"shared_accounts":{"$some":{}}}`, parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{"accounts": contact("f@example.com")}}))
	})

	t.Run("relations filter by their targets' computed columns", func(t *testing.T) {
		t1 := parse.Scope{Tenant: "t1"}
		related := func(policy *filter.PolicyConstraint) parse.Scope {
			return parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{"accounts": {Constraints: []*filter.PolicyConstraint{policy}}}}
		}
		tests := []struct {
			name string
			ids  []string
			want []string
		}{
			{"has-many", keysOf(t, db, accountTenants, d, `{"accounts":{"$some":{"email":""}}}`, t1), []string{"t1"}},
			{"has-many, held to the tenant", keysOf(t, db, accountTenants, d, `{"accounts":{"$some":{"member_count":{"$gte":3}}}}`, t1), nil},
			{"many-to-many", keysOf(t, db, accessGroups, d, `{"accounts":{"$some":{"email":""}}}`, t1), []string{"20"}},
			{"many-to-many by count", keysOf(t, db, accessGroups, d, `{"accounts":{"$some":{"member_count":1}}}`, t1), []string{"20"}},
			{"belongs-to", keysOf(t, db, accountMemberships, d, `{"account":{"$is":{"member_count":2}}}`, t1), []string{"1", "2", "4", "5"}},
			{"has-many, then many-to-many", keysOf(t, db, accountTenants, d, `{"accounts":{"$some":{"groups":{"$some":{"size":2}}}}}`, t1), []string{"t1"}},
			{"has-many, then many-to-many, none", keysOf(t, db, accountTenants, d, `{"accounts":{"$some":{"groups":{"$some":{"size":3}}}}}`, t1), nil},
			{"belongs-to, then many-to-many", keysOf(t, db, accountMemberships, d, `{"account":{"$is":{"groups":{"$some":{"size":2}}}}}`, t1), []string{"1", "2", "3"}},
			{"the target's policy", keysOf(t, db, accountTenants, d, `{"shared_accounts":{"$some":{}}}`, related(&filter.PolicyConstraint{Field: "member_count", Operator: filter.OpGte, Value: 3})), []string{"t2"}},
			{"the target's policy, both", keysOf(t, db, accountTenants, d, `{"shared_accounts":{"$some":{}}}`, related(&filter.PolicyConstraint{Field: "member_count", Operator: filter.OpGte, Value: 2})), []string{"t1", "t2"}},
		}
		for _, tt := range tests {
			assert.Equal(t, tt.want, tt.ids, tt.name)
		}
	})

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

	t.Run("an update reads computed columns after its relation changes", func(t *testing.T) {
		update := func(t *testing.T, body string, target parse.Target, opts ...parse.UpdateOption) []computedAccount {
			t.Helper()
			p, err := computedAccounts.ParseMergePatch([]byte(body))
			require.NoError(t, err)
			plan, err := computedAccounts.BuildUpdate(d, p, target, parse.Scope{Tenant: "t1"}, opts...)
			require.NoError(t, err)
			return run.update(t, plan)
		}
		rows := update(t, `{"groups":[30]}`, parse.ByID(1))
		require.Len(t, rows, 1)
		assert.Equal(t, []int64{30}, rows[0].GroupIDs, "the pairs the patch left, not the ones Owner read")

		rows = update(t, `{"name":"a","groups":[20]}`, parse.ByIDs(1, 4), parse.SkipUnchanged())
		require.Len(t, rows, 2)
		assert.Equal(t, []int{4, 1}, []int{rows[0].ID, rows[1].ID}, "the changed row, then the unchanged one")
		for _, row := range rows {
			assert.Equal(t, []int64{20}, row.GroupIDs, "account %d", row.ID)
		}
	})
}

// keysOf lists r's rows for filterJSON on d and returns their keys.
func keysOf[T any](t *testing.T, db *sql.DB, r *parse.Resource[T], d filter.Dialect, filterJSON string, scope parse.Scope) []string {
	t.Helper()
	req, err := r.ParseList(url.Values{filter.ParamFilter: {filterJSON}})
	require.NoError(t, err)
	plan, err := r.BuildList(d, req, scope)
	require.NoError(t, err)
	key := slices.Index(r.ColumnNames(), r.PrimaryKeyColumn())
	var out []string
	for _, row := range queryRows(t, db, plan.Page.SQL, plan.Page.Args) {
		out = append(out, fmt.Sprint(row[key]))
	}
	return out
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
