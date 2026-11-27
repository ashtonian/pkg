//go:build integration

package parse_test

import (
	"database/sql"
	"fmt"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

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
	stock *parse.Resource[crStock]
	moves *parse.Resource[crMove]
	tags  *parse.Resource[crTag]
}

// crxResources builds the graph on p.
func crxResources(t *testing.T, p *parse.Provider) crxGraph {
	t.Helper()
	g := crxGraph{
		bins:  parse.MustResource[crxBin](p, "crx_bins"),
		stock: parse.MustResource[crStock](p, "crx_stock", parse.PrimaryKey("warehouse_id", "sku")),
		moves: parse.MustResource[crMove](p, "crx_moves"),
		tags:  parse.MustResource[crTag](p, "crx_tags"),
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

func TestCompositeKeyRelations(t *testing.T) {
	runSQLBackends(t, runCompositeKeyRelations)
}

func runCompositeKeyRelations(t *testing.T, b sqlBackend) {
	// setup opens a fresh database holding the fixture, with its provider.
	setup := func(t *testing.T) (*parse.Provider, *sql.DB) {
		t.Helper()
		p, db := b.open(t)
		execStatements(t, db, crxFixture()...)
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
	for _, row := range queryRows[[]any](t, db, `SELECT warehouse_id, sku, tag_id FROM crx_stock_tags ORDER BY warehouse_id, sku, tag_id`) {
		pairs = append(pairs, fmt.Sprintf("%v/%v/%v", row[0], row[1], row[2]))
	}
	return pairs
}

func crxStockKey(s crStock) string { return fmt.Sprintf("%d/%s", s.WarehouseID, s.SKU) }
func crxMoveKey(m crMove) string   { return fmt.Sprint(m.ID) }
func crxBinKey(b crxBin) string    { return fmt.Sprint(b.ID) }
func crxTagKey(t crTag) string     { return t.ID }

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
