//go:build unit || integration

package parse_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"slices"
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

// Declared relations over one fixture: devices have commands
// (soft-deleted, tenant-scoped), commands have receipts, devices belong to a
// device type (platform types shared, tenant types private: policy-scoped),
// devices and tags pair through device_tags, and assets belong to a parent
// asset (a relation back to its own table).

type relDevice struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,sort" indexed:"true"`
	TypeID    *string    `json:"type_id"    db:"type_id"    parse:"filter"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	// Zones is a pair table compared by key (the relation tag).
	Zones []string `json:"-" db:"-" parse:"filter" relation_name:"zones" relation:"device_zones,device_id,zone_id,id,tenant_id" indexed:"true"`
}

type relCommand struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	DeviceID  string     `json:"device_id"  db:"device_id"  parse:"filter"`
	Status    string     `json:"status"     db:"status"     parse:"filter"`
	Attempts  int64      `json:"attempts"   db:"attempts"   parse:"filter"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
}

type relReceipt struct {
	ID        string `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string `json:"tenant_id"  db:"tenant_id"`
	CommandID string `json:"command_id" db:"command_id" parse:"filter"`
	OK        bool   `json:"ok"         db:"ok"         parse:"filter"`
}

type relType struct {
	ID       string  `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID *string `json:"tenant_id" db:"tenant_id" parse:"filter"`
	Name     string  `json:"name"      db:"name"      parse:"filter"`
}

type relTag struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Label     string     `json:"label"      db:"label"      parse:"filter"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
}

type relAsset struct {
	ID       string  `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string  `json:"tenant_id" db:"tenant_id"`
	Name     string  `json:"name"      db:"name"      parse:"filter"`
	ParentID *string `json:"parent_id" db:"parent_id" parse:"filter"`
}

type relResources struct {
	devices *parse.Resource[relDevice]
	assets  *parse.Resource[relAsset]
}

func relationResources(t *testing.T) relResources {
	t.Helper()
	devices := parse.MustResource[relDevice]("devices", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	commands := parse.MustResource[relCommand]("commands", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	receipts := parse.MustResource[relReceipt]("receipts", parse.TenantColumn("tenant_id"))
	types := parse.MustResource[relType]("device_types", parse.TenantColumn("tenant_id"))
	tags := parse.MustResource[relTag]("tags", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	assets := parse.MustResource[relAsset]("assets", parse.TenantColumn("tenant_id"))

	devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed(), parse.Includable(), parse.Facetable()))
	devices.MustRelate("type", parse.BelongsTo(types, "type_id", parse.PolicyScoped()))
	devices.MustRelate("tags", parse.ManyToMany(tags, parse.Junction{Table: "device_tags", Source: "device_id", Target: "tag_id", Tenant: "tenant_id"}, parse.Indexed(), parse.Includable(), parse.Facetable(), parse.Writable()))
	devices.MustRelate("shared_types", parse.ManyToMany(types, parse.Through("device_shared_types", "device_id", "type_id"), parse.PolicyScoped(), parse.Includable(), parse.Facetable(), parse.Writable()))
	commands.MustRelate("device", parse.BelongsTo(devices, "device_id"))
	commands.MustRelate("receipts", parse.HasMany(receipts, "command_id", parse.Indexed()))
	assets.MustRelate("parent", parse.BelongsTo(assets, "parent_id"))
	assets.MustRelate("children", parse.HasMany(assets, "parent_id", parse.Indexed()))
	return relResources{devices: devices, assets: assets}
}

// relationFixtureSQL seeds both dialects; deletedAt is the dialect's
// timestamp literal.
func relationFixtureSQL(deletedAt string, timestampType string) []string {
	return []string{
		`CREATE TABLE devices (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT NOT NULL, type_id TEXT, deleted_at ` + timestampType + `)`,
		`CREATE TABLE commands (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, device_id TEXT, status TEXT NOT NULL, attempts INTEGER NOT NULL, deleted_at ` + timestampType + `)`,
		`CREATE TABLE receipts (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, command_id TEXT NOT NULL, ok BOOLEAN NOT NULL)`,
		`CREATE TABLE device_types (id TEXT PRIMARY KEY, tenant_id TEXT, name TEXT NOT NULL)`,
		`CREATE TABLE tags (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, label TEXT NOT NULL, deleted_at ` + timestampType + `)`,
		`CREATE TABLE device_tags (device_id TEXT NOT NULL, tag_id TEXT NOT NULL, tenant_id TEXT NOT NULL, PRIMARY KEY (device_id, tag_id, tenant_id))`,
		`CREATE TABLE assets (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT NOT NULL, parent_id TEXT)`,
		`CREATE TABLE device_zones (device_id TEXT NOT NULL, zone_id TEXT NOT NULL, tenant_id TEXT NOT NULL)`,
		`CREATE TABLE device_shared_types (device_id TEXT NOT NULL, type_id TEXT NOT NULL, PRIMARY KEY (device_id, type_id))`,
		`INSERT INTO device_shared_types VALUES ('d1','pt'), ('d1','t2x'), ('d2','t1x')`,
		`INSERT INTO device_zones VALUES ('d1','z1','t1'), ('d2','z2','t1'), ('d3','z1','t2')`,
		// t1: d1 pending/1 + failed/5; d2 pending/3; d3 only a deleted pending/9; d4 nothing.
		// t2's c9 names t1's d4.
		`INSERT INTO devices VALUES ('d1','t1','pump','pt',NULL), ('d2','t1','fan','t1x',NULL), ('d3','t1','valve','t2x',NULL),
			('d4','t1','spare',NULL,NULL), ('e1','t2','other','pt',NULL)`,
		`INSERT INTO commands VALUES ('c1','t1','d1','pending',1,NULL), ('c2','t1','d1','failed',5,NULL), ('c3','t1','d2','pending',3,NULL),
			('c4','t1','d3','pending',9,` + deletedAt + `), ('c9','t2','d4','pending',7,NULL)`,
		`INSERT INTO receipts VALUES ('r1','t1','c1',true), ('r2','t1','c3',false)`,
		// pt is a platform type, t1x t1's own, t2x t2's.
		`INSERT INTO device_types VALUES ('pt',NULL,'pump-type'), ('t1x','t1','fan-type'), ('t2x','t2','valve-type')`,
		`INSERT INTO tags VALUES ('red','t1','red',NULL), ('old','t1','old',` + deletedAt + `), ('t2red','t2','red',NULL)`,
		`INSERT INTO device_tags VALUES ('d1','red','t1'), ('d2','old','t1'), ('d3','t2red','t1'), ('d4','red','t2')`,
		`INSERT INTO assets VALUES ('a1','t1','root',NULL), ('a2','t1','leaf','a1'), ('a3','t1','orphan',NULL)`,
	}
}

// relationScopeT1 is t1's scope: device types it may see are its own and
// the platform's.
func relationScopeT1() parse.Scope {
	return parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{
		"device_types": {Or: []*filter.PolicyConstraints{
			{Constraints: []*filter.PolicyConstraint{{Field: "tenant_id", Operator: filter.OpEq, Value: "t1"}}},
			{Constraints: []*filter.PolicyConstraint{{Field: "tenant_id", Operator: filter.OpNull}}},
		}},
	}}
}

type relationCase struct {
	name   string
	assets bool // the query is the assets resource's
	filter string
	want   []string
}

var relationCases = []relationCase{
	{name: "some: every predicate on one command", filter: `{"commands":{"$some":{"status":"pending","attempts":{"$gt":2}}}}`, want: []string{"d2"}},
	{name: "some: any visible command", filter: `{"commands":{"$some":{}}}`, want: []string{"d1", "d2"}},
	{name: "none: no failed command, devices without commands included", filter: `{"commands":{"$none":{"status":"failed"}}}`, want: []string{"d2", "d3", "d4"}},
	{name: "a deleted command is not related", filter: `{"commands":{"$some":{"attempts":{"$gte":9}}}}`, want: nil},
	{name: "null: no visible command", filter: `{"commands":{"$null":true}}`, want: []string{"d3", "d4"}},
	{name: "another tenant's command is not related", filter: `{"commands":"c9"}`, want: nil},
	{name: "id shorthand", filter: `{"commands":"c1"}`, want: []string{"d1"}},
	{name: "id list shorthand", filter: `{"commands":{"$in":["c1","c3"]}}`, want: []string{"d1", "d2"}},
	{name: "ne shorthand is none", filter: `{"commands":{"$ne":"c1"}}`, want: []string{"d2", "d3", "d4"}},
	{name: "depth two", filter: `{"commands":{"$some":{"receipts":{"$some":{"ok":false}}}}}`, want: []string{"d2"}},
	{name: "belongs-to is, through the target's policy", filter: `{"type":{"$is":{"name":{"$like":"%-type"}}}}`, want: []string{"d1", "d2"}},
	{name: "belongs-to is_not includes devices without a visible type", filter: `{"type":{"$is_not":{"name":"pump-type"}}}`, want: []string{"d2", "d3", "d4"}},
	{name: "belongs-to null: no visible type", filter: `{"type":{"$null":true}}`, want: []string{"d3", "d4"}},
	{name: "many-to-many through the pair table, target scoped", filter: `{"tags":{"$some":{"label":"red"}}}`, want: []string{"d1"}},
	{name: "many-to-many id shorthand skips deleted targets", filter: `{"tags":"old"}`, want: nil},
	{name: "combined with column filters", filter: `{"$or":[{"name":"spare"},{"commands":{"$some":{"status":"failed"}}}]}`, want: []string{"d1", "d4"}},
	{name: "pair table by key", filter: `{"zones":"z1"}`, want: []string{"d1"}},
	{name: "pair table by key, negated", filter: `{"zones":{"$nin":["z1"]}}`, want: []string{"d2", "d3", "d4"}},
	{name: "pair table without pairs", filter: `{"zones":{"$null":true}}`, want: []string{"d3", "d4"}},
	{name: "relation back to its own table", assets: true, filter: `{"parent":{"$is":{"name":"root"}}}`, want: []string{"a2"}},
	{name: "children of its own table", assets: true, filter: `{"children":{"$some":{"name":"leaf"}}}`, want: []string{"a1"}},
}

// runRelationCases lists each case through BuildList on query.
func runRelationCases(t *testing.T, r relResources, d filter.Dialect, query func(t *testing.T, st parse.Statement) []string) {
	t.Helper()
	for _, tc := range relationCases {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{filter.ParamFilter: {tc.filter}, filter.ParamPageSize: {"50"}}
			var plan *parse.ListPlan
			if tc.assets {
				req, err := r.assets.ParseList(q)
				require.NoError(t, err)
				plan, err = r.assets.BuildList(d, req, parse.Scope{Tenant: "t1"})
				require.NoError(t, err)
			} else {
				req, err := r.devices.ParseList(q)
				require.NoError(t, err)
				plan, err = r.devices.BuildList(d, req, relationScopeT1())
				require.NoError(t, err)
			}
			got := query(t, plan.Page)
			slices.Sort(got)
			assert.Equal(t, tc.want, got, plan.Page.SQL)
		})
	}
}

// runRelationMemberships answers each case the way a memory backend does:
// filter.ResolveMemberships turns its relation predicates into memberships of
// the owner's correlation field, here answered by the database itself, and
// the resolved filter — comparisons on the owner's columns only — must select
// the rows the EXISTS predicates select.
func runRelationMemberships(t *testing.T, r relResources, d filter.Dialect, query func(t *testing.T, sql string, args []any) [][]any) {
	t.Helper()
	lookup := sqlMembershipLookup{t: t, d: d, query: query}
	for _, tc := range relationCases {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{filter.ParamFilter: {tc.filter}, filter.ParamPageSize: {"50"}}
			scope := relationScopeT1()
			var plan *parse.ListPlan
			if tc.assets {
				scope = parse.Scope{Tenant: "t1"}
				req, err := r.assets.ParseList(q)
				require.NoError(t, err)
				req.Filter, err = filter.ResolveMemberships(context.Background(), req.Filter, lookup,
					filter.ResolveTenant(scope.Tenant), filter.ResolveRelatedPolicies(scope.Related))
				require.NoError(t, err)
				plan, err = r.assets.BuildList(d, req, scope)
				require.NoError(t, err)
			} else {
				req, err := r.devices.ParseList(q)
				require.NoError(t, err)
				req.Filter, err = filter.ResolveMemberships(context.Background(), req.Filter, lookup,
					filter.ResolveTenant(scope.Tenant), filter.ResolveRelatedPolicies(scope.Related))
				require.NoError(t, err)
				plan, err = r.devices.BuildList(d, req, scope)
				require.NoError(t, err)
			}
			assert.NotContains(t, plan.Page.SQL, "EXISTS", "every relation predicate is resolved")
			var got []string
			for _, row := range query(t, plan.Page.SQL, plan.Page.Args) {
				id, ok := row[0].(string)
				require.True(t, ok)
				got = append(got, id)
			}
			slices.Sort(got)
			assert.Equal(t, tc.want, got, plan.Page.SQL)
		})
	}

	_, err := filter.ResolveMemberships(context.Background(), relationFilter(t, r, `{"commands":"c1"}`), keysOnlyLookup{lookup}, filter.ResolveTenant("t1"))
	assert.ErrorContains(t, err, "cannot evaluate filters", "a store without filter evaluation refuses rather than over-matches")
	_, err = filter.ResolveMemberships(context.Background(), relationFilter(t, r, `{"commands":"c1"}`), nil)
	assert.ErrorContains(t, err, "needs a membership lookup", "a backend without a lookup refuses a relation predicate")
	_, err = filter.ResolveMemberships(context.Background(), relationFilter(t, r, `{"commands":"c1"}`), lookup)
	assert.ErrorContains(t, err, "tenant-scoped", "a tenant-scoped relation needs the tenant")
	_, err = filter.ResolveMemberships(context.Background(), relationFilter(t, r, `{"type":{"$is":{}}}`), lookup, filter.ResolveTenant("t1"))
	assert.ErrorIs(t, err, filter.ErrRelatedPolicyRequired)
}

func relationFilter(t *testing.T, r relResources, f string) *filter.ParsedFilter {
	t.Helper()
	req, err := r.devices.ParseList(url.Values{filter.ParamFilter: {f}})
	require.NoError(t, err)
	return req.Filter
}

// sqlMembershipLookup answers a lookup with the subquery SQL renders for it.
type sqlMembershipLookup struct {
	t     *testing.T
	d     filter.Dialect
	query func(t *testing.T, sql string, args []any) [][]any
}

func (l sqlMembershipLookup) LookupFilteredValues(ctx context.Context, lk *filter.Lookup) ([]any, error) {
	return l.LookupValues(ctx, lk)
}

func (l sqlMembershipLookup) LookupValues(_ context.Context, lk *filter.Lookup) ([]any, error) {
	probe := &filter.FieldSchema{ExternalName: "probe", SQLExpr: "probe", DataType: core.FieldTypeString}
	res, err := filter.NewSQLBuilder(&filter.ResourceSchema{Name: "probe"}, l.d).
		Build(&filter.FilterAST{Root: &filter.FilterNode{Field: probe, Op: filter.OpMember, Member: lk}})
	if err != nil {
		return nil, err
	}
	sub := strings.TrimSuffix(strings.TrimPrefix(res.Where, "probe IN ("), ")")
	var out []any
	for _, row := range l.query(l.t, sub, res.Args) {
		out = append(out, row[0])
	}
	return out, nil
}

func queryRows(t *testing.T, db interface {
	Query(string, ...any) (*sql.Rows, error)
}, q string, args []any) [][]any {
	t.Helper()
	rows, err := db.Query(q, args...)
	require.NoError(t, err, q)
	var out [][]any
	for rows.Next() {
		cols, err := rows.Columns()
		require.NoError(t, err)
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		out = append(out, vals)
	}
	require.NoError(t, errors.Join(rows.Err(), rows.Close()))
	return out
}

// keysOnlyLookup answers unfiltered lookups only, as a store that predates
// relation filters does.
type keysOnlyLookup struct{ l sqlMembershipLookup }

func (k keysOnlyLookup) LookupValues(ctx context.Context, lk *filter.Lookup) ([]any, error) {
	return k.l.LookupValues(ctx, lk)
}

// runRelationIncludes lists t1's devices including their commands, tags and
// shared types: only related rows the request may see, and none for an
// owner without them.
func runRelationIncludes(t *testing.T, r relResources, d filter.Dialect, query func(t *testing.T, sql string, args []any) [][]any) {
	t.Helper()
	req, err := r.devices.ParseList(url.Values{filter.ParamInclude: {"commands,tags,shared_types"}, filter.ParamPageSize: {"50"}})
	require.NoError(t, err)
	plan, err := r.devices.BuildList(d, req, relationScopeT1())
	require.NoError(t, err)
	queries, err := r.devices.IncludeQueries(plan, []any{"d1", "d2", "d3", "d4"})
	require.NoError(t, err)
	require.Len(t, queries, 3)
	got := map[string]map[string][]string{}
	for _, q := range queries {
		ids := map[string][]string{}
		for _, row := range query(t, q.SQL, q.Args) {
			owner, ownerOK := row[0].(string)
			target, targetOK := row[1].(string)
			require.True(t, ownerOK)
			require.True(t, targetOK)
			ids[owner] = append(ids[owner], target)
		}
		got[q.Field] = ids
	}
	assert.Equal(t, map[string]map[string][]string{
		"commands":     {"d1": {"c1", "c2"}, "d2": {"c3"}}, // d3's is deleted, d4's another tenant's
		"tags":         {"d1": {"red"}},                    // d2's tag is deleted, d3's another tenant's
		"shared_types": {"d1": {"pt"}, "d2": {"t1x"}},      // t2x is t2's own type
	}, got)

	_, err = r.devices.ParseList(url.Values{filter.ParamInclude: {"type"}})
	assert.Error(t, err, "a belongs-to is not includable")
	plan, err = r.devices.BuildList(d, req, parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	_, err = r.devices.IncludeQueries(plan, []any{"d1"})
	assert.ErrorIs(t, err, filter.ErrRelatedPolicyRequired, "a policy-scoped include needs the target's policy")
}

// runRelationFacets counts t1's devices per visible related key.
func runRelationFacets(t *testing.T, r relResources, d filter.Dialect, query func(t *testing.T, sql string, args []any) [][]any) {
	t.Helper()
	req, err := r.devices.ParseList(url.Values{filter.ParamFacets: {"commands,tags,shared_types"}})
	require.NoError(t, err)
	plan, err := r.devices.BuildList(d, req, relationScopeT1())
	require.NoError(t, err)
	require.NotNil(t, plan.Facets)
	got := map[string]map[string]int64{}
	for _, row := range query(t, plan.Facets.SQL, plan.Facets.Args) {
		field, fieldOK := row[0].(string)
		key, keyOK := row[1].(string)
		require.True(t, fieldOK)
		require.True(t, keyOK)
		if got[field] == nil {
			got[field] = map[string]int64{}
		}
		switch n := row[2].(type) {
		case int64:
			got[field][key] = n
		case int32:
			got[field][key] = int64(n)
		}
	}
	assert.Equal(t, map[string]map[string]int64{
		"commands":     {"c1": 1, "c2": 1, "c3": 1},
		"tags":         {"red": 1},
		"shared_types": {"pt": 1, "t1x": 1},
	}, got)
}

// runRelationWrites patches t1's devices' memberships: pairs are made only
// with targets the request may relate (live, in the tenant, or admitted by
// the target's policy), and a replace or remove reaches only pairs to such
// targets, so pairs the request cannot see stay.
func runRelationWrites(t *testing.T, r relResources, d parse.Dialect, query func(t *testing.T, sql string, args []any) [][]any) {
	t.Helper()
	apply := func(id, body string, merge bool) {
		t.Helper()
		var p *patch.ParsedPatch
		var err error
		if merge {
			p, err = r.devices.ParseMergePatch([]byte(body))
		} else {
			p, err = r.devices.ParsePatch([]byte(body))
		}
		require.NoError(t, err, body)
		plan, err := r.devices.BuildUpdate(d, p, parse.ByID(id), relationScopeT1())
		require.NoError(t, err)
		owners := query(t, plan.Owner.SQL, plan.Owner.Args)
		keys := make([]any, len(owners))
		for i, o := range owners {
			keys[i] = o[0]
		}
		stmts, err := r.devices.RelationStatements(plan, keys)
		require.NoError(t, err)
		for _, st := range stmts {
			query(t, st.SQL, st.Args)
		}
	}
	pairs := func(table, column, device string) []string {
		t.Helper()
		var out []string
		for _, row := range query(t, "SELECT "+column+" FROM "+table+" WHERE device_id = '"+device+"' ORDER BY "+column, nil) {
			id, ok := row[0].(string)
			require.True(t, ok)
			out = append(out, id)
		}
		return out
	}

	apply("d2", `{"tags":["red","old","t2red","missing"]}`, true)
	assert.Equal(t, []string{"old", "red"}, pairs("device_tags", "tag_id", "d2"), "only red is visible; the pair to the deleted tag stays")

	apply("d3", `[{"op":"add","path":"/tags","value":["red","t2red"]}]`, false)
	assert.Equal(t, []string{"red", "t2red"}, pairs("device_tags", "tag_id", "d3"), "t2's tag is not paired; its old pair stays")

	apply("d1", `[{"op":"remove","path":"/tags","value":["red"]}]`, false)
	assert.Empty(t, pairs("device_tags", "tag_id", "d1"))

	apply("d1", `{"shared_types":["t1x","t2x"]}`, true)
	assert.Equal(t, []string{"t1x", "t2x"}, pairs("device_shared_types", "type_id", "d1"), "pt is replaced, t2x is t2's own: kept, never added")

	apply("d4", `{"tags":[]}`, true)
	assert.Equal(t, []string{"red"}, pairs("device_tags", "tag_id", "d4"), "clearing reaches t1's pairs only; d4's pair is t2's")
}
