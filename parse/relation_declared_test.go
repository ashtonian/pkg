//go:build unit

package parse_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestDeclaredRelations_SQLite(t *testing.T) {
	db := dbtest.SQLite(t)
	for _, stmt := range relationFixtureSQL(`'2026-01-01T00:00:00Z'`, "TEXT") {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	runRelationCases(t, relationResources(t), filter.SQLiteDialect{}, func(t *testing.T, st parse.Statement) []string {
		rows, err := db.Query(st.SQL, st.Args...)
		require.NoError(t, err, st.SQL)
		var ids []string
		for rows.Next() {
			cols, err := rows.Columns()
			require.NoError(t, err)
			vals := make([]any, len(cols))
			for i := range vals {
				vals[i] = new(any)
			}
			require.NoError(t, rows.Scan(vals...))
			value, pointerOK := vals[0].(*any)
			require.True(t, pointerOK)
			id, stringOK := (*value).(string)
			require.True(t, stringOK)
			ids = append(ids, id)
		}
		require.NoError(t, errors.Join(rows.Err(), rows.Close()))
		return ids
	})
}

func TestDeclaredRelations_Includes_SQLite(t *testing.T) {
	db := dbtest.SQLite(t)
	for _, stmt := range relationFixtureSQL(`'2026-01-01T00:00:00Z'`, "TEXT") {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	runRelationIncludes(t, relationResources(t), filter.SQLiteDialect{}, func(t *testing.T, q string, args []any) [][]any {
		return queryRows(t, db, q, args)
	})
}

func TestDeclaredRelations_Facets_SQLite(t *testing.T) {
	db := dbtest.SQLite(t)
	for _, stmt := range relationFixtureSQL(`'2026-01-01T00:00:00Z'`, "TEXT") {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	runRelationFacets(t, relationResources(t), filter.SQLiteDialect{}, func(t *testing.T, q string, args []any) [][]any {
		return queryRows(t, db, q, args)
	})
}

func TestDeclaredRelations_Writes_SQLite(t *testing.T) {
	db := dbtest.SQLite(t)
	for _, stmt := range relationFixtureSQL(`'2026-01-01T00:00:00Z'`, "TEXT") {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	runRelationWrites(t, relationResources(t), filter.SQLiteDialect{}, func(t *testing.T, q string, args []any) [][]any {
		return queryRows(t, db, q, args)
	})
}

func TestElementPredicates_SQLite(t *testing.T) {
	db := dbtest.SQLite(t)
	for _, stmt := range orderFixtureSQL("TEXT") {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	runElementCases(t, filter.SQLiteDialect{}, func(t *testing.T, q string, args []any) [][]any {
		return queryRows(t, db, q, args)
	})
}

func TestDeclaredRelations_Memberships_SQLite(t *testing.T) {
	db := dbtest.SQLite(t)
	for _, stmt := range relationFixtureSQL(`'2026-01-01T00:00:00Z'`, "TEXT") {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	runRelationMemberships(t, relationResources(t), filter.SQLiteDialect{}, func(t *testing.T, q string, args []any) [][]any {
		return queryRows(t, db, q, args)
	})
}

// One `$some` is one correlated EXISTS over the target's live rows in the
// tenant, its predicates on one aliased row.
func TestDeclaredRelations_SQL(t *testing.T) {
	r := relationResources(t)
	plan := relationPlan(t, r.devices, `{"commands":{"$some":{"status":"pending","attempts":{"$gt":2}}}}`, relationScopeT1())
	assert.Contains(t, plan.Page.SQL, "EXISTS (SELECT 1 FROM commands WHERE commands.device_id = devices.id AND commands.tenant_id = $1 AND commands.deleted_at IS NULL AND (commands.attempts > $2 AND commands.status = $3))")

	plan = relationPlan(t, r.devices, `{"tags":{"$some":{"label":"red"}}}`, relationScopeT1())
	assert.Contains(t, plan.Page.SQL, "EXISTS (SELECT 1 FROM device_tags rel1__ JOIN tags ON tags.id = rel1__.tag_id WHERE rel1__.device_id = devices.id AND rel1__.tenant_id = $1 AND tags.tenant_id = $2 AND tags.deleted_at IS NULL AND tags.label = $3)")

	assets := relationPlan(t, r.assets, `{"parent":{"$is":{"name":"root"}}}`, parse.Scope{Tenant: "t1"})
	assert.Contains(t, assets.Page.SQL, "EXISTS (SELECT 1 FROM assets assets_rel1__ WHERE assets_rel1__.id = assets.parent_id AND assets_rel1__.tenant_id = $1 AND assets_rel1__.name = $2)", "the target is aliased away from its owner")
}

func relationPlan(t *testing.T, r interface {
	ParseList(url.Values) (*parse.ListRequest, error)
}, f string, s parse.Scope) *parse.ListPlan {
	t.Helper()
	req, err := r.ParseList(url.Values{filter.ParamFilter: {f}})
	require.NoError(t, err)
	switch res := r.(type) {
	case *parse.Resource[relDevice]:
		plan, err := res.BuildList(filter.PostgresDialect{}, req, s)
		require.NoError(t, err)
		return plan
	case *parse.Resource[relAsset]:
		plan, err := res.BuildList(filter.PostgresDialect{}, req, s)
		require.NoError(t, err)
		return plan
	}
	t.Fatalf("unexpected resource %T", r)
	return nil
}

func TestDeclaredRelations_Refuse(t *testing.T) {
	r := relationResources(t)
	for _, tc := range []struct {
		name, filter string
		code         string
	}{
		{"an unknown target field", `{"commands":{"$some":{"colour":"red"}}}`, filter.ECodeUnknownField.String()},
		{"$is on a collection", `{"commands":{"$is":{}}}`, filter.ECodeInvalidOperator.String()},
		{"$some on a belongs-to", `{"type":{"$some":{}}}`, filter.ECodeInvalidOperator.String()},
		{"a non-object target filter", `{"commands":{"$some":"c1"}}`, filter.ECodeInvalidJSON.String()},
		{"three levels deep", `{"commands":{"$some":{"device":{"$is":{"commands":{"$some":{}}}}}}}`, filter.ECodeDepthExceeded.String()},
		{"nine relation predicates", `{"$and":[` + strings.Repeat(`{"commands":{"$some":{}}},`, 8) + `{"commands":{"$some":{}}}]}`, filter.ECodeMaxConditions.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.devices.ParseList(url.Values{filter.ParamFilter: {tc.filter}})
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, tc.code, pe.Code, pe.Message)
		})
	}

	req, err := r.devices.ParseList(url.Values{filter.ParamFilter: {`{"type":{"$is":{}}}`}})
	require.NoError(t, err)
	_, err = r.devices.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: "t1"})
	assert.ErrorIs(t, err, filter.ErrRelatedPolicyRequired, "a policy-scoped relation needs the target's policy")
}

func TestRelate_RefusesBadDeclarations(t *testing.T) {
	commands := parse.MustResource[relCommand]("commands", parse.TenantColumn("tenant_id"))
	for name, tc := range map[string]struct {
		relName string
		rel     parse.Relation
	}{
		"unknown foreign key":     {"commands", parse.HasMany(commands, "devise_id")},
		"unknown owner column":    {"commands", parse.HasMany(commands, "device_id", parse.References("nope"))},
		"unknown belongs-to key":  {"command", parse.BelongsTo(commands, "type_id", parse.References("nope"))},
		"existing field":          {"name", parse.HasMany(commands, "device_id")},
		"system field":            {"tenant_id", parse.HasMany(commands, "device_id")},
		"not an identifier":       {"my commands", parse.HasMany(commands, "device_id")},
		"pair table identifiers":  {"tags", parse.ManyToMany(commands, parse.Through("pairs", "device id", "tag_id"))},
		"references on a pairing": {"tags", parse.ManyToMany(commands, parse.Through("pairs", "device_id", "tag_id"), parse.References("id"))},
	} {
		t.Run(name, func(t *testing.T) {
			devices := parse.MustResource[relDevice]("devices", parse.TenantColumn("tenant_id"))
			assert.Error(t, devices.Relate(tc.relName, tc.rel))
		})
	}
}

// A key comparison that reaches the builder without the parser — a policy
// constraint naming the relation — still tests only visible related rows,
// and flat parameters are parsed as the JSON filter is.
func TestDeclaredRelations_EveryEntryReachesOnlyVisibleRows(t *testing.T) {
	r := relationResources(t)
	s := relationScopeT1()
	s.Policy = &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "commands", Operator: filter.OpEq, Value: "c4"}}}
	plan, err := r.devices.BuildList(filter.PostgresDialect{}, nil, s)
	require.NoError(t, err)
	assert.Contains(t, plan.Page.SQL, "commands.deleted_at IS NULL", "the policy's key comparison is a relation predicate")

	req, err := r.devices.ParseList(url.Values{"commands": {"c1"}})
	require.NoError(t, err)
	plan, err = r.devices.BuildList(filter.PostgresDialect{}, req, s)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(plan.Page.SQL, "commands.deleted_at IS NULL"), plan.Page.SQL)
}

// A relation's label and description reach schema discovery as a column's
// tags do.
func TestRelate_DescribeReachesDiscovery(t *testing.T) {
	commands := parse.MustResource[relCommand]("commands", parse.TenantColumn("tenant_id"))
	devices := parse.MustResource[relDevice]("devices", parse.TenantColumn("tenant_id"))
	devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Describe("Commands", "Commands sent to the device")))
	reg := parse.NewSchemaRegistry()
	parse.RegisterResource(reg, devices)

	f := reg.SchemaFor("devices").Filter.Fields["commands"]
	require.NotNil(t, f)
	assert.Equal(t, "Commands", f.Label)
	assert.Equal(t, "Commands sent to the device", f.Description)
}
