//go:build integration

package corpus

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
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/patch"
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
func relationAlgebra(t *testing.T, engine Engine) {
	runOn(t, engine, runRelationAlgebra)
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

// relParser builds the widgets resource on p.
func relParser(t *testing.T, p *parse.Provider) *parse.Resource[relWidget] {
	t.Helper()
	// Unindexed relation predicates exceed the default cost ceiling; the
	// cost gate is orthogonal to the semantics under test.
	r, err := parse.NewResource[relWidget](p, "widgets", parse.ParserOptions(core.WithMaxCost(1000)))
	require.NoError(t, err)
	return r
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
	provider, db := b.open(t)
	seedRelDB(t, b, db)
	p := relParser(t, provider)

	lq, err := p.ListQuery()
	require.NoError(t, err)
	for name, tc := range relCases() {
		t.Run(name, func(t *testing.T) {
			res, err := lq.Build(
				filter.ListFilter(tc.filter), filter.ListPolicy(tenantA()), filter.ListRelationScope(filter.RelationScope{Tenant: "A"}),
				filter.ListLimit(100), filter.ListWithCount(true))
			require.NoError(t, err)
			got := dbtest.QueryRows[string](t, db, "SELECT widgets.id FROM widgets "+res.Fragment, res.Args...)
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

type crxBin struct {
	ID   int64  `json:"id"   db:"id"   parse:"filter,sort"`
	Name string `json:"name" db:"name" parse:"filter"`
}

// crxPairTag is a tag whose stock is a pair table compared by key: the
// relation tag names the target key's columns, and crxStockRef, the struct
// the field holds, types them.
type crxPairTag struct {
	ID    string        `json:"id"    db:"id"    parse:"filter,sort"`
	Stock []crxStockRef `json:"stock" db:"-"     relation:"crx_stock_tags,tag_id,warehouse_id+sku" parse:"filter,include,facet,patch" indexed:"true"`
}

type crxStockRef struct {
	WarehouseID int64  `db:"warehouse_id"`
	SKU         string `db:"sku"`
}

// crxGraph relates stock, keyed by (warehouse_id, sku), as the owner and as
// the target of a has-many, a belongs-to and a many-to-many each.
type crxGraph struct {
	bins  *parse.Resource[crxBin]
	stock *parse.Resource[parsetest.CrStock]
	moves *parse.Resource[parsetest.CrMove]
	tags  *parse.Resource[parsetest.CrTag]
}

// crxResources builds the graph on p.
func crxResources(t *testing.T, p *parse.Provider) crxGraph {
	t.Helper()
	g := crxGraph{
		bins:  parse.MustResource[crxBin](p, "crx_bins"),
		stock: parse.MustResource[parsetest.CrStock](p, "crx_stock", parse.PrimaryKey("warehouse_id", "sku")),
		moves: parse.MustResource[parsetest.CrMove](p, "crx_moves"),
		tags:  parse.MustResource[parsetest.CrTag](p, "crx_tags"),
	}
	key := []string{"warehouse_id", "sku"}
	all := []parse.RelationOption{parse.Indexed(), parse.Includable(), parse.Facetable()}
	require.NoError(t, g.bins.Relate("stock", parse.HasMany(g.stock, "warehouse_id", all...)))
	require.NoError(t, g.stock.Relate("bin", parse.BelongsTo(g.bins, "warehouse_id")))
	require.NoError(t, g.stock.Relate("moves", parse.HasMany(g.moves, key, all...)))
	require.NoError(t, g.stock.Relate("tags", parse.ManyToMany(g.tags, parse.Through("crx_stock_tags", key, "tag_id"), append(all, parse.Writable())...)))
	require.NoError(t, g.moves.Relate("stock", parse.BelongsTo(g.stock, key)))
	require.NoError(t, g.tags.Relate("stock", parse.ManyToMany(g.stock, parse.Through("crx_stock_tags", "tag_id", key), append(all, parse.Writable())...)))
	return g
}

// crxFixture: each key column alone ties across stock rows — warehouse 1
// holds SKUs a and b, SKU a is in warehouses 1 and 2 — so only the whole
// key relates a row. Move 13 names stock that does not exist.
func crxFixture() []string {
	stmts := []string{
		`CREATE TABLE crx_bins (id BIGINT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE crx_stock (warehouse_id BIGINT NOT NULL, sku TEXT NOT NULL, name TEXT NOT NULL, PRIMARY KEY (warehouse_id, sku))`,
		`CREATE TABLE crx_moves (id BIGINT PRIMARY KEY, warehouse_id BIGINT NOT NULL, sku TEXT NOT NULL, code TEXT NOT NULL)`,
		`CREATE TABLE crx_tags (id TEXT PRIMARY KEY, label TEXT NOT NULL)`,
		`CREATE TABLE crx_stock_tags (warehouse_id BIGINT NOT NULL, sku TEXT NOT NULL, tag_id TEXT NOT NULL, PRIMARY KEY (warehouse_id, sku, tag_id))`,
		`INSERT INTO crx_bins VALUES (1, 'north'), (2, 'south'), (3, 'empty')`,
		`INSERT INTO crx_stock VALUES (1, 'a', 'bolt'), (1, 'b', 'nut'), (2, 'a', 'washer'), (2, 'b', 'bolt')`,
		`INSERT INTO crx_moves VALUES (10, 1, 'a', 'in'), (11, 1, 'a', 'out'), (12, 2, 'b', 'in'), (13, 2, 'c', 'in')`,
		`INSERT INTO crx_tags VALUES ('red', 'Red'), ('blue', 'Blue'), ('green', 'Green')`,
		`INSERT INTO crx_stock_tags VALUES (1, 'a', 'red'), (1, 'a', 'blue'), (2, 'a', 'red'), (2, 'b', 'blue')`,
	}
	return stmts
}

func compositeKeyRelations(t *testing.T, engine Engine) {
	runOn(t, engine, runCompositeKeyRelations)
}

func runCompositeKeyRelations(t *testing.T, b sqlBackend) {
	// setup opens a fresh database holding the fixture, with its provider.
	setup := func(t *testing.T) (*parse.Provider, *sql.DB) {
		t.Helper()
		p, db := b.open(t)
		dbtest.Exec(t, db, crxFixture()...)
		return p, db
	}
	p, db := setup(t)
	g := crxResources(t, p)

	t.Run("filters", func(t *testing.T) {
		for _, tt := range []struct {
			name, filter string
			list         func(t *testing.T, doc string) []string
			want         []string
		}{
			{name: "has-many owner $some", filter: `{"moves":{"$some":{"code":"in"}}}`, list: crxList(g.stock, crxStockKey), want: []string{"1/a", "2/b"}},
			{name: "has-many owner $none", filter: `{"moves":{"$none":{}}}`, list: crxList(g.stock, crxStockKey), want: []string{"1/b", "2/a"}},
			{name: "many-to-many owner $some", filter: `{"tags":{"$some":{"label":"Blue"}}}`, list: crxList(g.stock, crxStockKey), want: []string{"1/a", "2/b"}},
			{name: "many-to-many owner $none", filter: `{"tags":{"$none":{"id":"red"}}}`, list: crxList(g.stock, crxStockKey), want: []string{"1/b", "2/b"}},
			{name: "many-to-many owner by key", filter: `{"tags":"red"}`, list: crxList(g.stock, crxStockKey), want: []string{"1/a", "2/a"}},
			{name: "belongs-to owner $is", filter: `{"bin":{"$is":{"name":"north"}}}`, list: crxList(g.stock, crxStockKey), want: []string{"1/a", "1/b"}},
			{name: "belongs-to target $is", filter: `{"stock":{"$is":{"name":"bolt"}}}`, list: crxList(g.moves, crxMoveKey), want: []string{"10", "11", "12"}},
			{name: "belongs-to target $is_not", filter: `{"stock":{"$is_not":{"name":"bolt"}}}`, list: crxList(g.moves, crxMoveKey), want: []string{"13"}},
			{name: "belongs-to target $null", filter: `{"stock":{"$null":true}}`, list: crxList(g.moves, crxMoveKey), want: []string{"13"}},
			{name: "has-many target $some", filter: `{"stock":{"$some":{"name":"nut"}}}`, list: crxList(g.bins, crxBinKey), want: []string{"1"}},
			{name: "has-many target $none", filter: `{"stock":{"$none":{}}}`, list: crxList(g.bins, crxBinKey), want: []string{"3"}},
			{name: "many-to-many target $some", filter: `{"stock":{"$some":{"warehouse_id":2}}}`, list: crxList(g.tags, crxTagKey), want: []string{"blue", "red"}},
			{name: "many-to-many target $some, none match", filter: `{"stock":{"$some":{"name":"nut"}}}`, list: crxList(g.tags, crxTagKey), want: nil},
			{name: "many-to-many target $none", filter: `{"stock":{"$none":{"sku":"a"}}}`, list: crxList(g.tags, crxTagKey), want: []string{"green"}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				assert.Equal(t, tt.want, tt.list(t, tt.filter))
			})
		}
	})

	t.Run("resolved outside SQL", func(t *testing.T) {
		store, lookups := resolvedStores(t, db, b.dialect, "crx_bins", "crx_stock", "crx_moves", "crx_tags", "crx_stock_tags")
		assertResolvedFilters(t, store, lookups, g.stock,
			`{"moves":{"$some":{"code":"in"}}}`, `{"moves":{"$none":{}}}`, `{"tags":{"$some":{"label":"Blue"}}}`,
			`{"tags":{"$none":{"id":"red"}}}`, `{"tags":"red"}`, `{"tags":{"$nin":["red"]}}`, `{"tags":{"$null":true}}`,
			`{"bin":{"$is":{"name":"north"}}}`)
		assertResolvedFilters(t, store, lookups, g.moves,
			`{"stock":{"$is":{"name":"bolt"}}}`, `{"stock":{"$is_not":{"name":"bolt"}}}`, `{"stock":{"$null":true}}`, `{"stock":{"$null":false}}`)
		assertResolvedFilters(t, store, lookups, g.bins, `{"stock":{"$some":{"name":"nut"}}}`, `{"stock":{"$none":{}}}`, `{"stock":{"$some":{"moves":{"$none":{}}}}}`)
		assertResolvedFilters(t, store, lookups, g.tags,
			`{"stock":{"$some":{"warehouse_id":2}}}`, `{"stock":{"$some":{"name":"nut"}}}`, `{"stock":{"$none":{"sku":"a"}}}`)
		assertResolvedIncludes(t, lookups, g.stock, "moves", "tags")
		assertResolvedIncludes(t, lookups, g.bins, "stock")
		assertResolvedIncludes(t, lookups, g.tags, "stock")
	})

	t.Run("includes and facets", func(t *testing.T) {
		stock := crxPage(t, g.stock, url.Values{filter.ParamInclude: {"moves,tags"}, filter.ParamFacets: {"tags,moves"}})
		assert.Equal(t, map[string][]string{`["1","a"]`: {"10", "11"}, `["2","b"]`: {"12"}}, stock.Relations["moves"])
		assert.Equal(t, map[string][]string{`["1","a"]`: {"blue", "red"}, `["2","a"]`: {"red"}, `["2","b"]`: {"blue"}}, stock.Relations["tags"])
		assert.ElementsMatch(t, []filter.FacetBucket{{Key: "blue", Count: 2}, {Key: "red", Count: 2}}, stock.Facets["tags"])
		assert.ElementsMatch(t, []filter.FacetBucket{{Key: "10", Count: 1}, {Key: "11", Count: 1}, {Key: "12", Count: 1}}, stock.Facets["moves"])

		bins := crxPage(t, g.bins, url.Values{filter.ParamInclude: {"stock"}, filter.ParamFacets: {"stock"}})
		assert.Equal(t, map[string][]string{"1": {`["1","a"]`, `["1","b"]`}, "2": {`["2","a"]`, `["2","b"]`}}, bins.Relations["stock"])
		assert.ElementsMatch(t, []filter.FacetBucket{
			{Key: `["1","a"]`, Count: 1}, {Key: `["1","b"]`, Count: 1}, {Key: `["2","a"]`, Count: 1}, {Key: `["2","b"]`, Count: 1},
		}, bins.Facets["stock"])

		tags := crxPage(t, g.tags, url.Values{filter.ParamInclude: {"stock"}, filter.ParamFacets: {"stock"}})
		assert.Equal(t, map[string][]string{"blue": {`["1","a"]`, `["2","b"]`}, "red": {`["1","a"]`, `["2","a"]`}}, tags.Relations["stock"])
		assert.ElementsMatch(t, []filter.FacetBucket{{Key: `["1","a"]`, Count: 2}, {Key: `["2","a"]`, Count: 1}, {Key: `["2","b"]`, Count: 1}}, tags.Facets["stock"])

		// The include names each owner as core.KeyText names its key.
		for _, item := range stock.Items {
			key, err := g.stock.Key(&item)
			require.NoError(t, err)
			ids, ok := stock.Relations["tags"][core.KeyText(key)]
			if ok {
				assert.NotEmpty(t, ids)
			}
		}
	})

	// The pair rows are the relation: a pair whose target key names no
	// stock row still pairs, as no target table is read.
	t.Run("pair table tagged by key", func(t *testing.T) {
		pairTags := func(p *parse.Provider) *parse.Resource[crxPairTag] {
			return parse.MustResource[crxPairTag](p, "crx_tags")
		}
		tags := pairTags(p)
		pairTagKey := func(p crxPairTag) string { return p.ID }
		list := crxList(tags, pairTagKey)
		assert.Equal(t, []string{"blue", "red"}, list(t, `{"stock":{"$null":false}}`))
		assert.Equal(t, []string{"green"}, list(t, `{"stock":{"$null":true}}`))
		for _, doc := range []string{`{"stock":"x"}`, `{"stock":{"$in":[[1,"a"]]}}`, `{"stock":{"$some":{}}}`} {
			_, err := tags.ParseList(url.Values{filter.ParamFilter: {doc}})
			require.Error(t, err, doc)
		}

		page := crxPage(t, tags, url.Values{filter.ParamInclude: {"stock"}, filter.ParamFacets: {"stock"}})
		assert.Equal(t, map[string][]string{"blue": {`["1","a"]`, `["2","b"]`}, "red": {`["1","a"]`, `["2","a"]`}}, page.Relations["stock"])
		assert.ElementsMatch(t, []filter.FacetBucket{{Key: `["1","a"]`, Count: 2}, {Key: `["2","a"]`, Count: 1}, {Key: `["2","b"]`, Count: 1}}, page.Facets["stock"])

		store, lookups := resolvedStores(t, db, b.dialect, "crx_tags", "crx_stock_tags")
		assertResolvedFilters(t, store, lookups, tags, `{"stock":{"$null":false}}`, `{"stock":{"$null":true}}`)
		assertResolvedIncludes(t, lookups, tags, "stock")

		for _, tt := range []struct {
			name   string
			update func(t *testing.T, tags *parse.Resource[crxPairTag])
			want   []string
		}{
			{
				name: "replace",
				update: func(t *testing.T, tags *parse.Resource[crxPairTag]) {
					crxUpdate(t, tags, `{"stock":[[1,"b"],[9,"z"],[1,"b"]]}`, parse.ByID("red"))
				},
				want: []string{"1/a/blue", "1/b/red", "2/b/blue", "9/z/red"},
			},
			{
				name: "add and remove",
				update: func(t *testing.T, tags *parse.Resource[crxPairTag]) {
					crxUpdatePatch(t, tags, `[{"op":"add","path":"/stock","value":[[1,"b"],[2,"a"]]}]`, parse.ByID("green"))
					crxUpdatePatch(t, tags, `[{"op":"remove","path":"/stock","value":[[1,"a"],[2,"b"]]}]`, parse.ByID("blue"))
				},
				want: []string{"1/a/red", "1/b/green", "2/a/green", "2/a/red"},
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				p, db := setup(t)
				tt.update(t, pairTags(p))
				assert.Equal(t, tt.want, crxPairs(t, db))
			})
		}
		_, err := tags.ParseMergePatch([]byte(`{"stock":["1/a"]}`))
		assert.Error(t, err, "a key of several columns is an array of a value per column")
	})

	t.Run("writes", func(t *testing.T) {
		for _, tt := range []struct {
			name   string
			update func(t *testing.T, g crxGraph)
			want   []string
		}{
			{
				name: "owner key replaces its targets",
				update: func(t *testing.T, g crxGraph) {
					crxUpdate(t, g.stock, `{"tags":["green","red"]}`, parse.ByKey(parse.Key{int64(1), "b"}))
				},
				want: []string{"1/a/blue", "1/a/red", "1/b/green", "1/b/red", "2/a/red", "2/b/blue"},
			},
			{
				name: "owner keys add a target",
				update: func(t *testing.T, g crxGraph) {
					crxUpdatePatch(t, g.stock, `[{"op":"add","path":"/tags","value":["green"]}]`, parse.ByKeys(parse.Key{int64(1), "a"}, parse.Key{int64(2), "a"}))
				},
				want: []string{"1/a/blue", "1/a/green", "1/a/red", "2/a/green", "2/a/red", "2/b/blue"},
			},
			{
				name: "target keys replace",
				update: func(t *testing.T, g crxGraph) {
					crxUpdate(t, g.tags, `{"stock":[[1,"b"],[2,"b"],[2,"b"]]}`, parse.ByID("red"))
				},
				want: []string{"1/a/blue", "1/b/red", "2/b/blue", "2/b/red"},
			},
			{
				name: "target keys add and remove",
				update: func(t *testing.T, g crxGraph) {
					crxUpdatePatch(t, g.tags, `[{"op":"add","path":"/stock","value":[[1,"b"],[2,"a"]]}]`, parse.ByID("blue"))
					crxUpdatePatch(t, g.tags, `[{"op":"remove","path":"/stock","value":[[1,"a"],[2,"a"]]}]`, parse.ByID("red"))
				},
				want: []string{"1/a/blue", "1/b/blue", "2/a/blue", "2/b/blue"},
			},
			{
				name: "a target key that names no row pairs nothing",
				update: func(t *testing.T, g crxGraph) {
					crxUpdatePatch(t, g.tags, `[{"op":"add","path":"/stock","value":[[1,"c"],[3,"a"]]}]`, parse.ByID("green"))
				},
				want: []string{"1/a/blue", "1/a/red", "2/a/red", "2/b/blue"},
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				p, db := setup(t)
				tt.update(t, crxResources(t, p))
				assert.Equal(t, tt.want, crxPairs(t, db))
			})
		}
	})
}

// crxPairs are the stock-tag pairs, each warehouse/sku/tag, in that order.
func crxPairs(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var pairs []string
	for _, row := range dbtest.QueryRows[[]any](t, db, `SELECT warehouse_id, sku, tag_id FROM crx_stock_tags ORDER BY warehouse_id, sku, tag_id`) {
		pairs = append(pairs, fmt.Sprintf("%v/%v/%v", row[0], row[1], row[2]))
	}
	return pairs
}

func crxStockKey(s parsetest.CrStock) string { return fmt.Sprintf("%d/%s", s.WarehouseID, s.SKU) }

func crxMoveKey(m parsetest.CrMove) string { return fmt.Sprint(m.ID) }

func crxBinKey(b crxBin) string { return fmt.Sprint(b.ID) }

func crxTagKey(t parsetest.CrTag) string { return t.ID }

// crxList lists r's rows a filter document admits, each as key names it,
// in key order.
func crxList[T any](r *parse.Resource[T], key func(T) string) func(t *testing.T, doc string) []string {
	return func(t *testing.T, doc string) []string {
		t.Helper()
		var out []string
		for _, item := range crxPage(t, r, url.Values{filter.ParamFilter: {doc}}).Items {
			out = append(out, key(item))
		}
		return out
	}
}

func crxPage[T any](t *testing.T, r *parse.Resource[T], q url.Values) *filter.Page[T] {
	t.Helper()
	req, err := r.ParseList(q)
	require.NoError(t, err)
	return listPage(t, r, req, parse.Scope{})
}

// crxUpdate applies a merge patch to the target's rows.
func crxUpdate[T any](t *testing.T, r *parse.Resource[T], body string, target parse.Target) {
	t.Helper()
	p, err := r.ParseMergePatch([]byte(body))
	require.NoError(t, err)
	crxApply(t, r, p, target)
}

// crxUpdatePatch applies a JSON Patch to the target's rows.
func crxUpdatePatch[T any](t *testing.T, r *parse.Resource[T], body string, target parse.Target) {
	t.Helper()
	p, err := r.ParsePatch([]byte(body))
	require.NoError(t, err)
	crxApply(t, r, p, target)
}

func crxApply[T any](t *testing.T, r *parse.Resource[T], p *patch.ParsedPatch, target parse.Target) {
	t.Helper()
	_, err := r.Update(t.Context(), p, target, parse.Scope{})
	require.NoError(t, err)
}

// Declared relations over one fixture on each backend.
func declaredRelations(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		p, db := loadRelations(t, b)
		runRelationCases(t, parsetest.RelationResources(t, p), db)
	})
}

func declaredRelationsMemberships(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		p, db := loadRelations(t, b)
		runRelationMemberships(t, parsetest.RelationResources(t, p), b.dialect, db)
	})
}

func declaredRelationsIncludes(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		p, db := loadRelations(t, b)
		runRelationIncludes(t, parsetest.RelationResources(t, p), db)
	})
}

func declaredRelationsFacets(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		p, db := loadRelations(t, b)
		runRelationFacets(t, parsetest.RelationResources(t, p), db)
	})
}

func declaredRelationsUnknownPolicy(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		p, db := loadRelations(t, b)
		runRelationUnknownPolicy(t, parsetest.RelationResources(t, p), db)
	})
}

func declaredRelationsWrites(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		p, db := loadRelations(t, b)
		runRelationWrites(t, parsetest.RelationResources(t, p), db)
	})
}

// loadRelations seeds a fresh database of b with the fixture, and returns
// its provider and the database.
func loadRelations(t *testing.T, b sqlBackend) (*parse.Provider, *sql.DB) {
	t.Helper()
	p, db := b.open(t)
	timestampType := b.columnType(core.TypeInfo{Type: core.FieldTypeTimestamp})
	const deletedAt = `'2026-01-01T00:00:00Z'`
	dbtest.Exec(t, db,
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
func runRelationCases(t *testing.T, r parsetest.RelResources, db *sql.DB) {
	t.Helper()
	for _, tc := range relationCases() {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{filter.ParamFilter: {tc.filter}, filter.ParamPageSize: {"50"}}
			var plan *parse.ListPlan
			if tc.assets {
				req, err := r.Assets.ParseList(q)
				require.NoError(t, err)
				plan, err = r.Assets.BuildList(req, parse.Scope{Tenant: "t1"})
				require.NoError(t, err)
			} else {
				req, err := r.Devices.ParseList(q)
				require.NoError(t, err)
				plan, err = r.Devices.BuildList(req, parsetest.RelationScopeT1())
				require.NoError(t, err)
			}
			got := rowIDs(t, dbtest.QueryRows[[]any](t, db, plan.Page.SQL, plan.Page.Args...))
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
func runRelationMemberships(t *testing.T, r parsetest.RelResources, d filter.Dialect, db *sql.DB) {
	t.Helper()
	lookup := sqlMembershipLookup{t: t, d: d, db: db}
	for _, tc := range relationCases() {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{filter.ParamFilter: {tc.filter}, filter.ParamPageSize: {"50"}}
			scope := parsetest.RelationScopeT1()
			var plan *parse.ListPlan
			if tc.assets {
				scope = parse.Scope{Tenant: "t1"}
				req, err := r.Assets.ParseList(q)
				require.NoError(t, err)
				resolved, err := filter.ResolveMemberships(t.Context(), req.Filter(), lookup,
					filter.ResolveRelationScope(filter.RelationScope{Tenant: scope.Tenant, Related: scope.Related}))
				require.NoError(t, err)
				req, err = req.WithFilter(resolved)
				require.NoError(t, err)
				plan, err = r.Assets.BuildList(req, scope)
				require.NoError(t, err)
			} else {
				req, err := r.Devices.ParseList(q)
				require.NoError(t, err)
				resolved, err := filter.ResolveMemberships(t.Context(), req.Filter(), lookup,
					filter.ResolveRelationScope(filter.RelationScope{Tenant: scope.Tenant, Related: scope.Related}))
				require.NoError(t, err)
				req, err = req.WithFilter(resolved)
				require.NoError(t, err)
				plan, err = r.Devices.BuildList(req, scope)
				require.NoError(t, err)
			}
			assert.NotContains(t, plan.Page.SQL, "EXISTS", "every relation predicate is resolved")
			got := rowIDs(t, dbtest.QueryRows[[]any](t, db, plan.Page.SQL, plan.Page.Args...))
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

func relationFilter(t *testing.T, r parsetest.RelResources, f string) *filter.ParsedFilter {
	t.Helper()
	req, err := r.Devices.ParseList(url.Values{filter.ParamFilter: {f}})
	require.NoError(t, err)
	return req.Filter()
}

// sqlMembershipLookup answers a lookup with the subquery SQL renders for
// it, run on db.
type sqlMembershipLookup struct {
	t  *testing.T
	d  filter.Dialect
	db dbtest.Queryer
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
	for _, row := range dbtest.QueryRowsContext[[]any](ctx, l.t, l.db, sub, res.Args...) {
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
func runRelationIncludes(t *testing.T, r parsetest.RelResources, db *sql.DB) {
	t.Helper()
	req, err := r.Devices.ParseList(url.Values{filter.ParamInclude: {"commands,tags,shared_types"}, filter.ParamPageSize: {"50"}})
	require.NoError(t, err)
	plan, err := r.Devices.BuildList(req, parsetest.RelationScopeT1())
	require.NoError(t, err)
	queries, err := r.Devices.IncludeQueries(plan, []parse.Key{{"d1"}, {"d2"}, {"d3"}, {"d4"}})
	require.NoError(t, err)
	require.Len(t, queries, 3)
	got := map[string]map[string][]string{}
	for _, q := range queries {
		ids := map[string][]string{}
		for _, row := range dbtest.QueryRows[[]any](t, db, q.SQL, q.Args...) {
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

	_, err = r.Devices.ParseList(url.Values{filter.ParamInclude: {"type"}})
	require.Error(t, err, "a belongs-to is not includable")
	plan, err = r.Devices.BuildList(req, parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	_, err = r.Devices.IncludeQueries(plan, []parse.Key{{"d1"}})
	assert.ErrorIs(t, err, filter.ErrRelatedPolicyRequired, "a policy-scoped include needs the target's policy")
}

// runRelationFacets counts t1's devices per visible related key.
func runRelationFacets(t *testing.T, r parsetest.RelResources, db *sql.DB) {
	t.Helper()
	req, err := r.Devices.ParseList(url.Values{filter.ParamFacets: {"commands,tags,shared_types"}})
	require.NoError(t, err)
	plan, err := r.Devices.BuildList(req, parsetest.RelationScopeT1())
	require.NoError(t, err)
	require.NotNil(t, plan.Facets)
	got := map[string]map[string]int64{}
	for _, row := range dbtest.QueryRows[[]any](t, db, plan.Facets.SQL, plan.Facets.Args...) {
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
func runRelationUnknownPolicy(t *testing.T, r parsetest.RelResources, db *sql.DB) {
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
		return rowIDs(t, dbtest.QueryRows[[]any](t, db, sql, args...))
	}

	t.Run("relation filter", func(t *testing.T) {
		plan := list(t, r.Devices, url.Values{filter.ParamFilter: {`{"type":{"$is":{}}}`}}, unknown("device_types"))
		assert.Empty(t, ids(t, plan.Page.SQL, plan.Page.Args), plan.Page.SQL)
	})
	t.Run("include", func(t *testing.T) {
		plan := list(t, r.Devices, url.Values{filter.ParamInclude: {"shared_types"}}, unknown("device_types"))
		queries, err := r.Devices.IncludeQueries(plan, []parse.Key{{"d1"}, {"d2"}, {"d3"}, {"d4"}})
		require.NoError(t, err)
		require.Len(t, queries, 1)
		assert.Empty(t, dbtest.QueryRows[[]any](t, db, queries[0].SQL, queries[0].Args...), queries[0].SQL)
	})
	t.Run("facet", func(t *testing.T) {
		plan := list(t, r.Devices, url.Values{filter.ParamFacets: {"shared_types"}}, unknown("device_types"))
		require.NotNil(t, plan.Facets)
		assert.Empty(t, dbtest.QueryRows[[]any](t, db, plan.Facets.SQL, plan.Facets.Args...), plan.Facets.SQL)
	})

	// A second resource over the assets table relates each asset to its
	// parent through the policy, so the target is the owner's own table.
	assets := parse.MustResource[parsetest.RelAsset](r.Assets.Provider(), "assets", parse.TenantColumn("tenant_id"))
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
func runRelationWrites(t *testing.T, r parsetest.RelResources, db *sql.DB) {
	t.Helper()
	apply := func(id, body string, merge bool) {
		t.Helper()
		var p *patch.ParsedPatch
		var err error
		if merge {
			p, err = r.Devices.ParseMergePatch([]byte(body))
		} else {
			p, err = r.Devices.ParsePatch([]byte(body))
		}
		require.NoError(t, err, body)
		_, err = r.Devices.Update(t.Context(), p, parse.ByID(id), parsetest.RelationScopeT1())
		require.NoError(t, err, body)
	}
	pairs := func(table, column, device string) []string {
		t.Helper()
		return dbtest.QueryRows[string](t, db, "SELECT "+column+" FROM "+table+" WHERE device_id = '"+device+"' ORDER BY "+column)
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

// Relations resolved outside SQL, held to what SQL answers: a backend that
// holds rows in memory resolves a filter's relation predicates
// (filter.ResolveMemberships) and includes (filter.ResolveInclude) through
// lookups, then evaluates the filter on each row. Each case runs through
// two lookups over one database: its tables loaded into memory, answered
// by filter.MatchLookupRow and filter.Evaluate (governedMemory), and the
// database running the SQL each lookup renders (sqlMembershipLookup).

// resolvedLookup is a store that answers every lookup a relation resolves
// through.
type resolvedLookup interface {
	filter.RelationLookup
	filter.IncludeLookup
}

// resolvedStores loads tables into memory and names each lookup.
func resolvedStores(t *testing.T, db *sql.DB, d filter.Dialect, tables ...string) (governedMemory, map[string]resolvedLookup) {
	t.Helper()
	store := governedMemory{}
	for _, table := range tables {
		store[table] = append(store[table], dbtest.QueryRows[map[string]any](t, db, "SELECT * FROM "+d.QuoteIdent(table))...)
	}
	lookups := map[string]resolvedLookup{
		"memory": store,
		"sql":    sqlMembershipLookup{t: t, d: d, db: db},
	}
	return store, lookups
}

// assertResolvedFilters requires each filter document to select the rows
// of r outside SQL that it selects in SQL on r's provider, through every
// lookup.
func assertResolvedFilters[T any](t *testing.T, store governedMemory, lookups map[string]resolvedLookup, r *parse.Resource[T], docs ...string) {
	t.Helper()
	types := resourceKeyTypes(t, r)
	for _, doc := range docs {
		var want []string
		for _, item := range crxPage(t, r, url.Values{filter.ParamFilter: {doc}}).Items {
			key, err := r.Key(&item)
			require.NoError(t, err)
			want = append(want, keyText(t, types, key))
		}
		slices.Sort(want)
		for name, lookup := range lookups {
			t.Run(r.Table()+" "+doc+" "+name, func(t *testing.T) {
				req, err := r.ParseList(url.Values{filter.ParamFilter: {doc}})
				require.NoError(t, err)
				resolved, err := filter.ResolveMemberships(t.Context(), req.Filter(), lookup)
				require.NoError(t, err)
				var got []string
				for _, row := range store[r.Table()] {
					truth, err := filter.Evaluate(resolved.AST().Root, governedRow(r.Table(), row))
					require.NoError(t, err)
					if truth == filter.True {
						got = append(got, keyText(t, types, rowKey(r, row)))
					}
				}
				slices.Sort(got)
				assert.Equal(t, want, got)
			})
		}
	}
}

// assertResolvedIncludes requires each include of every row of r outside
// SQL to name the owners and targets SQL on r's provider names, through
// every lookup.
func assertResolvedIncludes[T any](t *testing.T, lookups map[string]resolvedLookup, r *parse.Resource[T], fields ...string) {
	t.Helper()
	page := crxPage(t, r, url.Values{filter.ParamInclude: {strings.Join(fields, ",")}})
	owners := make([]core.Key, len(page.Items))
	for i := range page.Items {
		key, err := r.Key(&page.Items[i])
		require.NoError(t, err)
		owners[i] = key
	}
	schema := r.FilterSchema()
	for _, field := range fields {
		for name, lookup := range lookups {
			t.Run(r.Table()+" includes "+field+" "+name, func(t *testing.T) {
				rows, err := filter.ResolveInclude(t.Context(), schema.Fields[field], owners, lookup)
				require.NoError(t, err)
				assert.Empty(t, rows.Truncated)
				want := page.Relations[field]
				if want == nil {
					want = map[string][]string{}
				}
				assert.Equal(t, want, rows.IDs)
			})
		}
	}
}

// resourceKeyTypes are the types of r's key columns, as its filter schema
// declares them.
func resourceKeyTypes[T any](t *testing.T, r *parse.Resource[T]) []core.TypeInfo {
	t.Helper()
	schema := r.FilterSchema()
	var types []core.TypeInfo
	for _, column := range r.PrimaryKeyColumns() {
		var field *filter.FieldSchema
		for _, f := range schema.Fields {
			if f.Column == column && f.Relation == nil && f.Path == nil {
				field = f
			}
		}
		require.NotNil(t, field, "key column %s has no filter field", column)
		types = append(types, core.TypeInfo{Type: field.DataType, NumberKind: field.NumberKind})
	}
	return types
}

// rowKey is a stored row's key, a value per key column of r.
func rowKey[T any](r *parse.Resource[T], row map[string]any) core.Key {
	columns := r.PrimaryKeyColumns()
	key := make(core.Key, len(columns))
	for i, c := range columns {
		key[i] = row[c]
	}
	return key
}

func keyText(t *testing.T, types []core.TypeInfo, key core.Key) string {
	t.Helper()
	text, err := core.KeyTextOf(types, key)
	require.NoError(t, err)
	return text
}
