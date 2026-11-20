//go:build unit

package parse_test

import (
	"errors"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

var computedDialects = []struct {
	dialect         parse.Dialect
	email, groupIDs string
}{
	{filter.PostgresDialect{}, pgEmailExpr, pgGroupIDsExpr},
	{filter.SQLiteDialect{}, sqliteEmailExpr, sqliteGroupIDsExpr},
}

func TestReadExpr_Columns(t *testing.T) {
	for _, tt := range computedDialects {
		t.Run(tt.dialect.Name(), func(t *testing.T) {
			columns, err := computedAccounts.Columns(tt.dialect)
			require.NoError(t, err)
			assert.Equal(t, []string{
				"accounts.id",
				"accounts.tenant_id",
				"accounts.name",
				"(" + tt.email + ") AS email",
				"(" + memberCountExpr + ") AS member_count",
				"(" + tt.groupIDs + ") AS group_ids",
			}, columns)
		})
	}
	assert.Equal(t, []string{"id", "tenant_id", "name", "email", "member_count", "group_ids"}, computedAccounts.ColumnNames())

	type window struct {
		ID   int           `json:"id"   db:"id"`
		Idle core.Interval `json:"idle" db:"idle"`
	}
	windows, err := parse.NewResource[window]("windows", parse.ReadExpr("idle", parse.SQL("windows.ends - windows.starts")))
	require.NoError(t, err)
	cols, err := windows.Columns(filter.PostgresDialect{})
	require.NoError(t, err)
	assert.Contains(t, cols[1], "(windows.ends - windows.starts)", "a declared read cast applies to the expression")
	assert.Contains(t, cols[1], " AS idle")
}

// One resource compiles each dialect's statements against that dialect's
// expressions; its schemas and their document name the column.
func TestReadExpr_SchemasCompileAgainstEachDialectsExpression(t *testing.T) {
	req, err := computedAccounts.ParseList(url.Values{
		filter.ParamFilter:       {`{"member_count":{"$gte":1},"email":{"$ne":""}}`},
		filter.ParamSort:         {"-member_count,email"},
		filter.ParamFacets:       {"member_count"},
		filter.ParamSearch:       {"example"},
		filter.ParamIncludeTotal: {"true"},
	})
	require.NoError(t, err)
	for _, tt := range computedDialects {
		t.Run(tt.dialect.Name(), func(t *testing.T) {
			plan, err := computedAccounts.BuildList(tt.dialect, req, parse.Scope{Tenant: "t1"})
			require.NoError(t, err)
			memberCount, email := "("+memberCountExpr+")", "("+tt.email+")"
			assert.Contains(t, plan.Page.SQL, memberCount+" >= ")
			assert.Contains(t, plan.Page.SQL, email+" <> ")
			assert.Contains(t, plan.Page.SQL, "ORDER BY "+memberCount+" DESC")
			assert.Contains(t, plan.Page.SQL, tt.dialect.CodePointOrder(email))
			assert.Contains(t, plan.Count.SQL, memberCount+" >= ")
			assert.Contains(t, plan.Count.SQL, email, "search")
			assert.Contains(t, plan.Facets.SQL, memberCount)
			for _, other := range computedDialects {
				if other.dialect.Name() != tt.dialect.Name() {
					assert.NotContains(t, plan.Page.SQL, other.email)
				}
			}

			p, err := computedAccounts.ParseFilter(`{"email":"a@example.com"}`)
			require.NoError(t, err)
			update, err := computedAccounts.BuildUpdate(tt.dialect, computedPatch(t, computedAccounts, `{"name":"x"}`), parse.ByFilter(p), parse.Scope{Tenant: "t1"})
			require.NoError(t, err)
			assert.Contains(t, update.Owner.SQL, "WHERE ("+email+" = ")
		})
	}

	assert.Equal(t, "accounts.member_count", computedAccounts.FilterSchema().Fields["member_count"].SQLExpr, "the shared schema stays dialect-neutral")
	assert.Equal(t, "accounts.email", computedAccounts.SortSchema().Fields["email"].SQLExpr)
}

// Views are keyed by the expressions, not the dialect: two PostgreSQL
// dialects with one expression share a view, and each build still compiles
// under its own dialect's settings.
func TestReadExpr_DialectSettingsApplyPerBuild(t *testing.T) {
	req, err := computedAccounts.ParseList(url.Values{filter.ParamSort: {"email"}})
	require.NoError(t, err)
	email := "(" + pgEmailExpr + ")"
	for _, d := range []filter.PostgresDialect{{}, {Collation: filter.CollationC}, {}} {
		plan, err := computedAccounts.BuildList(d, req, parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		assert.Contains(t, plan.Page.SQL, "ORDER BY "+d.CodePointOrder(email)+" ASC")
	}
}

// A dialect an expression refuses fails every statement the resource
// builds for it, naming the table, the column and the dialect; the
// resource serves its other dialects.
func TestReadExpr_RefusedDialect(t *testing.T) {
	postgresOnly := func(d filter.Dialect) (string, error) {
		if _, ok := d.(filter.PostgresDialect); !ok {
			return "", filter.ErrUnsupportedDialect
		}
		return pgGroupIDsExpr, nil
	}
	accounts := newComputedAccounts(postgresOnly)
	req, err := accounts.ParseList(url.Values{
		filter.ParamFilter:       {`{"member_count":{"$gte":1}}`},
		filter.ParamFacets:       {"member_count"},
		filter.ParamInclude:      {"groups"},
		filter.ParamIncludeTotal: {"true"},
	})
	require.NoError(t, err)
	scope := parse.Scope{Tenant: "t1"}
	rename, groups := computedPatch(t, accounts, `{"name":"x"}`), computedPatch(t, accounts, `{"groups":[30]}`)
	entryPoints := []struct {
		name  string
		build func(parse.Dialect) error
	}{
		{"Columns", func(d parse.Dialect) error { _, err := accounts.Columns(d); return err }},
		{"BuildList", func(d parse.Dialect) error { _, err := accounts.BuildList(d, req, scope); return err }},
		{"BuildList of a first page", func(d parse.Dialect) error { _, err := accounts.BuildList(d, nil, scope); return err }},
		{"BuildUpdate", func(d parse.Dialect) error {
			_, err := accounts.BuildUpdate(d, rename, parse.ByID(1), scope)
			return err
		}},
		{"BuildUpdate of relations", func(d parse.Dialect) error {
			_, err := accounts.BuildUpdate(d, groups, parse.ByID(1), scope, parse.SkipUnchanged())
			return err
		}},
		{"BuildUpdateBatch", func(d parse.Dialect) error {
			_, err := accounts.BuildUpdateBatch(d, []parse.RowUpdate{{Key: 1, Patch: rename}}, scope, parse.UpdateBatchLimits{})
			return err
		}},
	}
	for _, tt := range entryPoints {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.build(filter.SQLiteDialect{})
			assert.ErrorIs(t, err, filter.ErrUnsupportedDialect)
			assert.ErrorContains(t, err, `parse: accounts: computed column "group_ids" for dialect sqlite`)
			assert.NoError(t, tt.build(filter.PostgresDialect{}))
		})
	}

	blank, err := parse.NewResource[computedAccount]("accounts", computedAccountOptions(parse.SQL(" "))...)
	require.NoError(t, err)
	_, err = blank.BuildList(filter.SQLiteDialect{}, nil, scope)
	assert.ErrorContains(t, err, `parse: accounts: computed column "group_ids" for dialect sqlite: no expression`)
}

// Concurrent first builds for two dialects each build and read their own
// dialect's view.
func TestReadExpr_ConcurrentFirstUse(t *testing.T) {
	accounts := newComputedAccounts(groupIDsExpr)
	req, err := accounts.ParseList(url.Values{
		filter.ParamFilter: {`{"email":{"$ne":""}}`},
		filter.ParamSort:   {"-member_count"},
		filter.ParamFacets: {"member_count"},
	})
	require.NoError(t, err)
	plans := make([]*parse.ListPlan, 32)
	errs := make([]error, len(plans))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range plans {
		wg.Go(func() {
			<-start
			plans[i], errs[i] = accounts.BuildList(computedDialects[i%2].dialect, req, parse.Scope{Tenant: "t1"})
		})
	}
	close(start)
	wg.Wait()
	for i, plan := range plans {
		require.NoError(t, errs[i])
		tt := computedDialects[i%2]
		assert.Contains(t, plan.Page.SQL, "("+tt.email+") <> ")
		assert.Contains(t, plan.Page.SQL, "("+tt.groupIDs+") AS group_ids")
	}
}

func TestReadExpr_Refusals(t *testing.T) {
	tests := []struct {
		name string
		opt  parse.ResourceOption
		want string
	}{
		{"not a column", parse.ReadExpr("nope", parse.SQL("1")), `computed column "nope": is not a column`},
		{"declared twice", parse.ReadExpr("email", parse.SQL("accounts.email")), `computed column "email": is declared twice`},
		{"no expression", parse.ReadExpr("name", nil), `computed column "name": has no expression`},
		{"the key", parse.ReadExpr("id", parse.SQL("1")), `computed column "id": is the key, tenant or deleted column`},
		{"the tenant", parse.ReadExpr("tenant_id", parse.SQL("'t1'")), `computed column "tenant_id": is the key, tenant or deleted column`},
		{"not read", parse.ReadColumns("id", "tenant_id", "name", "email", "group_ids"), `computed column "member_count": is not read`},
		{"patched", parse.ReadExpr("name", parse.SQL("upper(accounts.name)")), `computed column "name": is patched at /name`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse.NewResource[computedAccount]("accounts", computedAccountOptions(groupIDsExpr, tt.opt)...)
			assert.ErrorContains(t, err, tt.want)
		})
	}

	type document struct {
		ID    int            `json:"id"    db:"id"`
		Attrs map[string]any `json:"attrs" db:"attrs" parse:"filter" paths:"color:string"`
	}
	_, err := parse.NewResource[document]("documents", parse.ReadExpr("attrs", parse.SQL("jsonb_build_object('color', 'red')")))
	assert.ErrorContains(t, err, "declares document paths")

	type membership struct {
		ID        int `json:"id"         db:"id"         parse:"filter"`
		AccountID int `json:"account_id" db:"account_id" parse:"filter"`
	}
	memberships := parse.MustResource[membership]("memberships", parse.ReadExpr("account_id", parse.SQL("1")))
	accounts := newComputedAccounts(groupIDsExpr)
	err = accounts.Relate("memberships", parse.HasMany(memberships, "account_id"))
	assert.ErrorContains(t, err, `correlates on memberships's computed column "account_id"`)
}

// A relation's filter compiles for its owner's dialect, which resolves only
// the owner's expressions: the target's computed columns are no fields of
// it.
func TestReadExpr_RelationFiltersOmitTheTargetsComputedColumns(t *testing.T) {
	type accountTenant struct {
		ID string `json:"id" db:"id" parse:"filter"`
	}
	tenants := parse.MustResource[accountTenant]("tenants")
	tenants.MustRelate("accounts", parse.HasMany(computedAccounts, "tenant_id", parse.Indexed()))

	_, err := tenants.ParseList(url.Values{filter.ParamFilter: {`{"accounts":{"$some":{"name":"a"}}}`}})
	require.NoError(t, err)
	for _, field := range []string{"member_count", "email"} {
		_, err := tenants.ParseList(url.Values{filter.ParamFilter: {`{"accounts":{"$some":{"` + field + `":"1"}}}`}})
		var pe *core.ParseError
		require.True(t, errors.As(err, &pe), "%s: %v", field, err)
		assert.Equal(t, filter.ECodeUnknownField.String(), pe.Code, field)
	}
	assert.Contains(t, computedAccounts.FilterSchema().Fields, "member_count", "the resource's own lists filter by it")
}

func computedPatch(t *testing.T, accounts *parse.Resource[computedAccount], body string) *patch.ParsedPatch {
	t.Helper()
	p, err := accounts.ParseMergePatch([]byte(body))
	require.NoError(t, err)
	return p
}
