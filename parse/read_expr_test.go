//go:build unit || integration

package parse_test

import (
	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// computedAccount reads four columns accounts has no column for: a count of
// its memberships, its groups' keys, its nullable email as text, and a
// hidden contact only server policies name.
type computedAccount struct {
	ID          int     `json:"id"           db:"id"           parse:"filter,sort"`
	TenantID    string  `json:"-"            db:"tenant_id"`
	Name        string  `json:"name"         db:"name"         parse:"filter,sort,patch"`
	Email       string  `json:"email"        db:"email"        parse:"filter,sort,search"`
	MemberCount int     `json:"member_count" db:"member_count" parse:"filter,sort,facet"`
	GroupIDs    []int64 `json:"group_ids"    db:"group_ids"`
	Contact     string  `json:"-"            db:"contact"`
}

// accessGroup is a group accounts belong to through account_groups; its
// size is a count of them.
type accessGroup struct {
	ID       int64  `json:"id"   db:"id"   parse:"filter"`
	TenantID string `json:"-"    db:"tenant_id"`
	Size     int    `json:"size" db:"size" parse:"filter"`
}

// accountTenant has its tenant's accounts.
type accountTenant struct {
	ID string `json:"id" db:"id" parse:"filter"`
}

// accountMembership belongs to an account.
type accountMembership struct {
	ID        int `json:"id"         db:"id"         parse:"filter"`
	AccountID int `json:"account_id" db:"account_id" parse:"filter"`
}

const (
	groupSizeExpr      = "SELECT count(*) FROM account_groups ag WHERE ag.group_id = access_groups.id"
	memberCountExpr    = "SELECT count(*) FROM memberships m WHERE m.account_id = accounts.id"
	pgEmailExpr        = "coalesce(accounts.email, '')"
	sqliteEmailExpr    = "ifnull(accounts.email, '')"
	pgContactExpr      = "coalesce(accounts.email, accounts.name)"
	sqliteContactExpr  = "ifnull(accounts.email, accounts.name)"
	pgGroupIDsExpr     = "array(SELECT g.group_id FROM account_groups g WHERE g.account_id = accounts.id ORDER BY g.group_id)"
	sqliteGroupIDsExpr = "SELECT json_group_array(group_id) FROM (SELECT g.group_id FROM account_groups g WHERE g.account_id = accounts.id ORDER BY g.group_id)"
)

// emailExpr reads the nullable email through each dialect's own function.
func emailExpr(d filter.Dialect) (string, error) {
	if _, ok := d.(filter.SQLiteDialect); ok {
		return sqliteEmailExpr, nil
	}
	return pgEmailExpr, nil
}

// contactExpr is the account's email, or its name without one.
func contactExpr(d filter.Dialect) (string, error) {
	if _, ok := d.(filter.SQLiteDialect); ok {
		return sqliteContactExpr, nil
	}
	return pgContactExpr, nil
}

// groupIDsExpr is the account's group keys: a native array on PostgreSQL,
// JSON text on SQLite, as parse holds arrays on each.
func groupIDsExpr(d filter.Dialect) (string, error) {
	switch d.(type) {
	case filter.PostgresDialect:
		return pgGroupIDsExpr, nil
	case filter.SQLiteDialect:
		return sqliteGroupIDsExpr, nil
	}
	return "", filter.ErrUnsupportedDialect
}

// The resources are declared once, before any dialect is known, and serve
// PostgreSQL and SQLite with each one's expressions.
var accessGroups, computedAccounts, accountTenants, accountMemberships = newComputedResources()

// newComputedResources relates accounts and their groups both ways, and
// tenants and memberships to accounts: filters reach computed columns
// through has-many, many-to-many and belongs-to relations, at depth 2, and
// around the accounts–groups cycle. A tenant's shared_accounts are any
// tenant's the policy for accounts admits (Scope.Related).
func newComputedResources() (*parse.Resource[accessGroup], *parse.Resource[computedAccount], *parse.Resource[accountTenant], *parse.Resource[accountMembership]) {
	groups := parse.MustResource[accessGroup]("access_groups", parse.TenantColumn("tenant_id"), parse.ReadExpr("size", parse.SQL(groupSizeExpr)))
	accounts := newComputedAccounts(groups, groupIDsExpr)
	groups.MustRelate("accounts", parse.ManyToMany(accounts, parse.Through("account_groups", "group_id", "account_id"), parse.Indexed()))
	tenants := newAccountTenants(accounts)
	memberships := parse.MustResource[accountMembership]("memberships")
	memberships.MustRelate("account", parse.BelongsTo(accounts, "account_id"))
	return groups, accounts, tenants, memberships
}

func newComputedAccounts(groups *parse.Resource[accessGroup], groupIDs parse.ColumnExpr) *parse.Resource[computedAccount] {
	r := parse.MustResource[computedAccount]("accounts", computedAccountOptions(groupIDs)...)
	r.MustRelate("groups", parse.ManyToMany(groups, parse.Through("account_groups", "account_id", "group_id"), parse.Writable(), parse.Includable(), parse.Indexed()))
	return r
}

func newAccountTenants(accounts *parse.Resource[computedAccount]) *parse.Resource[accountTenant] {
	tenants := parse.MustResource[accountTenant]("tenants")
	tenants.MustRelate("accounts", parse.HasMany(accounts, "tenant_id", parse.Indexed(), parse.Includable(), parse.Facetable()))
	tenants.MustRelate("shared_accounts", parse.HasMany(accounts, "tenant_id", parse.Indexed(), parse.Includable(), parse.PolicyScoped()))
	return tenants
}

// computedAccountOptions declares accounts and its computed columns, its
// group keys read through groupIDs.
func computedAccountOptions(groupIDs parse.ColumnExpr, opts ...parse.ResourceOption) []parse.ResourceOption {
	return append([]parse.ResourceOption{
		parse.TenantColumn("tenant_id"), parse.PageSize(2, 10),
		parse.ReadExpr("member_count", parse.SQL(memberCountExpr)),
		parse.ReadExpr("email", emailExpr),
		parse.ReadExpr("group_ids", groupIDs),
		parse.ReadExpr("contact", contactExpr),
	}, opts...)
}
