//go:build integration

package parse_test

import (
	"database/sql"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// relWidget has a tenant-scoped M2M relation. Owner ids are tenant-local:
// "shared" exists in tenants A and B, and only B's carries tag secret-B.
type relWidget struct {
	ID       string   `json:"id"        db:"id"        parse:"filter"`
	TenantID string   `json:"tenant_id" db:"tenant_id" parse:"filter"`
	Status   string   `json:"status"    db:"status"    parse:"filter,facet"`
	Tags     []string `json:"-"         db:"-"         parse:"filter,facet,include" filter_ops:"eq,ne,in,nin,null,not_null" relation:"widget_tags,widget_id,tag_id,id,tenant_id"`
}

type relSeed struct {
	tenant, id, status string
	tags               []string
}

func relSeeds() []relSeed {
	seeds := []relSeed{
		{"A", "w1", "active", []string{"red", "blue"}},
		{"A", "w2", "active", []string{"blue"}},
		{"A", "w3", "inactive", nil},
		{"A", "w4", "inactive", []string{"red"}},
		{"A", "shared", "active", nil},
		{"B", "shared", "active", []string{"secret-B"}},
	}
	return seeds
}

type relCase struct {
	filter string
	want   []string
}

// relCases are evaluated for tenant A against a reference row set.
func relCases() map[string]relCase {
	cases := map[string]relCase{
		"eq":                 {`{"tags":"red"}`, []string{"w1", "w4"}},
		"in":                 {`{"tags":{"$in":["red"]}}`, []string{"w1", "w4"}},
		"in empty":           {`{"tags":{"$in":[]}}`, nil},
		"nin":                {`{"tags":{"$nin":["red"]}}`, []string{"shared", "w2", "w3"}},
		"nin empty":          {`{"tags":{"$nin":[]}}`, []string{"shared", "w1", "w2", "w3", "w4"}},
		"ne":                 {`{"tags":{"$ne":"red"}}`, []string{"shared", "w2", "w3"}},
		"null":               {`{"tags":{"$null":true}}`, []string{"shared", "w3"}},
		"not null":           {`{"tags":{"$null":false}}`, []string{"w1", "w2", "w4"}},
		"or with scalar":     {`{"$or":[{"tags":"red"},{"status":"inactive"}]}`, []string{"w1", "w3", "w4"}},
		"two relations":      {`{"$and":[{"tags":"red"},{"tags":"blue"}]}`, []string{"w1"}},
		"not in":             {`{"$not":{"tags":{"$in":["blue"]}}}`, []string{"shared", "w3", "w4"}},
		"other tenant's tag": {`{"tags":{"$in":["secret-B"]}}`, nil},
	}
	return cases
}

// Relation predicates keep set semantics inside AND / OR / NOT, stay inside
// the tenant, and never multiply owners in counts or facets, on each
// backend.
func TestRelationAlgebra(t *testing.T) {
	runSQLBackends(t, runRelationAlgebra)
}

func relWidgets() sqlTable {
	table := sqlTable{name: "widgets", key: []string{"tenant_id", "id"}, columns: []sqlColumn{
		column[string]("tenant_id").required(), column[string]("id").required(), column[string]("status").required(),
	}}
	return table
}

func relWidgetTags() sqlTable {
	table := sqlTable{name: "widget_tags", key: []string{"tenant_id", "widget_id", "tag_id"}, columns: []sqlColumn{
		column[string]("tenant_id").required(), column[string]("widget_id").required(), column[string]("tag_id").required(),
	}}
	return table
}

func relParser(t *testing.T) *parse.Resource[relWidget] {
	t.Helper()
	// Unindexed relation predicates exceed the default cost ceiling; the
	// cost gate is orthogonal to the semantics under test.
	p, err := parse.NewResource[relWidget]("widgets", parse.ParserOptions(core.WithMaxCost(1000)))
	require.NoError(t, err)
	return p
}

func seedRelDB(t *testing.T, b sqlBackend, db *sql.DB) {
	t.Helper()
	widgets, tags := relWidgets(), relWidgetTags()
	b.create(t, db, widgets, tags)
	for _, s := range relSeeds() {
		b.insert(t, db, widgets, []any{s.tenant, s.id, s.status})
		for _, tag := range s.tags {
			b.insert(t, db, tags, []any{s.tenant, s.id, tag})
		}
	}
}

func tenantA() *filter.PolicyConstraints {
	return filter.BuildBaseConstraints("tenant_id", "A", "", false)
}

func runRelationAlgebra(t *testing.T, b sqlBackend) {
	t.Helper()
	db := b.open(t)
	seedRelDB(t, b, db)
	p := relParser(t)

	lq, err := p.ListQuery(b.dialect)
	require.NoError(t, err)
	for name, tc := range relCases() {
		t.Run(name, func(t *testing.T) {
			res, err := lq.Build(
				filter.ListFilter(tc.filter), filter.ListPolicy(tenantA()), filter.ListRelationScope(filter.RelationScope{Tenant: "A"}),
				filter.ListLimit(100), filter.ListWithCount(true))
			require.NoError(t, err)
			got := queryRows[string](t, db, "SELECT widgets.id FROM widgets "+res.Fragment, res.Args...)
			slices.Sort(got)
			assert.Equal(t, tc.want, got)

			var count int64
			require.NoError(t, db.QueryRowContext(t.Context(), res.CountSQL, res.CountArgs...).Scan(&count))
			assert.Equal(t, int64(len(tc.want)), count, "count")
		})
	}

	t.Run("missing tenant fails closed", func(t *testing.T) {
		_, err := lq.Build(filter.ListFilter(`{"tags":"red"}`), filter.ListPolicy(tenantA()))
		require.Error(t, err)
	})

	t.Run("facets count owners once", func(t *testing.T) {
		// w1 matches through two junction rows; it must count once.
		res, err := lq.Build(
			filter.ListFilter(`{"tags":{"$in":["red","blue"]}}`), filter.ListPolicy(tenantA()), filter.ListRelationScope(filter.RelationScope{Tenant: "A"}),
			filter.ListFacets("status", "tags"))
		require.NoError(t, err)
		got := bucketCounts(queryFacets(t, db, res.Facets.SQL, res.Facets.Args...))
		assert.Equal(t, map[string]int64{"active": 2, "inactive": 1}, got["status"])
		assert.Equal(t, map[string]int64{"red": 2, "blue": 2}, got["tags"], "relation facet stays tenant-scoped")
	})

	t.Run("includes stay tenant-scoped and capped per owner", func(t *testing.T) {
		field := p.FilterSchema().Fields["tags"]
		read := func(opts ...filter.IncludeQueryOption) map[string][]string {
			q, err := filter.BuildIncludeQuery(field, b.dialect, []core.Key{{"w1"}, {"w2"}, {"shared"}}, append(opts, filter.WithIncludeRelationScope(filter.RelationScope{Tenant: "A"}))...)
			require.NoError(t, err)
			rows, err := db.QueryContext(t.Context(), q.SQL, q.Args...)
			require.NoError(t, err, q.SQL)
			defer func() { require.NoError(t, rows.Close()) }()
			got, err := q.Scan(rows)
			require.NoError(t, err)
			require.NoError(t, rows.Err(), q.SQL)
			return got.IDs
		}
		assert.Equal(t, map[string][]string{"w1": {"blue", "red"}, "w2": {"blue"}}, read(), "B's tag on 'shared' is not A's")
		assert.Equal(t, map[string][]string{"w1": {"blue"}, "w2": {"blue"}}, read(filter.WithMaxIncludePerRelation(1)))
	})
}
