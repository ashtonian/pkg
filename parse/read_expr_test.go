//go:build unit || integration

package parse_test

import (
	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// computedAccount reads three columns accounts has no column for: a count of
// its memberships, its groups' keys, and its nullable email as text.
type computedAccount struct {
	ID          int     `json:"id"           db:"id"           parse:"filter,sort"`
	TenantID    string  `json:"-"            db:"tenant_id"`
	Name        string  `json:"name"         db:"name"         parse:"filter,sort,patch"`
	Email       string  `json:"email"        db:"email"        parse:"filter,sort,search"`
	MemberCount int     `json:"member_count" db:"member_count" parse:"filter,sort,facet"`
	GroupIDs    []int64 `json:"group_ids"    db:"group_ids"`
}

// accessGroup is a group accounts belong to through account_groups.
type accessGroup struct {
	ID       int64  `json:"id" db:"id" parse:"filter"`
	TenantID string `json:"-"  db:"tenant_id"`
}

const (
	memberCountExpr    = "SELECT count(*) FROM memberships m WHERE m.account_id = accounts.id"
	pgEmailExpr        = "coalesce(accounts.email, '')"
	sqliteEmailExpr    = "ifnull(accounts.email, '')"
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

var (
	accessGroups = parse.MustResource[accessGroup]("access_groups", parse.TenantColumn("tenant_id"))
	// computedAccounts is declared once, before any dialect is known, and
	// serves PostgreSQL and SQLite with each one's expressions.
	computedAccounts = newComputedAccounts(groupIDsExpr)
)

func newComputedAccounts(groupIDs parse.ColumnExpr) *parse.Resource[computedAccount] {
	r := parse.MustResource[computedAccount]("accounts", computedAccountOptions(groupIDs)...)
	r.MustRelate("groups", parse.ManyToMany(accessGroups, parse.Through("account_groups", "account_id", "group_id"), parse.Writable(), parse.Includable()))
	return r
}

// computedAccountOptions declares accounts and its computed columns, its
// group keys read through groupIDs.
func computedAccountOptions(groupIDs parse.ColumnExpr, opts ...parse.ResourceOption) []parse.ResourceOption {
	return append([]parse.ResourceOption{
		parse.TenantColumn("tenant_id"), parse.PageSize(2, 10),
		parse.ReadExpr("member_count", parse.SQL(memberCountExpr)),
		parse.ReadExpr("email", emailExpr),
		parse.ReadExpr("group_ids", groupIDs),
	}, opts...)
}
