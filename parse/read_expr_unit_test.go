//go:build unit

package parse_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/patch"
)

func TestReadExpr_Columns(t *testing.T) {
	for _, tt := range computedDialects {
		t.Run(tt.dialect.Name(), func(t *testing.T) {
			_, accounts, _, _ := parsetest.NewComputedResources(providertest.Compile(tt.dialect))
			assert.Equal(t, []string{
				`"accounts"."id"`,
				`"accounts"."tenant_id"`,
				`"accounts"."name"`,
				"(" + tt.email + `) AS "email"`,
				"(" + parsetest.MemberCountExpr + `) AS "member_count"`,
				"(" + tt.groupIDs + `) AS "group_ids"`,
				"(" + tt.contact + `) AS "contact"`,
			}, accounts.Columns())
			assert.Equal(t, []string{"id", "tenant_id", "name", "email", "member_count", "group_ids", "contact"}, accounts.ColumnNames())
		})
	}

	type window struct {
		ID   int           `json:"id"   db:"id"`
		Idle core.Interval `json:"idle" db:"idle"`
	}
	windows, err := parse.NewResource[window](onPostgres, "windows", parse.ReadExpr("idle", parse.SQL("windows.ends - windows.starts")))
	require.NoError(t, err)
	cols := windows.Columns()
	assert.Contains(t, cols[1], "(windows.ends - windows.starts)", "a declared read cast applies to the expression")
	assert.Contains(t, cols[1], ` AS "idle"`)
}

// A resource compiles its statements against its provider's dialect's
// expressions; its schemas and their document name the column.
func TestReadExpr_SchemasCompileAgainstEachDialectsExpression(t *testing.T) {
	for _, tt := range computedDialects {
		t.Run(tt.dialect.Name(), func(t *testing.T) {
			_, accounts, _, _ := parsetest.NewComputedResources(providertest.Compile(tt.dialect))
			req, err := accounts.ParseList(url.Values{
				filter.ParamFilter:       {`{"member_count":{"$gte":1},"email":{"$ne":""}}`},
				filter.ParamSort:         {"-member_count,email"},
				filter.ParamFacets:       {"member_count"},
				filter.ParamSearch:       {"example"},
				filter.ParamIncludeTotal: {"true"},
			})
			require.NoError(t, err)
			plan, err := accounts.BuildList(req, parse.Scope{Tenant: "t1"})
			require.NoError(t, err)
			memberCount, email := "("+parsetest.MemberCountExpr+")", "("+tt.email+")"
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

			p, err := accounts.ParseFilter(`{"email":"a@example.com"}`)
			require.NoError(t, err)
			update, err := accounts.BuildUpdate(computedPatch(t, accounts, `{"name":"x"}`), parse.ByFilter(p), parse.Scope{Tenant: "t1"})
			require.NoError(t, err)
			assert.Contains(t, update.Owner.SQL, "WHERE ("+email+" = ")

			assert.Equal(t, `"accounts"."member_count"`, filter.FieldExpr(filter.PostgresDialect{}, accounts.FilterSchema().Fields["member_count"]), "the schema names the column")
			assert.Empty(t, accounts.SortSchema().Fields["email"].Expr, "the schema names the column")
		})
	}
}

// The resource's ListQuery compiles its computed columns as BuildList does.
func TestReadExpr_ListQueryCompilesTheDialectsExpression(t *testing.T) {
	for _, tt := range computedDialects {
		t.Run(tt.dialect.Name(), func(t *testing.T) {
			_, accounts, _, _ := parsetest.NewComputedResources(providertest.Compile(tt.dialect))
			lq, err := accounts.ListQuery()
			require.NoError(t, err)
			res, err := lq.Build(filter.ListFilter(`{"member_count":{"$gte":1}}`), filter.ListSort("-member_count"))
			require.NoError(t, err)
			assert.Contains(t, res.Where, "("+parsetest.MemberCountExpr+") >= ")
			assert.Contains(t, res.OrderBy, "("+parsetest.MemberCountExpr+") DESC")
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
	tallies, err := parse.NewResource[tally](onSQLite, "tallies", mappedColumns, parse.ReadExpr("label", parse.SQL("upper(tallies.name)")))
	require.NoError(t, err)
	req, err := tallies.ParseList(url.Values{
		filter.ParamFilter:       {`{"label":{"$ne":""}}`},
		filter.ParamSort:         {"label"},
		filter.ParamFacets:       {"label"},
		filter.ParamSearch:       {"x"},
		filter.ParamIncludeTotal: {"true"},
	})
	require.NoError(t, err)
	plan, err := tallies.BuildList(req, parse.Scope{})
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

// A resource compiles under its provider's dialect settings: on two
// PostgreSQL providers one expression orders by each one's collation.
func TestReadExpr_DialectSettingsApplyPerProvider(t *testing.T) {
	email := "(" + parsetest.PgEmailExpr + ")"
	for _, d := range []filter.PostgresDialect{{}, {Collation: filter.CollationC}} {
		_, accounts, _, _ := parsetest.NewComputedResources(providertest.Compile(d))
		req, err := accounts.ParseList(url.Values{filter.ParamSort: {"email"}})
		require.NoError(t, err)
		plan, err := accounts.BuildList(req, parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		assert.Contains(t, plan.Page.SQL, "ORDER BY "+d.CodePointOrder(email)+" ASC")
	}
}

// A dialect an expression refuses fails NewResource on a provider of that
// dialect, naming the table, the column and the dialect. On a provider of
// a dialect it serves, the resource builds every statement, and so do the
// relations that compile its schema.
func TestReadExpr_RefusedDialect(t *testing.T) {
	postgresOnly := func(d filter.Dialect) (string, error) {
		if _, ok := d.(filter.PostgresDialect); !ok {
			return "", filter.ErrUnsupportedDialect
		}
		return parsetest.PgGroupIDsExpr, nil
	}
	_, err := parse.NewResource[parsetest.ComputedAccount](onSQLite, "accounts", parsetest.ComputedAccountOptions(postgresOnly)...)
	require.ErrorIs(t, err, filter.ErrUnsupportedDialect)
	require.ErrorContains(t, err, `parse: accounts: computed column "group_ids" for dialect sqlite`)
	_, err = parse.NewResource[parsetest.ComputedAccount](onSQLite, "accounts", parsetest.ComputedAccountOptions(parse.SQL(" "))...)
	require.ErrorContains(t, err, `parse: accounts: computed column "group_ids" for dialect sqlite: no expression`)

	accounts := parsetest.NewComputedAccounts(parsetest.NewAccessGroups(onPostgres), postgresOnly)
	req, err := accounts.ParseList(url.Values{
		filter.ParamFilter:       {`{"member_count":{"$gte":1}}`},
		filter.ParamFacets:       {"member_count"},
		filter.ParamInclude:      {"groups"},
		filter.ParamIncludeTotal: {"true"},
	})
	require.NoError(t, err)
	scope := parse.Scope{Tenant: "t1"}
	rename, groups := computedPatch(t, accounts, `{"name":"x"}`), computedPatch(t, accounts, `{"groups":[30]}`)
	assert.Contains(t, strings.Join(accounts.Columns(), ", "), "("+parsetest.PgGroupIDsExpr+`) AS "group_ids"`)
	entryPoints := []struct {
		name  string
		build func() error
	}{
		{"BuildList", func() error { _, err := accounts.BuildList(req, scope); return err }},
		{"BuildList of a first page", func() error { _, err := accounts.BuildList(nil, scope); return err }},
		{"BuildUpdate", func() error {
			_, err := accounts.BuildUpdate(rename, parse.ByID(1), scope)
			return err
		}},
		{"BuildUpdate of relations", func() error {
			_, err := accounts.BuildUpdate(groups, parse.ByID(1), scope, parse.SkipUnchanged())
			return err
		}},
		{"BuildUpdateBatch", func() error {
			_, err := accounts.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{1}, Patch: rename}}, scope, parse.UpdateBatchLimits{})
			return err
		}},
	}
	for _, tt := range entryPoints {
		t.Run(tt.name, func(t *testing.T) {
			assert.NoError(t, tt.build())
		})
	}

	tenants := parsetest.NewAccountTenants(accounts)
	for _, tt := range []struct {
		name  string
		query url.Values
		scope parse.Scope
	}{
		{"a relation filter", url.Values{filter.ParamFilter: {`{"accounts":{"$some":{"name":"a"}}}`}}, scope},
		{"a relation include", url.Values{filter.ParamInclude: {"accounts"}}, scope},
		{"a relation facet", url.Values{filter.ParamFacets: {"accounts"}}, scope},
		{"a target policy", url.Values{filter.ParamFilter: {`{"shared_accounts":{"$some":{}}}`}}, parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{
			"accounts": {Constraints: []*filter.PolicyConstraint{{Field: "member_count", Operator: filter.OpGte, Value: 1}}},
		}}},
	} {
		t.Run("owner's "+tt.name, func(t *testing.T) {
			req, err := tenants.ParseList(tt.query)
			require.NoError(t, err)
			_, err = tenants.BuildList(req, tt.scope)
			assert.NoError(t, err)
		})
	}

	t.Run("owner's membership change", func(t *testing.T) {
		groups := parse.MustResource[parsetest.AccessGroup](onPostgres, "access_groups", parse.TenantColumn("tenant_id"))
		groups.MustRelate("accounts", parse.ManyToMany(accounts, parse.Through("account_groups", "group_id", "account_id"), parse.Writable()))
		p, err := groups.ParseMergePatch([]byte(`{"accounts":[1]}`))
		require.NoError(t, err)
		_, err = groups.BuildUpdate(p, parse.ByID(20), scope)
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
			s.Filter.Computed = []filter.ComputedColumn{{Column: "name", SQL: "1"}}
			return nil
		}), "computed columns are declared with ReadExpr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse.NewResource[parsetest.ComputedAccount](onSQLite, "accounts", parsetest.ComputedAccountOptions(parsetest.GroupIDsExpr, tt.opt)...)
			assert.ErrorContains(t, err, tt.want)
		})
	}

	type document struct {
		ID    int            `json:"id"    db:"id"`
		Attrs map[string]any `json:"attrs" db:"attrs" parse:"filter" paths:"color:string"`
	}
	_, err := parse.NewResource[document](onPostgres, "documents", parse.ReadExpr("attrs", parse.SQL("jsonb_build_object('color', 'red')")))
	assert.ErrorContains(t, err, "declares document paths")

	type untagged struct {
		ID    int            `json:"id"    db:"id"`
		Attrs map[string]any `json:"attrs" db:"attrs" parse:"filter"`
	}
	computedDocs, err := parse.NewResource[untagged](onPostgres, "documents", parse.ReadExpr("attrs", parse.SQL("jsonb_build_object('color', 'red')")))
	require.NoError(t, err)
	for _, decl := range []filter.PathDecl{
		{Path: "attrs.color", Type: core.FieldTypeString},
		{Path: "attrs.color", Type: core.FieldTypeString, Sort: true},
	} {
		_, err = computedDocs.WithPaths(decl)
		assert.ErrorContains(t, err, `parse: documents: computed column "attrs": declares document paths, which read a stored document`, "a request's paths as the tags'")
	}
	storedDocs := parse.MustResource[untagged](onPostgres, "documents")
	_, err = storedDocs.WithPaths(filter.PathDecl{Path: "attrs.color", Type: core.FieldTypeString, Sort: true})
	require.NoError(t, err)

	type membership struct {
		ID        int `json:"id"         db:"id"         parse:"filter"`
		AccountID int `json:"account_id" db:"account_id" parse:"filter"`
	}
	memberships := parse.MustResource[membership](onSQLite, "memberships", parse.ReadExpr("account_id", parse.SQL("1")))
	_, accounts, _, _ := parsetest.NewComputedResources(onSQLite)
	err = accounts.Relate("memberships", parse.HasMany(memberships, "account_id"))
	assert.ErrorContains(t, err, `correlates on memberships's computed column "account_id"`)

	type labelledAccount struct {
		ID       int     `json:"id"        db:"id"`
		OwnerKey int     `json:"owner_key" db:"owner_key"`
		Labels   []int64 `json:"labels"    db:"-" parse:"filter" relation:"account_labels,account_key,label_id,owner_key"`
	}
	_, err = parse.NewResource[labelledAccount](onSQLite, "accounts")
	require.NoError(t, err)
	_, err = parse.NewResource[labelledAccount](onSQLite, "accounts", parse.ReadExpr("owner_key", parse.SQL("accounts.id * 10")))
	assert.ErrorContains(t, err, `parse: accounts: relation "labels" correlates on computed column "owner_key"`, "a relation tag as Relate")
}

// A relation's filter compiles its target's computed columns as the
// target's expressions for the owner's dialect: through a has-many, a
// many-to-many and a belongs-to, at depth 1 and 2, and in the target's
// policy (Scope.Related).
func TestReadExpr_RelationFiltersCompileTheTargetsExpressions(t *testing.T) {
	scope := parse.Scope{Tenant: "t1"}
	memberCount, size := "("+parsetest.MemberCountExpr+")", "("+parsetest.GroupSizeExpr+")"
	for _, tt := range computedDialects {
		t.Run(tt.dialect.Name(), func(t *testing.T) {
			groups, _, tenants, memberships := parsetest.NewComputedResources(providertest.Compile(tt.dialect))
			email := "(" + tt.email + ")"
			tests := []struct {
				name, sql, want string
			}{
				{"has-many", pageSQL(t, tenants, `{"accounts":{"$some":{"email":""}}}`, scope), email + " = "},
				{"many-to-many", pageSQL(t, groups, `{"accounts":{"$some":{"member_count":{"$gte":2}}}}`, scope), memberCount + " >= "},
				{"belongs-to", pageSQL(t, memberships, `{"account":{"$is":{"email":""}}}`, scope), email + " = "},
				{"has-many, then many-to-many", pageSQL(t, tenants, `{"accounts":{"$some":{"groups":{"$some":{"size":2}}}}}`, scope), size + " = "},
				{"belongs-to, then many-to-many", pageSQL(t, memberships, `{"account":{"$is_not":{"groups":{"$some":{"size":2}}}}}`, scope), size + " = "},
				{"the target's policy", pageSQL(t, tenants, `{"shared_accounts":{"$some":{"name":"a"}}}`, parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{
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
			assert.Equal(t, `"accounts"."email"`, filter.FieldExpr(filter.PostgresDialect{}, tenants.FilterSchema().Fields["accounts"].Related.Fields["email"]), "the relation's schema names the column")
		})
	}
}

// Rebinding walks the filter, not the relation graph: around the
// groups–accounts cycle it ends where the filter does. Back at groups the
// subquery aliases the table, which a stored column follows and a computed
// column's expression, naming the table, cannot.
func TestReadExpr_RelationCycles(t *testing.T) {
	scope := parse.Scope{Tenant: "t1"}
	for _, tt := range computedDialects {
		groups, _, _, _ := parsetest.NewComputedResources(providertest.Compile(tt.dialect))
		sql := pageSQL(t, groups, `{"accounts":{"$some":{"email":"","groups":{"$some":{"id":20}}}}}`, scope)
		assert.Contains(t, sql, `"access_groups_rel2__"."id" = `)
		assert.Contains(t, sql, "("+tt.email+") = ")

		req, err := groups.ParseList(url.Values{filter.ParamFilter: {`{"accounts":{"$some":{"groups":{"$some":{"size":2}}}}}`}})
		require.NoError(t, err)
		_, err = groups.BuildList(req, scope)
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
	provider, db := providertest.SQLite(t)
	dbtest.Exec(t, db, parsetest.ComputedAccountFixture()...)
	_, accounts, tenants, _ := parsetest.NewComputedResources(provider)
	req, err := tenants.ParseList(url.Values{filter.ParamInclude: {"shared_accounts"}})
	require.NoError(t, err)
	plan, err := tenants.BuildList(req, scope)
	require.NoError(t, err)
	_, err = tenants.IncludeQueries(plan, []parse.Key{{"t1"}})
	assert.ErrorContains(t, err, "field member_count is not a column of accounts and cannot be filtered through a relation")

	// A membership change holds its pairs to the targets the same policy
	// admits, once the owner statement has returned the group, and so
	// writes nothing.
	groups := parsetest.NewAccessGroups(provider)
	groups.MustRelate("accounts", parse.ManyToMany(accounts, parse.Through("account_groups", "group_id", "account_id"), parse.Writable(), parse.PolicyScoped()))
	p, err := groups.ParseMergePatch([]byte(`{"accounts":[1]}`))
	require.NoError(t, err)
	_, err = groups.Update(t.Context(), p, parse.ByID(20), scope)
	assert.ErrorContains(t, err, "field member_count is not a column of accounts and cannot be filtered through a relation")
	assert.Equal(t, []string{"1:10", "1:20", "2:20", "3:30"}, dbtest.QueryRows[string](t, db, `SELECT account_id || ':' || group_id FROM account_groups ORDER BY 1`))
}

// pageSQL is the page statement r builds for filterJSON.
func pageSQL[T any](t *testing.T, r *parse.Resource[T], filterJSON string, scope parse.Scope) string {
	t.Helper()
	req, err := r.ParseList(url.Values{filter.ParamFilter: {filterJSON}})
	require.NoError(t, err)
	plan, err := r.BuildList(req, scope)
	require.NoError(t, err)
	return plan.Page.SQL
}

// An update's stamp and counters write the table's own columns, which a
// computed column is not.
func TestReadExpr_UpdatesNeitherStampNorIncrementComputedColumns(t *testing.T) {
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
				_, accounts, _, _ := parsetest.NewComputedResources(providertest.Compile(d.dialect))
				rename := computedPatch(t, accounts, `{"name":"x"}`)
				_, err := accounts.BuildUpdate(rename, parse.ByID(1), scope, tt.opt)
				assert.ErrorContains(t, err, tt.want, "BuildUpdate %s", d.dialect.Name())
				_, err = accounts.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{1}, Patch: rename}}, scope, parse.UpdateBatchLimits{}, tt.opt)
				assert.ErrorContains(t, err, tt.want, "BuildUpdateBatch %s", d.dialect.Name())
			}
		})
	}
}

// An update reads its owners again only when its relation statements may
// have changed what a computed column reads: after them, in its
// transaction, held to the update's tenant.
func TestReadExpr_UpdateRereadsAfterRelationChanges(t *testing.T) {
	db := dbtest.SQLiteHooked(t)
	provider := providertest.SQLiteOn(t, db)
	dbtest.Exec(t, db, parsetest.ComputedAccountFixture()...)
	_, accounts, _, _ := parsetest.NewComputedResources(provider)
	scope := parse.Scope{Tenant: "t1"}
	var statements []string
	ctx := dbtest.OnStatement(t.Context(), func(_ context.Context, query string) { statements = append(statements, query) })
	reread := "SELECT " + strings.Join(accounts.Columns(), ", ") + ` FROM "accounts" WHERE `

	_, err := accounts.Update(ctx, computedPatch(t, accounts, `{"name":"x"}`), parse.ByID(1), scope)
	require.NoError(t, err)
	require.NotEmpty(t, statements)
	for _, query := range statements {
		assert.False(t, strings.HasPrefix(query, reread), "no relation statements: Owner's rows are current: %s", query)
	}

	statements = nil
	res, err := accounts.Update(ctx, computedPatch(t, accounts, `{"groups":[30]}`), parse.ByID(1), scope)
	require.NoError(t, err)
	last := statements[len(statements)-1]
	assert.True(t, strings.HasPrefix(last, reread), "the reread follows the relation statements: %s", last)
	assert.Contains(t, last, `"accounts"."tenant_id" = `)
	require.Len(t, res.Unchanged, 1)
	assert.Equal(t, []int64{30}, res.Unchanged[0].GroupIDs, "the pairs the relation statements left")
}

// A server policy names a computed column requests cannot filter by — a
// hidden one, by its column — and compiles it as the dialect's expression:
// on the resource's own lists and updates, and as a relation's target
// (Scope.Related).
func TestReadExpr_PolicyFieldsCompileAgainstEachDialectsExpression(t *testing.T) {
	contact := &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "contact", Operator: filter.OpEq, Value: "b"}}}
	for _, tt := range computedDialects {
		t.Run(tt.dialect.Name(), func(t *testing.T) {
			_, accounts, tenants, _ := parsetest.NewComputedResources(providertest.Compile(tt.dialect))
			_, err := accounts.ParseList(url.Values{filter.ParamFilter: {`{"contact":"b"}`}})
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe, "requests cannot filter by it")
			assert.Equal(t, filter.ECodeUnknownField.String(), pe.Code)

			expr := "(" + tt.contact + ") = "
			plan, err := accounts.BuildList(nil, parse.Scope{Tenant: "t1", Policy: contact})
			require.NoError(t, err)
			assert.Contains(t, plan.Page.SQL, expr)

			update, err := accounts.BuildUpdate(computedPatch(t, accounts, `{"name":"x"}`), parse.ByID(2), parse.Scope{Tenant: "t1", Policy: contact})
			require.NoError(t, err)
			assert.Contains(t, update.Owner.SQL, expr)

			sql := pageSQL(t, tenants, `{"shared_accounts":{"$some":{}}}`, parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{"accounts": contact}})
			assert.Contains(t, sql, expr, "the target's policy")

			for _, other := range computedDialects {
				if other.dialect.Name() != tt.dialect.Name() {
					assert.NotContains(t, plan.Page.SQL+update.Owner.SQL+sql, other.contact)
				}
			}

			field, ok, err := accounts.FilterSchema().PolicyField("contact")
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, `"accounts"."contact"`, filter.FieldExpr(filter.PostgresDialect{}, field), "the schema names the column")
		})
	}
}

func computedPatch(t *testing.T, accounts *parse.Resource[parsetest.ComputedAccount], body string) *patch.ParsedPatch {
	t.Helper()
	p, err := accounts.ParseMergePatch([]byte(body))
	require.NoError(t, err)
	return p
}
