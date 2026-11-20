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
	Email       string  `json:"email"        db:"email"        parse:"filter,sort"`
	MemberCount int     `json:"member_count" db:"member_count" parse:"filter,sort,facet"`
	GroupIDs    []int64 `json:"group_ids"    db:"group_ids"`
}

const (
	memberCountExpr = "SELECT count(*) FROM memberships m WHERE m.account_id = accounts.id"
	emailExpr       = "coalesce(accounts.email, '')"
)

// groupIDsExpr is the account's group keys: a native array on PostgreSQL,
// JSON text on SQLite, as parse holds arrays on each.
func groupIDsExpr(d filter.Dialect) string {
	if _, ok := d.(filter.SQLiteDialect); ok {
		return "SELECT json_group_array(group_id) FROM (SELECT g.group_id FROM account_groups g WHERE g.account_id = accounts.id ORDER BY g.group_id)"
	}
	return "array(SELECT g.group_id FROM account_groups g WHERE g.account_id = accounts.id ORDER BY g.group_id)"
}

func computedAccounts(d filter.Dialect, opts ...parse.ResourceOption) (*parse.Resource[computedAccount], error) {
	return parse.NewResource[computedAccount]("accounts", append([]parse.ResourceOption{
		parse.TenantColumn("tenant_id"), parse.PageSize(2, 10),
		parse.ReadExpr("member_count", memberCountExpr),
		parse.ReadExpr("email", emailExpr),
		parse.ReadExpr("group_ids", groupIDsExpr(d)),
	}, opts...)...)
}
