//go:build unit

package parse_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

func TestReadExpr_Columns(t *testing.T) {
	accounts, err := computedAccounts(filter.PostgresDialect{})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"accounts.id",
		"accounts.tenant_id",
		"accounts.name",
		"(" + emailExpr + ") AS email",
		"(" + memberCountExpr + ") AS member_count",
		"(" + groupIDsExpr(filter.PostgresDialect{}) + ") AS group_ids",
	}, accounts.Columns(filter.PostgresDialect{}))
	assert.Equal(t, []string{"id", "tenant_id", "name", "email", "member_count", "group_ids"}, accounts.ColumnNames())

	type window struct {
		ID   int           `json:"id"   db:"id"`
		Idle core.Interval `json:"idle" db:"idle"`
	}
	windows, err := parse.NewResource[window]("windows", parse.ReadExpr("idle", "windows.ends - windows.starts"))
	require.NoError(t, err)
	cols := windows.Columns(filter.PostgresDialect{})
	assert.Contains(t, cols[1], "(windows.ends - windows.starts)", "a declared read cast applies to the expression")
	assert.Contains(t, cols[1], " AS idle")
}

func TestReadExpr_FiltersSortsAndFacetsCompileAgainstTheExpression(t *testing.T) {
	accounts, err := computedAccounts(filter.PostgresDialect{})
	require.NoError(t, err)
	req, err := accounts.ParseList(url.Values{
		filter.ParamFilter:       {`{"member_count":{"$gte":1},"email":{"$ilike":"%25@example.com"}}`},
		filter.ParamSort:         {"-member_count,email"},
		filter.ParamFacets:       {"member_count"},
		filter.ParamIncludeTotal: {"true"},
	})
	require.NoError(t, err)
	plan, err := accounts.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	assert.Contains(t, plan.Page.SQL, "("+memberCountExpr+") >= $")
	assert.Contains(t, plan.Page.SQL, "("+emailExpr+") ILIKE $")
	assert.Contains(t, plan.Page.SQL, "ORDER BY ("+memberCountExpr+") DESC")
	assert.Contains(t, plan.Count.SQL, "("+memberCountExpr+") >= $")
	assert.Contains(t, plan.Facets.SQL, "("+memberCountExpr+")")
}

func TestReadExpr_Refusals(t *testing.T) {
	pg := filter.PostgresDialect{}
	tests := []struct {
		name string
		opt  parse.ResourceOption
		want string
	}{
		{"not a column", parse.ReadExpr("nope", "1"), `computed column "nope": is not a column`},
		{"declared twice", parse.ReadExpr("email", "accounts.email"), `computed column "email": is declared twice`},
		{"no expression", parse.ReadExpr("name", "  "), `computed column "name": has no expression`},
		{"the key", parse.ReadExpr("id", "1"), `computed column "id": is the key, tenant or deleted column`},
		{"the tenant", parse.ReadExpr("tenant_id", "'t1'"), `computed column "tenant_id": is the key, tenant or deleted column`},
		{"not read", parse.ReadColumns("id", "tenant_id", "name", "email", "group_ids"), `computed column "member_count": is not read`},
		{"patched", parse.ReadExpr("name", "upper(accounts.name)"), `computed column "name": is patched at /name`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := computedAccounts(pg, tt.opt)
			assert.ErrorContains(t, err, tt.want)
		})
	}

	type document struct {
		ID    int            `json:"id"    db:"id"`
		Attrs map[string]any `json:"attrs" db:"attrs" parse:"filter" paths:"color:string"`
	}
	_, err := parse.NewResource[document]("documents", parse.ReadExpr("attrs", "jsonb_build_object('color', 'red')"))
	assert.ErrorContains(t, err, "declares document paths")

	type membership struct {
		ID        int `json:"id"         db:"id"         parse:"filter"`
		AccountID int `json:"account_id" db:"account_id" parse:"filter"`
	}
	memberships := parse.MustResource[membership]("memberships", parse.ReadExpr("account_id", "1"))
	accounts, err := computedAccounts(pg)
	require.NoError(t, err)
	err = accounts.Relate("memberships", parse.HasMany(memberships, "account_id"))
	assert.ErrorContains(t, err, `correlates on memberships's computed column "account_id"`)
}
