//go:build unit || integration

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

var relSeeds = []struct {
	tenant, id, status string
	tags               []string
}{
	{"A", "w1", "active", []string{"red", "blue"}},
	{"A", "w2", "active", []string{"blue"}},
	{"A", "w3", "inactive", nil},
	{"A", "w4", "inactive", []string{"red"}},
	{"A", "shared", "active", nil},
	{"B", "shared", "active", []string{"secret-B"}},
}

// relCases are evaluated for tenant A against a reference row set.
var relCases = map[string]struct {
	filter string
	want   []string
}{
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

type relDB struct {
	db      *sql.DB
	dialect filter.Dialect
	ddl     []string
}

func relParser(t *testing.T) *parse.Resource[relWidget] {
	t.Helper()
	// Unindexed relation predicates exceed the default cost ceiling; the
	// cost gate is orthogonal to the semantics under test.
	p, err := parse.NewResource[relWidget]("widgets", parse.ParserOptions(core.WithMaxCost(1000)))
	require.NoError(t, err)
	return p
}

func seedRelDB(t *testing.T, rdb relDB) {
	t.Helper()
	for _, stmt := range rdb.ddl {
		_, err := rdb.db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	ph := func(n int) string { return rdb.dialect.Placeholder(n) }
	for _, s := range relSeeds {
		_, err := rdb.db.Exec("INSERT INTO widgets (tenant_id, id, status) VALUES ("+ph(1)+", "+ph(2)+", "+ph(3)+")", s.tenant, s.id, s.status)
		require.NoError(t, err)
		for _, tag := range s.tags {
			_, err := rdb.db.Exec("INSERT INTO widget_tags (tenant_id, widget_id, tag_id) VALUES ("+ph(1)+", "+ph(2)+", "+ph(3)+")", s.tenant, s.id, tag)
			require.NoError(t, err)
		}
	}
}

func tenantA() *filter.PolicyConstraints {
	return filter.BuildBaseConstraints("tenant_id", "A", "", false)
}

func runRelationAlgebra(t *testing.T, rdb relDB) {
	t.Helper()
	seedRelDB(t, rdb)
	p := relParser(t)

	for name, tc := range relCases {
		t.Run(name, func(t *testing.T) {
			res, err := p.ListQuery(rdb.dialect).Build(
				filter.ListFilter(tc.filter), filter.ListPolicy(tenantA()), filter.ListTenant("A"),
				filter.ListLimit(100), filter.ListWithCount(true))
			require.NoError(t, err)
			got := queryIDs(t, rdb.db, "SELECT widgets.id FROM widgets "+res.Fragment, res.Args)
			slices.Sort(got)
			assert.Equal(t, tc.want, got)

			var count int64
			require.NoError(t, rdb.db.QueryRow(res.CountSQL, res.CountArgs...).Scan(&count))
			assert.Equal(t, int64(len(tc.want)), count, "count")
		})
	}

	t.Run("missing tenant fails closed", func(t *testing.T) {
		_, err := p.ListQuery(rdb.dialect).Build(filter.ListFilter(`{"tags":"red"}`), filter.ListPolicy(tenantA()))
		require.Error(t, err)
	})

	t.Run("facets count owners once", func(t *testing.T) {
		// w1 matches through two junction rows; it must count once.
		res, err := p.ListQuery(rdb.dialect).Build(
			filter.ListFilter(`{"tags":{"$in":["red","blue"]}}`), filter.ListPolicy(tenantA()), filter.ListTenant("A"),
			filter.ListFacets("status", "tags"))
		require.NoError(t, err)
		rows, err := rdb.db.Query(res.Facets.SQL, res.Facets.Args...)
		require.NoError(t, err)
		facets, err := filter.ScanFacetUnion(rows)
		require.NoError(t, rows.Close())
		require.NoError(t, err)
		got := map[string]map[string]int64{}
		for _, f := range facets {
			got[f.Field] = map[string]int64{}
			for _, b := range f.Buckets {
				got[f.Field][b.Key] = b.Count
			}
		}
		assert.Equal(t, map[string]int64{"active": 2, "inactive": 1}, got["status"])
		assert.Equal(t, map[string]int64{"red": 2, "blue": 2}, got["tags"], "relation facet stays tenant-scoped")
	})

	t.Run("includes stay tenant-scoped and capped per owner", func(t *testing.T) {
		field := p.FilterSchema().Fields["tags"]
		read := func(opts ...filter.IncludeQueryOption) map[string][]string {
			q, err := filter.BuildIncludeQuery(field, rdb.dialect, []any{"w1", "w2", "shared"}, append(opts, filter.WithIncludeTenant("A"))...)
			require.NoError(t, err)
			rows, err := rdb.db.Query(q.SQL, q.Args...)
			require.NoError(t, err, q.SQL)
			defer func() { require.NoError(t, rows.Close()) }()
			got, err := q.Scan(rows)
			require.NoError(t, err)
			return got.IDs
		}
		assert.Equal(t, map[string][]string{"w1": {"blue", "red"}, "w2": {"blue"}}, read(), "B's tag on 'shared' is not A's")
		assert.Equal(t, map[string][]string{"w1": {"blue"}, "w2": {"blue"}}, read(filter.WithMaxIncludePerRelation(1)))
	})
}

func queryIDs(t *testing.T, db *sql.DB, q string, args []any) []string {
	t.Helper()
	rows, err := db.Query(q, args...)
	require.NoError(t, err, q)
	defer func() { require.NoError(t, rows.Close()) }()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}
