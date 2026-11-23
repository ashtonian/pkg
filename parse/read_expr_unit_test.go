//go:build unit

package parse_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

func TestReadExpr_Columns(t *testing.T) {
	for _, tt := range computedDialects {
		t.Run(tt.dialect.Name(), func(t *testing.T) {
			columns, err := computedAccounts.Columns(tt.dialect)
			require.NoError(t, err)
			assert.Equal(t, []string{
				`"accounts"."id"`,
				`"accounts"."tenant_id"`,
				`"accounts"."name"`,
				"(" + tt.email + `) AS "email"`,
				"(" + memberCountExpr + `) AS "member_count"`,
				"(" + tt.groupIDs + `) AS "group_ids"`,
				"(" + tt.contact + `) AS "contact"`,
			}, columns)
		})
	}
	assert.Equal(t, []string{"id", "tenant_id", "name", "email", "member_count", "group_ids", "contact"}, computedAccounts.ColumnNames())

	type window struct {
		ID   int           `json:"id"   db:"id"`
		Idle core.Interval `json:"idle" db:"idle"`
	}
	windows, err := parse.NewResource[window]("windows", parse.ReadExpr("idle", parse.SQL("windows.ends - windows.starts")))
	require.NoError(t, err)
	cols, err := windows.Columns(filter.PostgresDialect{})
	require.NoError(t, err)
	assert.Contains(t, cols[1], "(windows.ends - windows.starts)", "a declared read cast applies to the expression")
	assert.Contains(t, cols[1], ` AS "idle"`)
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

	assert.Equal(t, `"accounts"."member_count"`, filter.FieldExpr(filter.PostgresDialect{}, computedAccounts.FilterSchema().Fields["member_count"]), "the shared schema stays dialect-neutral")
	assert.Empty(t, computedAccounts.SortSchema().Fields["email"].Expr, "the shared schema stays dialect-neutral")
}

// The resource's ListQuery compiles its computed columns as BuildList does.
func TestReadExpr_ListQueryCompilesTheDialectsExpression(t *testing.T) {
	for _, tt := range computedDialects {
		t.Run(tt.dialect.Name(), func(t *testing.T) {
			lq, err := computedAccounts.ListQuery(tt.dialect)
			require.NoError(t, err)
			res, err := lq.Build(filter.ListFilter(`{"member_count":{"$gte":1}}`), filter.ListSort("-member_count"))
			require.NoError(t, err)
			assert.Contains(t, res.Where, "("+memberCountExpr+") >= ")
			assert.Contains(t, res.OrderBy, "("+memberCountExpr+") DESC")
		})
	}
}

// A column mapper writes each field's SQL, so a computed column's fields are
// found by their column, not by table.column text: its filters, sorts,
// facets and search still compile against the expression, never the
// mapper's SQL for a column the table does not hold.
func TestReadExpr_UnderAColumnMapper(t *testing.T) {
	type tally struct {
		ID    string `json:"id"    db:"id"    parse:"filter,sort"`
		Name  string `json:"name"  db:"name"  parse:"filter"`
		Label string `json:"label" db:"label" parse:"filter,sort,facet,search"`
	}
	tallies, err := parse.NewResource[tally]("tallies", mappedColumns, parse.ReadExpr("label", parse.SQL("upper(tallies.name)")))
	require.NoError(t, err)
	req, err := tallies.ParseList(url.Values{
		filter.ParamFilter:       {`{"label":{"$ne":""}}`},
		filter.ParamSort:         {"label"},
		filter.ParamFacets:       {"label"},
		filter.ParamSearch:       {"x"},
		filter.ParamIncludeTotal: {"true"},
	})
	require.NoError(t, err)
	plan, err := tallies.BuildList(filter.SQLiteDialect{}, req, parse.Scope{})
	require.NoError(t, err)
	label := "(upper(tallies.name))"
	assert.Contains(t, plan.Page.SQL, label+" <> ")
	assert.Contains(t, plan.Page.SQL, "ORDER BY "+label)
	assert.Contains(t, plan.Count.SQL, label, "search")
	assert.Contains(t, plan.Facets.SQL, label)
	for _, sql := range []string{plan.Page.SQL, plan.Count.SQL, plan.Facets.SQL} {
		assert.NotContains(t, sql, `"tallies"."label"`)
	}
}

// Each build compiles under its own dialect's settings: two PostgreSQL
// dialects that resolve one expression order it by their own collation.
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
	accounts := newComputedAccounts(accessGroups, postgresOnly)
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
			_, err := accounts.BuildUpdateBatch(d, []parse.RowUpdate{{Key: parse.Key{1}, Patch: rename}}, scope, parse.UpdateBatchLimits{})
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

	// A build that compiles the refusing target's schema fails too; one
	// that never reaches the target does not.
	tenants := newAccountTenants(accounts)
	for _, tt := range []struct {
		name   string
		query  url.Values
		scope  parse.Scope
		refuse bool
	}{
		{"a relation filter", url.Values{filter.ParamFilter: {`{"accounts":{"$some":{"name":"a"}}}`}}, scope, true},
		{"a relation include", url.Values{filter.ParamInclude: {"accounts"}}, scope, true},
		{"a relation facet", url.Values{filter.ParamFacets: {"accounts"}}, scope, true},
		{"a target policy", url.Values{filter.ParamFilter: {`{"shared_accounts":{"$some":{}}}`}}, parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{
			"accounts": {Constraints: []*filter.PolicyConstraint{{Field: "member_count", Operator: filter.OpGte, Value: 1}}},
		}}, true},
		{"no relation", url.Values{filter.ParamFilter: {`{"id":"t1"}`}}, scope, false},
	} {
		t.Run("owner's "+tt.name, func(t *testing.T) {
			req, err := tenants.ParseList(tt.query)
			require.NoError(t, err)
			_, err = tenants.BuildList(filter.SQLiteDialect{}, req, tt.scope)
			if !tt.refuse {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, filter.ErrUnsupportedDialect)
			assert.ErrorContains(t, err, `parse: accounts: computed column "group_ids" for dialect sqlite`)
			_, isParseError := errors.AsType[*core.ParseError](err)
			assert.False(t, isParseError, "a dialect refusal is no filter error")
			_, err = tenants.BuildList(filter.PostgresDialect{}, req, tt.scope)
			assert.NoError(t, err)
		})
	}

	t.Run("owner's membership change", func(t *testing.T) {
		groups := parse.MustResource[accessGroup]("access_groups", parse.TenantColumn("tenant_id"))
		groups.MustRelate("accounts", parse.ManyToMany(accounts, parse.Through("account_groups", "group_id", "account_id"), parse.Writable()))
		p, err := groups.ParseMergePatch([]byte(`{"accounts":[1]}`))
		require.NoError(t, err)
		_, err = groups.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByID(20), scope)
		assert.ErrorIs(t, err, filter.ErrUnsupportedDialect)
		assert.ErrorContains(t, err, `parse: accounts: computed column "group_ids" for dialect sqlite`)
		_, err = groups.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID(20), scope)
		assert.NoError(t, err)
	})
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
		{"customized", parse.CustomizeSchemas(func(s parse.Schemas) error {
			s.Filter.Computed = []filter.ComputedColumn{{Column: "name", Expr: func(filter.Dialect) (string, error) { return "1", nil }}}
			return nil
		}), "computed columns are declared with ReadExpr"},
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

	type untagged struct {
		ID    int            `json:"id"    db:"id"`
		Attrs map[string]any `json:"attrs" db:"attrs" parse:"filter"`
	}
	computedDocs, err := parse.NewResource[untagged]("documents", parse.ReadExpr("attrs", parse.SQL("jsonb_build_object('color', 'red')")))
	require.NoError(t, err)
	for _, decl := range []filter.PathDecl{
		{Path: "attrs.color", Type: core.FieldTypeString},
		{Path: "attrs.color", Type: core.FieldTypeString, Sort: true},
	} {
		_, err = computedDocs.WithPaths(decl)
		assert.ErrorContains(t, err, `parse: documents: computed column "attrs": declares document paths, which read a stored document`, "a request's paths as the tags'")
	}
	storedDocs := parse.MustResource[untagged]("documents")
	_, err = storedDocs.WithPaths(filter.PathDecl{Path: "attrs.color", Type: core.FieldTypeString, Sort: true})
	require.NoError(t, err)

	type membership struct {
		ID        int `json:"id"         db:"id"         parse:"filter"`
		AccountID int `json:"account_id" db:"account_id" parse:"filter"`
	}
	memberships := parse.MustResource[membership]("memberships", parse.ReadExpr("account_id", parse.SQL("1")))
	accounts := newComputedAccounts(accessGroups, groupIDsExpr)
	err = accounts.Relate("memberships", parse.HasMany(memberships, "account_id"))
	assert.ErrorContains(t, err, `correlates on memberships's computed column "account_id"`)

	type labelledAccount struct {
		ID       int     `json:"id"        db:"id"`
		OwnerKey int     `json:"owner_key" db:"owner_key"`
		Labels   []int64 `json:"labels"    db:"-" parse:"filter" relation:"account_labels,account_key,label_id,owner_key"`
	}
	_, err = parse.NewResource[labelledAccount]("accounts")
	require.NoError(t, err)
	_, err = parse.NewResource[labelledAccount]("accounts", parse.ReadExpr("owner_key", parse.SQL("accounts.id * 10")))
	assert.ErrorContains(t, err, `parse: accounts: relation "labels" correlates on computed column "owner_key"`, "a relation tag as Relate")
}

// A relation's filter compiles its target's computed columns as the
// target's expressions for the owner's dialect: through a has-many, a
// many-to-many and a belongs-to, at depth 1 and 2, and in the target's
// policy (Scope.Related).
func TestReadExpr_RelationFiltersCompileTheTargetsExpressions(t *testing.T) {
	scope := parse.Scope{Tenant: "t1"}
	memberCount, size := "("+memberCountExpr+")", "("+groupSizeExpr+")"
	for _, tt := range computedDialects {
		t.Run(tt.dialect.Name(), func(t *testing.T) {
			email := "(" + tt.email + ")"
			tests := []struct {
				name, sql, want string
			}{
				{"has-many", pageSQL(t, accountTenants, tt.dialect, `{"accounts":{"$some":{"email":""}}}`, scope), email + " = "},
				{"many-to-many", pageSQL(t, accessGroups, tt.dialect, `{"accounts":{"$some":{"member_count":{"$gte":2}}}}`, scope), memberCount + " >= "},
				{"belongs-to", pageSQL(t, accountMemberships, tt.dialect, `{"account":{"$is":{"email":""}}}`, scope), email + " = "},
				{"has-many, then many-to-many", pageSQL(t, accountTenants, tt.dialect, `{"accounts":{"$some":{"groups":{"$some":{"size":2}}}}}`, scope), size + " = "},
				{"belongs-to, then many-to-many", pageSQL(t, accountMemberships, tt.dialect, `{"account":{"$is_not":{"groups":{"$some":{"size":2}}}}}`, scope), size + " = "},
				{"the target's policy", pageSQL(t, accountTenants, tt.dialect, `{"shared_accounts":{"$some":{"name":"a"}}}`, parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{
					"accounts": {Constraints: []*filter.PolicyConstraint{{Field: "email", Operator: filter.OpEq, Value: ""}}},
				}}), email + " = "},
			}
			for _, c := range tests {
				assert.Contains(t, c.sql, c.want, c.name)
				for _, other := range computedDialects {
					if other.dialect.Name() != tt.dialect.Name() {
						assert.NotContains(t, c.sql, other.email, c.name)
					}
				}
			}
		})
	}
	assert.Equal(t, `"accounts"."email"`, filter.FieldExpr(filter.PostgresDialect{}, accountTenants.FilterSchema().Fields["accounts"].Related.Fields["email"]), "the relation's schema stays dialect-neutral")
}

// Rebinding walks the filter, not the relation graph: around the
// groups–accounts cycle it ends where the filter does. Back at groups the
// subquery aliases the table, which a stored column follows and a computed
// column's expression, naming the table, cannot.
func TestReadExpr_RelationCycles(t *testing.T) {
	scope := parse.Scope{Tenant: "t1"}
	for _, tt := range computedDialects {
		sql := pageSQL(t, accessGroups, tt.dialect, `{"accounts":{"$some":{"email":"","groups":{"$some":{"id":20}}}}}`, scope)
		assert.Contains(t, sql, `"access_groups_rel2__"."id" = `)
		assert.Contains(t, sql, "("+tt.email+") = ")

		req, err := accessGroups.ParseList(url.Values{filter.ParamFilter: {`{"accounts":{"$some":{"groups":{"$some":{"size":2}}}}}`}})
		require.NoError(t, err)
		_, err = accessGroups.BuildList(tt.dialect, req, scope)
		assert.ErrorContains(t, err, "field size is not a column of access_groups and cannot be filtered through a relation")
	}
}

// The relation's includes and facets compile its target's policy for the
// owner's dialect too. They read the target under an alias, so a policy on
// a computed column fails the build instead of reading a stored column the
// table lacks.
func TestReadExpr_RelationIncludesCompileTheTargetsPolicy(t *testing.T) {
	scope := parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{
		"accounts": {Constraints: []*filter.PolicyConstraint{{Field: "member_count", Operator: filter.OpGte, Value: 1}}},
	}}
	req, err := accountTenants.ParseList(url.Values{filter.ParamInclude: {"shared_accounts"}})
	require.NoError(t, err)
	plan, err := accountTenants.BuildList(filter.SQLiteDialect{}, req, scope)
	require.NoError(t, err)
	_, err = accountTenants.IncludeQueries(plan, []parse.Key{{"t1"}})
	assert.ErrorContains(t, err, "field member_count is not a column of accounts and cannot be filtered through a relation")

	// A membership change holds its pairs to the targets the same policy
	// admits.
	groups := parse.MustResource[accessGroup]("access_groups", parse.TenantColumn("tenant_id"))
	groups.MustRelate("accounts", parse.ManyToMany(computedAccounts, parse.Through("account_groups", "group_id", "account_id"), parse.Writable(), parse.PolicyScoped()))
	p, err := groups.ParseMergePatch([]byte(`{"accounts":[1]}`))
	require.NoError(t, err)
	update, err := groups.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByID(20), scope)
	require.NoError(t, err)
	_, err = groups.RelationStatements(update, []parse.Key{{int64(20)}})
	assert.ErrorContains(t, err, "field member_count is not a column of accounts and cannot be filtered through a relation")
}

// pageSQL is the page statement r builds for filterJSON on d.
func pageSQL[T any](t *testing.T, r *parse.Resource[T], d filter.Dialect, filterJSON string, scope parse.Scope) string {
	t.Helper()
	req, err := r.ParseList(url.Values{filter.ParamFilter: {filterJSON}})
	require.NoError(t, err)
	plan, err := r.BuildList(d, req, scope)
	require.NoError(t, err)
	return plan.Page.SQL
}

// An update's stamp and counters write the table's own columns, which a
// computed column is not.
func TestReadExpr_UpdatesNeitherStampNorIncrementComputedColumns(t *testing.T) {
	rename := computedPatch(t, computedAccounts, `{"name":"x"}`)
	scope := parse.Scope{Tenant: "t1"}
	tests := []struct {
		name string
		opt  parse.UpdateOption
		want string
	}{
		{"Increment", parse.Increment("member_count"), `parse: accounts: computed column "member_count": is incremented; an update writes the table's own columns`},
		{"Stamp", parse.Stamp("email", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)), `parse: accounts: computed column "email": is stamped; an update writes the table's own columns`},
		{"StampSQL", parse.StampSQL("email", "CURRENT_TIMESTAMP"), `parse: accounts: computed column "email": is stamped; an update writes the table's own columns`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, d := range computedDialects {
				_, err := computedAccounts.BuildUpdate(d.dialect, rename, parse.ByID(1), scope, tt.opt)
				assert.ErrorContains(t, err, tt.want, "BuildUpdate %s", d.dialect.Name())
				_, err = computedAccounts.BuildUpdateBatch(d.dialect, []parse.RowUpdate{{Key: parse.Key{1}, Patch: rename}}, scope, parse.UpdateBatchLimits{}, tt.opt)
				assert.ErrorContains(t, err, tt.want, "BuildUpdateBatch %s", d.dialect.Name())
			}
		})
	}
}

// Reread reads the owners again only when relation statements may have
// changed what a computed column reads, held to the update's tenant.
func TestReadExpr_Reread(t *testing.T) {
	scope := parse.Scope{Tenant: "t1"}
	pg := filter.PostgresDialect{}
	rename, err := computedAccounts.BuildUpdate(pg, computedPatch(t, computedAccounts, `{"name":"x"}`), parse.ByID(1), scope)
	require.NoError(t, err)
	stmt, err := computedAccounts.Reread(rename, []parse.Key{{1}})
	require.NoError(t, err)
	assert.Nil(t, stmt, "no relation statements: Owner's rows are current")

	groups, err := computedAccounts.BuildUpdate(pg, computedPatch(t, computedAccounts, `{"groups":[30]}`), parse.ByID(1), scope)
	require.NoError(t, err)
	stmt, err = computedAccounts.Reread(groups, nil)
	require.NoError(t, err)
	assert.Nil(t, stmt, "no owners")
	stmt, err = computedAccounts.Reread(groups, []parse.Key{{1}, {3}})
	require.NoError(t, err)
	require.NotNil(t, stmt)
	columns, err := computedAccounts.Columns(pg)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(stmt.SQL, "SELECT "+strings.Join(columns, ", ")+` FROM "accounts" WHERE `), stmt.SQL)
	assert.Contains(t, stmt.SQL, `"accounts"."tenant_id" = `)
	assert.Contains(t, stmt.Args, "t1")

	_, err = accessGroups.Reread(groups, []parse.Key{{1}})
	assert.ErrorContains(t, err, `update plan built for "accounts"`)
}

// A server policy names a computed column requests cannot filter by — a
// hidden one, by its column — and compiles it as the dialect's expression:
// on the resource's own lists and updates, and as a relation's target
// (Scope.Related).
func TestReadExpr_PolicyFieldsCompileAgainstEachDialectsExpression(t *testing.T) {
	contact := &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "contact", Operator: filter.OpEq, Value: "b"}}}
	_, err := computedAccounts.ParseList(url.Values{filter.ParamFilter: {`{"contact":"b"}`}})
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe, "requests cannot filter by it")
	assert.Equal(t, filter.ECodeUnknownField.String(), pe.Code)

	for _, tt := range computedDialects {
		t.Run(tt.dialect.Name(), func(t *testing.T) {
			expr := "(" + tt.contact + ") = "
			plan, err := computedAccounts.BuildList(tt.dialect, nil, parse.Scope{Tenant: "t1", Policy: contact})
			require.NoError(t, err)
			assert.Contains(t, plan.Page.SQL, expr)

			update, err := computedAccounts.BuildUpdate(tt.dialect, computedPatch(t, computedAccounts, `{"name":"x"}`), parse.ByID(2), parse.Scope{Tenant: "t1", Policy: contact})
			require.NoError(t, err)
			assert.Contains(t, update.Owner.SQL, expr)

			sql := pageSQL(t, accountTenants, tt.dialect, `{"shared_accounts":{"$some":{}}}`, parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{"accounts": contact}})
			assert.Contains(t, sql, expr, "the target's policy")

			for _, other := range computedDialects {
				if other.dialect.Name() != tt.dialect.Name() {
					assert.NotContains(t, plan.Page.SQL+update.Owner.SQL+sql, other.contact)
				}
			}
		})
	}
	field, ok, err := computedAccounts.FilterSchema().PolicyField("contact")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, `"accounts"."contact"`, filter.FieldExpr(filter.PostgresDialect{}, field), "the shared schema stays dialect-neutral")
}

func computedPatch(t *testing.T, accounts *parse.Resource[computedAccount], body string) *patch.ParsedPatch {
	t.Helper()
	p, err := accounts.ParseMergePatch([]byte(body))
	require.NoError(t, err)
	return p
}
