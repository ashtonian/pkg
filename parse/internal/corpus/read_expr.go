//go:build integration

package corpus

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

func readExpr(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		if b.engine == Memory {
			requireComputedRefused(t, b)
			return
		}
		runReadExpr(t, b)
	})
}

// requireComputedRefused holds the Memory engine to its refusal of
// computed columns: it evaluates no SQL expression, so each resource that
// reads one (ReadExpr) is refused as it is built, with the typed
// capability error.
func requireComputedRefused(t *testing.T, b sqlBackend) {
	t.Helper()
	p, _ := b.open(t)
	_, groups := parse.NewResource[parsetest.AccessGroup](p, "access_groups", parse.TenantColumn("tenant_id"), parse.ReadExpr("size", parse.SQL(parsetest.GroupSizeExpr)))
	_, accounts := parse.NewResource[parsetest.ComputedAccount](p, "accounts", parse.TenantColumn("tenant_id"), parse.ReadExpr("member_count", parse.SQL(parsetest.MemberCountExpr)))
	for _, err := range []error{groups, accounts} {
		require.ErrorIs(t, err, parse.ErrUnsupported)
		unsupported, ok := errors.AsType[*parse.UnsupportedError](err)
		require.True(t, ok, "%v", err)
		assert.Equal(t, "computed columns", unsupported.Capability)
	}
}

// runReadExpr serves the computed resources on b: SQLite and PostgreSQL
// read them through their own expressions.
func runReadExpr(t *testing.T, b sqlBackend) {
	provider, db := b.open(t)
	accessGroups, computedAccounts, accountTenants, accountMemberships := parsetest.NewComputedResources(provider)
	page := func(t *testing.T, req *parse.ListRequest) *filter.Page[parsetest.ComputedAccount] {
		return listPage(t, computedAccounts, req, parse.Scope{Tenant: "t1"})
	}
	dbtest.Exec(t, db, parsetest.ComputedAccountFixture()...)
	list := func(t *testing.T, q url.Values) []int {
		t.Helper()
		req, err := computedAccounts.ParseList(q)
		require.NoError(t, err)
		return parsetest.ItemIDs(page(t, req).Items, computedAccountID)
	}

	t.Run("a page scans every computed column", func(t *testing.T) {
		req, err := computedAccounts.ParseList(url.Values{filter.ParamFilter: {`{"id":{"$in":[1,2]}}`}})
		require.NoError(t, err)
		items := page(t, req).Items
		require.Len(t, items, 2)
		assert.Equal(t, parsetest.ComputedAccount{ID: 1, TenantID: "t1", Name: "a", Email: "a@example.com", MemberCount: 2, GroupIDs: []int64{10, 20}, Contact: "a@example.com"}, items[0])
		assert.Equal(t, parsetest.ComputedAccount{ID: 2, TenantID: "t1", Name: "b", Email: "", MemberCount: 1, GroupIDs: []int64{20}, Contact: "b"}, items[1])
	})

	t.Run("a policy compares a computed column requests cannot filter by", func(t *testing.T) {
		contact := func(value string) *filter.PolicyConstraints {
			return &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "contact", Operator: filter.OpEq, Value: value}}}
		}
		assert.Equal(t, []string{"2"}, keysOf(t, db, computedAccounts, `{}`, parse.Scope{Tenant: "t1", Policy: contact("b")}), "the name stands in for the NULL email")
		assert.Equal(t, []string{"t1"}, keysOf(t, db, accountTenants, `{"shared_accounts":{"$some":{}}}`, parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{"accounts": contact("b")}}), "the target's policy")
		assert.Equal(t, []string{"t2"}, keysOf(t, db, accountTenants, `{"shared_accounts":{"$some":{}}}`, parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{"accounts": contact("f@example.com")}}))
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
			{"has-many", keysOf(t, db, accountTenants, `{"accounts":{"$some":{"email":""}}}`, t1), []string{"t1"}},
			{"has-many, held to the tenant", keysOf(t, db, accountTenants, `{"accounts":{"$some":{"member_count":{"$gte":3}}}}`, t1), nil},
			{"many-to-many", keysOf(t, db, accessGroups, `{"accounts":{"$some":{"email":""}}}`, t1), []string{"20"}},
			{"many-to-many by count", keysOf(t, db, accessGroups, `{"accounts":{"$some":{"member_count":1}}}`, t1), []string{"20"}},
			{"belongs-to", keysOf(t, db, accountMemberships, `{"account":{"$is":{"member_count":2}}}`, t1), []string{"1", "2", "4", "5"}},
			{"has-many, then many-to-many", keysOf(t, db, accountTenants, `{"accounts":{"$some":{"groups":{"$some":{"size":2}}}}}`, t1), []string{"t1"}},
			{"has-many, then many-to-many, none", keysOf(t, db, accountTenants, `{"accounts":{"$some":{"groups":{"$some":{"size":3}}}}}`, t1), nil},
			{"belongs-to, then many-to-many", keysOf(t, db, accountMemberships, `{"account":{"$is":{"groups":{"$some":{"size":2}}}}}`, t1), []string{"1", "2", "3"}},
			{"the target's policy", keysOf(t, db, accountTenants, `{"shared_accounts":{"$some":{}}}`, related(&filter.PolicyConstraint{Field: "member_count", Operator: filter.OpGte, Value: 3})), []string{"t2"}},
			{"the target's policy, both", keysOf(t, db, accountTenants, `{"shared_accounts":{"$some":{}}}`, related(&filter.PolicyConstraint{Field: "member_count", Operator: filter.OpGte, Value: 2})), []string{"t1", "t2"}},
		}
		for _, tt := range tests {
			assert.Equal(t, tt.want, tt.ids, tt.name)
		}
	})

	t.Run("a keyset walk sorted by a computed column visits every row once", func(t *testing.T) {
		all, err := computedAccounts.ParseList(url.Values{filter.ParamSort: {"-member_count"}, filter.ParamPageSize: {"10"}})
		require.NoError(t, err)
		var want, counts []int
		for _, item := range page(t, all).Items {
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
			p := page(t, req)
			got = append(got, parsetest.ItemIDs(p.Items, computedAccountID)...)
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
		listed := page(t, req)
		assert.Equal(t, []int{1, 3}, parsetest.ItemIDs(listed.Items, computedAccountID), "t2's account 6 has more, but is not t1's")
		require.NotNil(t, listed.PageInfo.TotalCount)
		assert.Equal(t, int64(2), *listed.PageInfo.TotalCount)

		assert.Equal(t, []int{2}, list(t, url.Values{filter.ParamFilter: {`{"email":""}`}}), "the NULL email compares as the dialect's expression reads it")
	})

	t.Run("search and sorts read the dialect's expression", func(t *testing.T) {
		assert.Equal(t, []int{5}, list(t, url.Values{filter.ParamSearch: {"e@example.com"}}))
		assert.Equal(t, []int{2, 1, 3, 4, 5}, list(t, url.Values{filter.ParamSort: {"email"}, filter.ParamPageSize: {"10"}}), "the NULL email sorts as the empty string")
	})

	t.Run("an update returns the computed columns", func(t *testing.T) {
		p, err := computedAccounts.ParseMergePatch([]byte(`{"name":"renamed"}`))
		require.NoError(t, err)
		plan, err := computedAccounts.BuildUpdate(p, parse.ByID(3), parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		rows := dbtest.QueryRows[[]any](t, db, plan.Owner.SQL, plan.Owner.Args...)
		require.Len(t, rows, 1)
		assert.Equal(t, "renamed", fmt.Sprint(rows[0][2]))
		assert.Equal(t, int64(2), computedInt(t, rows[0][4]), "member_count")
	})

	t.Run("an update reads computed columns after its relation changes", func(t *testing.T) {
		update := func(t *testing.T, body string, target parse.Target, opts ...parse.UpdateOption) parse.UpdateResult[parsetest.ComputedAccount] {
			t.Helper()
			p, err := computedAccounts.ParseMergePatch([]byte(body))
			require.NoError(t, err)
			res, err := applyUpdate(t, computedAccounts, false, p, target, parse.Scope{Tenant: "t1"}, opts...)
			require.NoError(t, err)
			return res
		}
		res := update(t, `{"groups":[30]}`, parse.ByID(1))
		assert.Empty(t, res.Changed, "a relation-only patch writes no column")
		require.Len(t, res.Unchanged, 1)
		assert.Equal(t, []int64{30}, res.Unchanged[0].GroupIDs, "the pairs the patch left, not the ones Owner read")
		assert.Equal(t, int64(3), res.RelationRows, "10 and 20 removed, 30 added")

		res = update(t, `{"name":"a","groups":[20]}`, parse.ByIDs(1, 4), parse.SkipUnchanged())
		require.Len(t, res.Changed, 1)
		require.Len(t, res.Unchanged, 1)
		assert.Equal(t, 4, res.Changed[0].ID, "d is renamed")
		assert.Equal(t, 1, res.Unchanged[0].ID, "a is already a")
		for _, row := range slices.Concat(res.Changed, res.Unchanged) {
			assert.Equal(t, []int64{20}, row.GroupIDs, "account %d", row.ID)
		}
	})
}

func computedAccountID(a parsetest.ComputedAccount) int { return a.ID }

// keysOf runs the page r builds for filterJSON on db and returns its rows'
// keys.
func keysOf[T any](t *testing.T, db *sql.DB, r *parse.Resource[T], filterJSON string, scope parse.Scope) []string {
	t.Helper()
	req, err := r.ParseList(url.Values{filter.ParamFilter: {filterJSON}})
	require.NoError(t, err)
	plan, err := r.BuildList(req, scope)
	require.NoError(t, err)
	key := slices.Index(r.ColumnNames(), r.PrimaryKeyColumns()[0])
	var out []string
	for _, row := range dbtest.QueryRows[[]any](t, db, plan.Page.SQL, plan.Page.Args...) {
		out = append(out, fmt.Sprint(row[key]))
	}
	return out
}

// The relation's includes and facets compile its target's policy for the
// owner's dialect too. They read the target under an alias, so a policy on
// a computed column fails the build instead of reading a stored column the
// table lacks.
func readExprRelationIncludesCompileTheTargetsPolicy(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		if b.engine == Memory {
			requireComputedRefused(t, b)
			return
		}
		scope := parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{
			"accounts": {Constraints: []*filter.PolicyConstraint{{Field: "member_count", Operator: filter.OpGte, Value: 1}}},
		}}
		provider, db := b.open(t)
		dbtest.Exec(t, db, parsetest.ComputedAccountFixture()...)
		_, accounts, tenants, _ := parsetest.NewComputedResources(provider)
		req, err := tenants.ParseList(url.Values{filter.ParamInclude: {"shared_accounts"}})
		require.NoError(t, err)
		plan, err := tenants.BuildList(req, scope)
		require.NoError(t, err)
		_, err = tenants.IncludeQueries(plan, []parse.Key{{"t1"}})
		require.ErrorContains(t, err, "field member_count is not a column of accounts and cannot be filtered through a relation")

		// A membership change holds its pairs to the targets the same policy
		// admits, once the owner statement has returned the group, and so
		// writes nothing.
		groups := parsetest.NewAccessGroups(provider)
		groups.MustRelate("accounts", parse.ManyToMany(accounts, parse.Through("account_groups", "group_id", "account_id"), parse.Writable(), parse.PolicyScoped()))
		p, err := groups.ParseMergePatch([]byte(`{"accounts":[1]}`))
		require.NoError(t, err)
		_, err = groups.Update(t.Context(), p, parse.ByID(20), scope)
		require.ErrorContains(t, err, "field member_count is not a column of accounts and cannot be filtered through a relation")
		assert.Equal(t, []string{"1:10", "1:20", "2:20", "3:30"}, dbtest.QueryRows[string](t, db, `SELECT account_id || ':' || group_id FROM account_groups ORDER BY 1`))
	})
}

// An update reads its owners again only when its relation statements may
// have changed what a computed column reads: after them, in its
// transaction, held to the update's tenant. The update runs in the
// application's transaction, whose statements every engine reports.
func readExprUpdateRereadsAfterRelationChanges(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		if b.engine == Memory {
			requireComputedRefused(t, b)
			return
		}
		provider, db := b.open(t)
		dbtest.Exec(t, db, parsetest.ComputedAccountFixture()...)
		_, accounts, _, _ := parsetest.NewComputedResources(provider)
		scope := parse.Scope{Tenant: "t1"}
		var statements []string
		record := func(_ context.Context, query string) {
			if !strings.Contains(query, "SAVEPOINT") {
				statements = append(statements, query)
			}
		}
		reread := "SELECT " + strings.Join(accounts.Columns(), ", ") + ` FROM "accounts" WHERE `
		update := func(t *testing.T, body string) parse.UpdateResult[parsetest.ComputedAccount] {
			t.Helper()
			statements = nil
			var res parse.UpdateResult[parsetest.ComputedAccount]
			callerTx(t, b, db, func(tx *parse.Tx, _ inTx) {
				scoped, err := accounts.In(tx)
				require.NoError(t, err)
				p, err := accounts.ParseMergePatch([]byte(body))
				require.NoError(t, err)
				res, err = scoped.Update(dbtest.OnStatement(t.Context(), record), p, parse.ByID(1), scope)
				require.NoError(t, err)
			})
			require.NotEmpty(t, statements)
			return res
		}

		update(t, `{"name":"x"}`)
		for _, query := range statements {
			assert.False(t, strings.HasPrefix(query, reread), "no relation statements: Owner's rows are current: %s", query)
		}

		res := update(t, `{"groups":[30]}`)
		last := statements[len(statements)-1]
		assert.True(t, strings.HasPrefix(last, reread), "the reread follows the relation statements: %s", last)
		assert.Contains(t, last, `"accounts"."tenant_id" = `)
		require.Len(t, res.Unchanged, 1)
		assert.Equal(t, []int64{30}, res.Unchanged[0].GroupIDs, "the pairs the relation statements left")
	}, hooked())
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
