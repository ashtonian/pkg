//go:build unit || integration || concurrency

package parsetest

import (
	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// ComputedAccount reads four columns accounts has no column for: a count of
// its memberships, its groups' keys, its nullable email as text, and a
// hidden contact only server policies name.
type ComputedAccount struct {
	ID          int     `json:"id"           db:"id"           parse:"filter,sort"`
	TenantID    string  `json:"-"            db:"tenant_id"`
	Name        string  `json:"name"         db:"name"         parse:"filter,sort,patch"`
	Email       string  `json:"email"        db:"email"        parse:"filter,sort,search"`
	MemberCount int     `json:"member_count" db:"member_count" parse:"filter,sort,facet"`
	GroupIDs    []int64 `json:"group_ids"    db:"group_ids"`
	Contact     string  `json:"-"            db:"contact"`
}

// AccessGroup is a group accounts belong to through account_groups; its
// size is a count of them.
type AccessGroup struct {
	ID       int64  `json:"id"   db:"id"   parse:"filter"`
	TenantID string `json:"-"    db:"tenant_id"`
	Size     int    `json:"size" db:"size" parse:"filter"`
}

// AccountTenant has its tenant's accounts.
type AccountTenant struct {
	ID string `json:"id" db:"id" parse:"filter"`
}

// accountMembership belongs to an account.
type accountMembership struct {
	ID        int `json:"id"         db:"id"         parse:"filter"`
	AccountID int `json:"account_id" db:"account_id" parse:"filter"`
}

// ComputedAccountFixture: accounts 1–5 of t1 hold 2, 1, 2, 0 and 1
// memberships (ties on the count), account 2 has no email, and t2's
// account 6 has the most memberships of all. Account 1 is in groups 10 and
// 20, account 2 in group 20 and account 3 in group 30, so group 20 alone
// has two accounts.
func ComputedAccountFixture() []string {
	fixture := []string{
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
	return fixture
}

const (
	GroupSizeExpr      = "SELECT count(*) FROM account_groups ag WHERE ag.group_id = access_groups.id"
	MemberCountExpr    = "SELECT count(*) FROM memberships m WHERE m.account_id = accounts.id"
	PgEmailExpr        = "coalesce(accounts.email, '')"
	SqliteEmailExpr    = "ifnull(accounts.email, '')"
	PgContactExpr      = "coalesce(accounts.email, accounts.name)"
	SqliteContactExpr  = "ifnull(accounts.email, accounts.name)"
	PgGroupIDsExpr     = "array(SELECT g.group_id FROM account_groups g WHERE g.account_id = accounts.id ORDER BY g.group_id)"
	SqliteGroupIDsExpr = "SELECT json_group_array(group_id) FROM (SELECT g.group_id FROM account_groups g WHERE g.account_id = accounts.id ORDER BY g.group_id)"
)

// emailExpr reads the nullable email through each dialect's own function.
func emailExpr(d filter.Dialect) (string, error) {
	_, sqlite := d.(filter.SQLiteDialect)
	if sqlite {
		return SqliteEmailExpr, nil
	}
	return PgEmailExpr, nil
}

// contactExpr is the account's email, or its name without one.
func contactExpr(d filter.Dialect) (string, error) {
	_, sqlite := d.(filter.SQLiteDialect)
	if sqlite {
		return SqliteContactExpr, nil
	}
	return PgContactExpr, nil
}

// GroupIDsExpr is the account's group keys: a native array on PostgreSQL,
// JSON text on SQLite, as parse holds arrays on each.
func GroupIDsExpr(d filter.Dialect) (string, error) {
	switch d.(type) {
	case filter.PostgresDialect:
		return PgGroupIDsExpr, nil
	case filter.SQLiteDialect:
		return SqliteGroupIDsExpr, nil
	}
	return "", filter.ErrUnsupportedDialect
}

// NewComputedResources declares the resources on p, so they read p's
// dialect's expressions. It relates accounts and their groups both ways,
// and tenants and memberships to accounts: filters reach computed columns
// through has-many, many-to-many and belongs-to relations, at depth 2, and
// around the accounts–groups cycle. A tenant's shared_accounts are any
// tenant's the policy for accounts admits (Scope.Related).
func NewComputedResources(p *parse.Provider) (*parse.Resource[AccessGroup], *parse.Resource[ComputedAccount], *parse.Resource[AccountTenant], *parse.Resource[accountMembership]) {
	groups := NewAccessGroups(p)
	accounts := NewComputedAccounts(groups, GroupIDsExpr)
	groups.MustRelate("accounts", parse.ManyToMany(accounts, parse.Through("account_groups", "group_id", "account_id"), parse.Indexed()))
	tenants := NewAccountTenants(accounts)
	memberships := parse.MustResource[accountMembership](p, "memberships")
	memberships.MustRelate("account", parse.BelongsTo(accounts, "account_id"))
	return groups, accounts, tenants, memberships
}

// NewAccessGroups declares access_groups on p, without relations.
func NewAccessGroups(p *parse.Provider) *parse.Resource[AccessGroup] {
	return parse.MustResource[AccessGroup](p, "access_groups", parse.TenantColumn("tenant_id"), parse.ReadExpr("size", parse.SQL(GroupSizeExpr)))
}

// NewComputedAccounts declares accounts on its groups' provider, its group
// keys read through groupIDs.
func NewComputedAccounts(groups *parse.Resource[AccessGroup], groupIDs parse.ColumnExpr) *parse.Resource[ComputedAccount] {
	r := parse.MustResource[ComputedAccount](groups.Provider(), "accounts", ComputedAccountOptions(groupIDs)...)
	r.MustRelate("groups", parse.ManyToMany(groups, parse.Through("account_groups", "account_id", "group_id"), parse.Writable(), parse.Includable(), parse.Indexed()))
	return r
}

// NewAccountTenants declares tenants on its accounts' provider.
func NewAccountTenants(accounts *parse.Resource[ComputedAccount]) *parse.Resource[AccountTenant] {
	tenants := parse.MustResource[AccountTenant](accounts.Provider(), "tenants")
	tenants.MustRelate("accounts", parse.HasMany(accounts, "tenant_id", parse.Indexed(), parse.Includable(), parse.Facetable()))
	tenants.MustRelate("shared_accounts", parse.HasMany(accounts, "tenant_id", parse.Indexed(), parse.Includable(), parse.PolicyScoped()))
	return tenants
}

// ComputedAccountOptions declares accounts and its computed columns, its
// group keys read through groupIDs.
func ComputedAccountOptions(groupIDs parse.ColumnExpr, opts ...parse.ResourceOption) []parse.ResourceOption {
	return append([]parse.ResourceOption{
		parse.TenantColumn("tenant_id"), parse.PageSize(2, 10),
		parse.ReadExpr("member_count", parse.SQL(MemberCountExpr)),
		parse.ReadExpr("email", emailExpr),
		parse.ReadExpr("group_ids", groupIDs),
		parse.ReadExpr("contact", contactExpr),
	}, opts...)
}
