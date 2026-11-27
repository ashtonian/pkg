//go:build integration

package parse_test

import (
	"context"
	"database/sql"
	"fmt"
	"iter"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// Declared relations over one fixture on each backend.
func TestDeclaredRelations(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		p, db := loadRelations(t, b)
		runRelationCases(t, relationResources(t, p), db)
	})
}

func TestDeclaredRelations_Memberships(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		p, db := loadRelations(t, b)
		runRelationMemberships(t, relationResources(t, p), b.dialect, db)
	})
}

func TestDeclaredRelations_Includes(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		p, db := loadRelations(t, b)
		runRelationIncludes(t, relationResources(t, p), db)
	})
}

func TestDeclaredRelations_Facets(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		p, db := loadRelations(t, b)
		runRelationFacets(t, relationResources(t, p), db)
	})
}

func TestDeclaredRelations_UnknownPolicy(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		p, db := loadRelations(t, b)
		runRelationUnknownPolicy(t, relationResources(t, p), db)
	})
}

func TestDeclaredRelations_Writes(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		p, db := loadRelations(t, b)
		runRelationWrites(t, relationResources(t, p), db)
	})
}

// loadRelations seeds a fresh database of b with the fixture, and returns
// its provider and the database.
func loadRelations(t *testing.T, b sqlBackend) (*parse.Provider, *sql.DB) {
	t.Helper()
	p, db := b.open(t)
	timestampType := b.columnType(core.TypeInfo{Type: core.FieldTypeTimestamp})
	const deletedAt = `'2026-01-01T00:00:00Z'`
	execStatements(t, db,
		`CREATE TABLE devices (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT NOT NULL, type_id TEXT, deleted_at `+timestampType+`)`,
		`CREATE TABLE commands (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, device_id TEXT, status TEXT NOT NULL, attempts INTEGER NOT NULL, deleted_at `+timestampType+`)`,
		`CREATE TABLE receipts (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, command_id TEXT NOT NULL, ok BOOLEAN NOT NULL)`,
		`CREATE TABLE device_types (id TEXT PRIMARY KEY, tenant_id TEXT, name TEXT NOT NULL)`,
		`CREATE TABLE tags (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, label TEXT NOT NULL, deleted_at `+timestampType+`)`,
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
			('c4','t1','d3','pending',9,`+deletedAt+`), ('c9','t2','d4','pending',7,NULL)`,
		`INSERT INTO receipts VALUES ('r1','t1','c1',true), ('r2','t1','c3',false)`,
		// pt is a platform type, t1x t1's own, t2x t2's.
		`INSERT INTO device_types VALUES ('pt',NULL,'pump-type'), ('t1x','t1','fan-type'), ('t2x','t2','valve-type')`,
		`INSERT INTO tags VALUES ('red','t1','red',NULL), ('old','t1','old',`+deletedAt+`), ('t2red','t2','red',NULL)`,
		`INSERT INTO device_tags VALUES ('d1','red','t1'), ('d2','old','t1'), ('d3','t2red','t1'), ('d4','red','t2')`,
		`INSERT INTO assets VALUES ('a1','t1','root',NULL), ('a2','t1','leaf','a1'), ('a3','t1','orphan',NULL)`,
	)
	return p, db
}

type relationCase struct {
	name   string
	assets bool // the query is the assets resource's
	filter string
	want   []string
}

func relationCases() []relationCase {
	cases := []relationCase{
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
	return cases
}

// runRelationCases lists each case through BuildList on db.
func runRelationCases(t *testing.T, r relResources, db *sql.DB) {
	t.Helper()
	for _, tc := range relationCases() {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{filter.ParamFilter: {tc.filter}, filter.ParamPageSize: {"50"}}
			var plan *parse.ListPlan
			if tc.assets {
				req, err := r.assets.ParseList(q)
				require.NoError(t, err)
				plan, err = r.assets.BuildList(req, parse.Scope{Tenant: "t1"})
				require.NoError(t, err)
			} else {
				req, err := r.devices.ParseList(q)
				require.NoError(t, err)
				plan, err = r.devices.BuildList(req, relationScopeT1())
				require.NoError(t, err)
			}
			got := rowIDs(t, queryRows[[]any](t, db, plan.Page.SQL, plan.Page.Args...))
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
func runRelationMemberships(t *testing.T, r relResources, d filter.Dialect, db *sql.DB) {
	t.Helper()
	lookup := sqlMembershipLookup{t: t, d: d, db: db}
	for _, tc := range relationCases() {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{filter.ParamFilter: {tc.filter}, filter.ParamPageSize: {"50"}}
			scope := relationScopeT1()
			var plan *parse.ListPlan
			if tc.assets {
				scope = parse.Scope{Tenant: "t1"}
				req, err := r.assets.ParseList(q)
				require.NoError(t, err)
				resolved, err := filter.ResolveMemberships(t.Context(), req.Filter(), lookup,
					filter.ResolveRelationScope(filter.RelationScope{Tenant: scope.Tenant, Related: scope.Related}))
				require.NoError(t, err)
				req, err = req.WithFilter(resolved)
				require.NoError(t, err)
				plan, err = r.assets.BuildList(req, scope)
				require.NoError(t, err)
			} else {
				req, err := r.devices.ParseList(q)
				require.NoError(t, err)
				resolved, err := filter.ResolveMemberships(t.Context(), req.Filter(), lookup,
					filter.ResolveRelationScope(filter.RelationScope{Tenant: scope.Tenant, Related: scope.Related}))
				require.NoError(t, err)
				req, err = req.WithFilter(resolved)
				require.NoError(t, err)
				plan, err = r.devices.BuildList(req, scope)
				require.NoError(t, err)
			}
			assert.NotContains(t, plan.Page.SQL, "EXISTS", "every relation predicate is resolved")
			got := rowIDs(t, queryRows[[]any](t, db, plan.Page.SQL, plan.Page.Args...))
			slices.Sort(got)
			assert.Equal(t, tc.want, got, plan.Page.SQL)
		})
	}

	_, err := filter.ResolveMemberships(t.Context(), relationFilter(t, r, `{"commands":"c1"}`), keysOnlyLookup{lookup}, filter.ResolveRelationScope(filter.RelationScope{Tenant: "t1"}))
	require.ErrorIs(t, err, filter.ErrRelationUnresolvable, "a store without filter evaluation refuses rather than over-matches")
	_, err = filter.ResolveMemberships(t.Context(), relationFilter(t, r, `{"commands":"c1"}`), nil)
	require.ErrorIs(t, err, filter.ErrRelationUnresolvable, "a backend without a lookup refuses a relation predicate as its own fault")
	_, err = filter.ResolveMemberships(t.Context(), relationFilter(t, r, `{"commands":"c1"}`), lookup)
	require.ErrorIs(t, err, filter.ErrRelationTenantRequired, "a tenant-scoped relation needs the tenant")
	_, err = filter.ResolveMemberships(t.Context(), relationFilter(t, r, `{"type":{"$is":{}}}`), lookup, filter.ResolveRelationScope(filter.RelationScope{Tenant: "t1"}))
	assert.ErrorIs(t, err, filter.ErrRelatedPolicyRequired)
}

func relationFilter(t *testing.T, r relResources, f string) *filter.ParsedFilter {
	t.Helper()
	req, err := r.devices.ParseList(url.Values{filter.ParamFilter: {f}})
	require.NoError(t, err)
	return req.Filter()
}

// sqlMembershipLookup answers a lookup with the subquery SQL renders for
// it, run on db.
type sqlMembershipLookup struct {
	t  *testing.T
	d  filter.Dialect
	db sqlQueryer
}

func (l sqlMembershipLookup) LookupFilteredValues(ctx context.Context, lk *filter.Lookup) ([]any, error) {
	return l.LookupValues(ctx, lk)
}

// LookupValues runs the subquery a membership renders for lk; a lookup
// projecting a key renders as the inner lookup of a probe's WhereKeyMember,
// and answers a core.Key per row.
func (l sqlMembershipLookup) LookupValues(ctx context.Context, lk *filter.Lookup) ([]any, error) {
	probe := &filter.FieldSchema{ExternalName: "probe", Column: "probe", DataType: core.FieldTypeString}
	member, prefix, suffix := lk, `"probe" IN (`, ")"
	if lk.ProjectKey != nil {
		member = &filter.Lookup{Table: "probe", Project: "probe", Where: filter.WhereKeyMember(lk.ProjectKey, lk)}
		columns := make([]string, len(lk.ProjectKey))
		for i, c := range lk.ProjectKey {
			columns[i] = `"probe".` + l.d.QuoteIdent(c)
		}
		prefix = `"probe" IN (SELECT "probe"."probe" FROM "probe" WHERE (` + strings.Join(columns, ", ") + `) IN (`
		suffix = "))"
	}
	res, err := filter.NewSQLBuilder(&filter.ResourceSchema{Name: "probe"}, l.d).
		Build(&filter.FilterAST{Root: &filter.FilterNode{Field: probe, Op: filter.OpMember, Member: member}})
	if err != nil {
		return nil, err
	}
	sub, ok := strings.CutPrefix(res.Where, prefix)
	require.True(l.t, ok, res.Where)
	sub, ok = strings.CutSuffix(sub, suffix)
	require.True(l.t, ok, res.Where)
	var out []any
	for _, row := range queryRowsContext[[]any](ctx, l.t, l.db, sub, res.Args...) {
		if lk.ProjectKey == nil {
			out = append(out, row[0])
		} else {
			out = append(out, core.Key(row))
		}
	}
	return out, nil
}

// LookupPairs runs lk projecting the owner's columns and lk's own as one
// key, and splits each row into its pair.
func (l sqlMembershipLookup) LookupPairs(ctx context.Context, lk *filter.Lookup, owner []string) (iter.Seq2[filter.IncludePair, error], error) {
	// Owner and target may share columns (a has-many whose foreign key is
	// part of the target's key), so the lookup projects each column once.
	var columns []string
	for _, c := range slices.Concat(owner, lookupColumns(lk)) {
		if !slices.Contains(columns, c) {
			columns = append(columns, c)
		}
	}
	pairs := *lk
	pairs.Project, pairs.ProjectKey = "", columns
	if len(columns) == 1 {
		pairs.Project, pairs.ProjectKey = columns[0], nil
	}
	rows, err := l.LookupValues(ctx, &pairs)
	if err != nil {
		return nil, err
	}
	part := func(row core.Key, names []string) any {
		if len(names) == 1 {
			return row[slices.Index(columns, names[0])]
		}
		k := make(core.Key, len(names))
		for i, c := range names {
			k[i] = row[slices.Index(columns, c)]
		}
		return k
	}
	return func(yield func(filter.IncludePair, error) bool) {
		for _, v := range rows {
			row, ok := v.(core.Key)
			if len(columns) == 1 {
				row, ok = core.Key{v}, true
			}
			if !ok {
				yield(filter.IncludePair{}, fmt.Errorf("pair row %T", v))
				return
			}
			if !yield(filter.IncludePair{Owner: part(row, owner), Target: part(row, lookupColumns(lk))}, nil) {
				return
			}
		}
	}, nil
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
func runRelationIncludes(t *testing.T, r relResources, db *sql.DB) {
	t.Helper()
	req, err := r.devices.ParseList(url.Values{filter.ParamInclude: {"commands,tags,shared_types"}, filter.ParamPageSize: {"50"}})
	require.NoError(t, err)
	plan, err := r.devices.BuildList(req, relationScopeT1())
	require.NoError(t, err)
	queries, err := r.devices.IncludeQueries(plan, []parse.Key{{"d1"}, {"d2"}, {"d3"}, {"d4"}})
	require.NoError(t, err)
	require.Len(t, queries, 3)
	got := map[string]map[string][]string{}
	for _, q := range queries {
		ids := map[string][]string{}
		for _, row := range queryRows[[]any](t, db, q.SQL, q.Args...) {
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
	require.Error(t, err, "a belongs-to is not includable")
	plan, err = r.devices.BuildList(req, parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	_, err = r.devices.IncludeQueries(plan, []parse.Key{{"d1"}})
	assert.ErrorIs(t, err, filter.ErrRelatedPolicyRequired, "a policy-scoped include needs the target's policy")
}

// runRelationFacets counts t1's devices per visible related key.
func runRelationFacets(t *testing.T, r relResources, db *sql.DB) {
	t.Helper()
	req, err := r.devices.ParseList(url.Values{filter.ParamFacets: {"commands,tags,shared_types"}})
	require.NoError(t, err)
	plan, err := r.devices.BuildList(req, relationScopeT1())
	require.NoError(t, err)
	require.NotNil(t, plan.Facets)
	got := map[string]map[string]int64{}
	for _, row := range queryRows[[]any](t, db, plan.Facets.SQL, plan.Facets.Args...) {
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

// runRelationUnknownPolicy lists t1's devices under a related policy that
// is SQL's unknown, as BuildBaseConstraints writes one without a tenant: no
// related row is visible, so a relation filter matches no owner and an
// include or facet holds no related row. That holds where the target is
// aliased too: in an include and a facet, and in a relation back to the
// owner's own table. Raw policy SQL is still refused under an alias.
func runRelationUnknownPolicy(t *testing.T, r relResources, db *sql.DB) {
	t.Helper()
	unknown := func(table string) parse.Scope {
		scope := parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{
			table: {Constraints: []*filter.PolicyConstraint{filter.UnknownConstraint()}},
		}}
		return scope
	}
	list := func(t *testing.T, owner interface {
		ParseList(url.Values) (*parse.ListRequest, error)
		BuildList(*parse.ListRequest, parse.Scope) (*parse.ListPlan, error)
	}, q url.Values, s parse.Scope) *parse.ListPlan {
		t.Helper()
		req, err := owner.ParseList(q)
		require.NoError(t, err)
		plan, err := owner.BuildList(req, s)
		require.NoError(t, err)
		return plan
	}
	ids := func(t *testing.T, sql string, args []any) []string {
		return rowIDs(t, queryRows[[]any](t, db, sql, args...))
	}

	t.Run("relation filter", func(t *testing.T) {
		plan := list(t, r.devices, url.Values{filter.ParamFilter: {`{"type":{"$is":{}}}`}}, unknown("device_types"))
		assert.Empty(t, ids(t, plan.Page.SQL, plan.Page.Args), plan.Page.SQL)
	})
	t.Run("include", func(t *testing.T) {
		plan := list(t, r.devices, url.Values{filter.ParamInclude: {"shared_types"}}, unknown("device_types"))
		queries, err := r.devices.IncludeQueries(plan, []parse.Key{{"d1"}, {"d2"}, {"d3"}, {"d4"}})
		require.NoError(t, err)
		require.Len(t, queries, 1)
		assert.Empty(t, queryRows[[]any](t, db, queries[0].SQL, queries[0].Args...), queries[0].SQL)
	})
	t.Run("facet", func(t *testing.T) {
		plan := list(t, r.devices, url.Values{filter.ParamFacets: {"shared_types"}}, unknown("device_types"))
		require.NotNil(t, plan.Facets)
		assert.Empty(t, queryRows[[]any](t, db, plan.Facets.SQL, plan.Facets.Args...), plan.Facets.SQL)
	})

	// A second resource over the assets table relates each asset to its
	// parent through the policy, so the target is the owner's own table.
	assets := parse.MustResource[relAsset](r.assets.Provider(), "assets", parse.TenantColumn("tenant_id"))
	assets.MustRelate("parent", parse.BelongsTo(assets, "parent_id", parse.PolicyScoped()))
	t.Run("aliased relation filter", func(t *testing.T) {
		all := list(t, assets, url.Values{filter.ParamFilter: {`{"parent":{"$is":{}}}`}}, parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{"assets": {}}})
		assert.Equal(t, []string{"a2"}, ids(t, all.Page.SQL, all.Page.Args), "an empty policy admits every parent")
		plan := list(t, assets, url.Values{filter.ParamFilter: {`{"parent":{"$is":{}}}`}}, unknown("assets"))
		assert.Empty(t, ids(t, plan.Page.SQL, plan.Page.Args), plan.Page.SQL)
	})
	t.Run("raw SQL under an alias", func(t *testing.T) {
		req, err := assets.ParseList(url.Values{filter.ParamFilter: {`{"parent":{"$is":{}}}`}})
		require.NoError(t, err)
		_, err = assets.BuildList(req, parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{
			"assets": {Constraints: []*filter.PolicyConstraint{{RawSQL: "assets.name = $1", RawArgs: []any{"root"}}}},
		}})
		assert.ErrorContains(t, err, "raw SQL cannot be applied to the aliased relation table")
	})
}

// runRelationWrites patches t1's devices' memberships: pairs are made only
// with targets the request may relate (live, in the tenant, or admitted by
// the target's policy), and a replace or remove reaches only pairs to such
// targets, so pairs the request cannot see stay.
func runRelationWrites(t *testing.T, r relResources, db *sql.DB) {
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
		_, err = r.devices.Update(t.Context(), p, parse.ByID(id), relationScopeT1())
		require.NoError(t, err, body)
	}
	pairs := func(table, column, device string) []string {
		t.Helper()
		return queryRows[string](t, db, "SELECT "+column+" FROM "+table+" WHERE device_id = '"+device+"' ORDER BY "+column)
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
