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
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/patch"
)

type crxBin struct {
	ID   int64  `json:"id"   db:"id"   parse:"filter,sort"`
	Name string `json:"name" db:"name" parse:"filter"`
}

type crxStock struct {
	WarehouseID int64  `json:"warehouse_id" db:"warehouse_id" parse:"filter,sort"`
	SKU         string `json:"sku"          db:"sku"          parse:"filter,sort"`
	Name        string `json:"name"         db:"name"         parse:"filter"`
}

type crxMove struct {
	ID          int64  `json:"id"           db:"id"           parse:"filter,sort"`
	WarehouseID int64  `json:"warehouse_id" db:"warehouse_id" parse:"filter"`
	SKU         string `json:"sku"          db:"sku"          parse:"filter"`
	Code        string `json:"code"         db:"code"         parse:"filter"`
}

type crxTag struct {
	ID    string `json:"id"    db:"id"    parse:"filter,sort"`
	Label string `json:"label" db:"label" parse:"filter"`
}

// crxGraph relates stock, keyed by (warehouse_id, sku), as the owner and as
// the target of a has-many, a belongs-to and a many-to-many each.
type crxGraph struct {
	bins  *parse.Resource[crxBin]
	stock *parse.Resource[crxStock]
	moves *parse.Resource[crxMove]
	tags  *parse.Resource[crxTag]
}

func crxResources(t *testing.T) crxGraph {
	t.Helper()
	g := crxGraph{
		bins:  parse.MustResource[crxBin]("crx_bins"),
		stock: parse.MustResource[crxStock]("crx_stock", parse.PrimaryKey("warehouse_id", "sku")),
		moves: parse.MustResource[crxMove]("crx_moves"),
		tags:  parse.MustResource[crxTag]("crx_tags"),
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
func crxFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
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
	} {
		_, err := db.ExecContext(t.Context(), stmt)
		require.NoError(t, err, stmt)
	}
}

func TestCompositeKeyRelations_SQLite(t *testing.T) {
	testCompositeKeyRelations(t, func(t *testing.T) *sql.DB { return dbtest.SQLite(t) }, filter.SQLiteDialect{})
}

func TestCompositeKeyRelations_Postgres(t *testing.T) {
	testCompositeKeyRelations(t, func(t *testing.T) *sql.DB { return dbtest.Postgres(t) }, filter.PostgresDialect{})
}

func testCompositeKeyRelations(t *testing.T, open func(*testing.T) *sql.DB, d filter.Dialect) {
	g := crxResources(t)
	setup := func(t *testing.T) *sql.DB {
		t.Helper()
		db := open(t)
		crxFixture(t, db)
		return db
	}
	db := setup(t)

	t.Run("filters", func(t *testing.T) {
		for _, tt := range []struct {
			name, filter string
			list         func(t *testing.T, doc string) []string
			want         []string
		}{
			{name: "has-many owner $some", filter: `{"moves":{"$some":{"code":"in"}}}`, list: crxList(db, d, g.stock, crxStockKey), want: []string{"1/a", "2/b"}},
			{name: "has-many owner $none", filter: `{"moves":{"$none":{}}}`, list: crxList(db, d, g.stock, crxStockKey), want: []string{"1/b", "2/a"}},
			{name: "many-to-many owner $some", filter: `{"tags":{"$some":{"label":"Blue"}}}`, list: crxList(db, d, g.stock, crxStockKey), want: []string{"1/a", "2/b"}},
			{name: "many-to-many owner $none", filter: `{"tags":{"$none":{"id":"red"}}}`, list: crxList(db, d, g.stock, crxStockKey), want: []string{"1/b", "2/b"}},
			{name: "many-to-many owner by key", filter: `{"tags":"red"}`, list: crxList(db, d, g.stock, crxStockKey), want: []string{"1/a", "2/a"}},
			{name: "belongs-to owner $is", filter: `{"bin":{"$is":{"name":"north"}}}`, list: crxList(db, d, g.stock, crxStockKey), want: []string{"1/a", "1/b"}},
			{name: "belongs-to target $is", filter: `{"stock":{"$is":{"name":"bolt"}}}`, list: crxList(db, d, g.moves, crxMoveKey), want: []string{"10", "11", "12"}},
			{name: "belongs-to target $is_not", filter: `{"stock":{"$is_not":{"name":"bolt"}}}`, list: crxList(db, d, g.moves, crxMoveKey), want: []string{"13"}},
			{name: "belongs-to target $null", filter: `{"stock":{"$null":true}}`, list: crxList(db, d, g.moves, crxMoveKey), want: []string{"13"}},
			{name: "has-many target $some", filter: `{"stock":{"$some":{"name":"nut"}}}`, list: crxList(db, d, g.bins, crxBinKey), want: []string{"1"}},
			{name: "has-many target $none", filter: `{"stock":{"$none":{}}}`, list: crxList(db, d, g.bins, crxBinKey), want: []string{"3"}},
			{name: "many-to-many target $some", filter: `{"stock":{"$some":{"warehouse_id":2}}}`, list: crxList(db, d, g.tags, crxTagKey), want: []string{"blue", "red"}},
			{name: "many-to-many target $some, none match", filter: `{"stock":{"$some":{"name":"nut"}}}`, list: crxList(db, d, g.tags, crxTagKey), want: nil},
			{name: "many-to-many target $none", filter: `{"stock":{"$none":{"sku":"a"}}}`, list: crxList(db, d, g.tags, crxTagKey), want: []string{"green"}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				assert.Equal(t, tt.want, tt.list(t, tt.filter))
			})
		}
	})

	t.Run("resolved outside SQL", func(t *testing.T) {
		store, lookups := resolvedStores(t, db, d, "crx_bins", "crx_stock", "crx_moves", "crx_tags", "crx_stock_tags")
		assertResolvedFilters(t, db, d, store, lookups, g.stock,
			`{"moves":{"$some":{"code":"in"}}}`, `{"moves":{"$none":{}}}`, `{"tags":{"$some":{"label":"Blue"}}}`,
			`{"tags":{"$none":{"id":"red"}}}`, `{"tags":"red"}`, `{"tags":{"$nin":["red"]}}`, `{"tags":{"$null":true}}`,
			`{"bin":{"$is":{"name":"north"}}}`)
		assertResolvedFilters(t, db, d, store, lookups, g.moves,
			`{"stock":{"$is":{"name":"bolt"}}}`, `{"stock":{"$is_not":{"name":"bolt"}}}`, `{"stock":{"$null":true}}`, `{"stock":{"$null":false}}`)
		assertResolvedFilters(t, db, d, store, lookups, g.bins, `{"stock":{"$some":{"name":"nut"}}}`, `{"stock":{"$none":{}}}`, `{"stock":{"$some":{"moves":{"$none":{}}}}}`)
		assertResolvedFilters(t, db, d, store, lookups, g.tags,
			`{"stock":{"$some":{"warehouse_id":2}}}`, `{"stock":{"$some":{"name":"nut"}}}`, `{"stock":{"$none":{"sku":"a"}}}`)
		assertResolvedIncludes(t, db, d, lookups, g.stock, "moves", "tags")
		assertResolvedIncludes(t, db, d, lookups, g.bins, "stock")
		assertResolvedIncludes(t, db, d, lookups, g.tags, "stock")
	})

	t.Run("includes and facets", func(t *testing.T) {
		stock := crxPage(t, db, d, g.stock, url.Values{filter.ParamInclude: {"moves,tags"}, filter.ParamFacets: {"tags,moves"}})
		assert.Equal(t, map[string][]string{`["1","a"]`: {"10", "11"}, `["2","b"]`: {"12"}}, stock.Relations["moves"])
		assert.Equal(t, map[string][]string{`["1","a"]`: {"blue", "red"}, `["2","a"]`: {"red"}, `["2","b"]`: {"blue"}}, stock.Relations["tags"])
		assert.ElementsMatch(t, []filter.FacetBucket{{Key: "blue", Count: 2}, {Key: "red", Count: 2}}, stock.Facets["tags"])
		assert.ElementsMatch(t, []filter.FacetBucket{{Key: "10", Count: 1}, {Key: "11", Count: 1}, {Key: "12", Count: 1}}, stock.Facets["moves"])

		bins := crxPage(t, db, d, g.bins, url.Values{filter.ParamInclude: {"stock"}, filter.ParamFacets: {"stock"}})
		assert.Equal(t, map[string][]string{"1": {`["1","a"]`, `["1","b"]`}, "2": {`["2","a"]`, `["2","b"]`}}, bins.Relations["stock"])
		assert.ElementsMatch(t, []filter.FacetBucket{
			{Key: `["1","a"]`, Count: 1}, {Key: `["1","b"]`, Count: 1}, {Key: `["2","a"]`, Count: 1}, {Key: `["2","b"]`, Count: 1},
		}, bins.Facets["stock"])

		tags := crxPage(t, db, d, g.tags, url.Values{filter.ParamInclude: {"stock"}, filter.ParamFacets: {"stock"}})
		assert.Equal(t, map[string][]string{"blue": {`["1","a"]`, `["2","b"]`}, "red": {`["1","a"]`, `["2","a"]`}}, tags.Relations["stock"])
		assert.ElementsMatch(t, []filter.FacetBucket{{Key: `["1","a"]`, Count: 2}, {Key: `["2","a"]`, Count: 1}, {Key: `["2","b"]`, Count: 1}}, tags.Facets["stock"])

		// The include names each owner as core.KeyText names its key.
		for _, item := range stock.Items {
			key, err := g.stock.Key(&item)
			require.NoError(t, err)
			if ids, ok := stock.Relations["tags"][core.KeyText(key)]; ok {
				assert.NotEmpty(t, ids)
			}
		}
	})

	t.Run("writes", func(t *testing.T) {
		for _, tt := range []struct {
			name   string
			update func(t *testing.T, db *sql.DB)
			want   []string
		}{
			{
				name: "owner key replaces its targets",
				update: func(t *testing.T, db *sql.DB) {
					crxUpdate(t, db, d, g.stock, `{"tags":["green","red"]}`, parse.ByKey(parse.Key{int64(1), "b"}))
				},
				want: []string{"1/a/blue", "1/a/red", "1/b/green", "1/b/red", "2/a/red", "2/b/blue"},
			},
			{
				name: "owner keys add a target",
				update: func(t *testing.T, db *sql.DB) {
					crxUpdatePatch(t, db, d, g.stock, `[{"op":"add","path":"/tags","value":["green"]}]`, parse.ByKeys(parse.Key{int64(1), "a"}, parse.Key{int64(2), "a"}))
				},
				want: []string{"1/a/blue", "1/a/green", "1/a/red", "2/a/green", "2/a/red", "2/b/blue"},
			},
			{
				name: "target keys replace",
				update: func(t *testing.T, db *sql.DB) {
					crxUpdate(t, db, d, g.tags, `{"stock":[[1,"b"],[2,"b"],[2,"b"]]}`, parse.ByID("red"))
				},
				want: []string{"1/a/blue", "1/b/red", "2/b/blue", "2/b/red"},
			},
			{
				name: "target keys add and remove",
				update: func(t *testing.T, db *sql.DB) {
					crxUpdatePatch(t, db, d, g.tags, `[{"op":"add","path":"/stock","value":[[1,"b"],[2,"a"]]}]`, parse.ByID("blue"))
					crxUpdatePatch(t, db, d, g.tags, `[{"op":"remove","path":"/stock","value":[[1,"a"],[2,"a"]]}]`, parse.ByID("red"))
				},
				want: []string{"1/a/blue", "1/b/blue", "2/a/blue", "2/b/blue"},
			},
			{
				name: "a target key that names no row pairs nothing",
				update: func(t *testing.T, db *sql.DB) {
					crxUpdatePatch(t, db, d, g.tags, `[{"op":"add","path":"/stock","value":[[1,"c"],[3,"a"]]}]`, parse.ByID("green"))
				},
				want: []string{"1/a/blue", "1/a/red", "2/a/red", "2/b/blue"},
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				db := setup(t)
				tt.update(t, db)
				var pairs []string
				for _, row := range queryRows(t, db, `SELECT warehouse_id, sku, tag_id FROM crx_stock_tags ORDER BY warehouse_id, sku, tag_id`, nil) {
					pairs = append(pairs, fmt.Sprintf("%v/%v/%v", row[0], row[1], row[2]))
				}
				assert.Equal(t, tt.want, pairs)
			})
		}
	})
}

func crxStockKey(s crxStock) string { return fmt.Sprintf("%d/%s", s.WarehouseID, s.SKU) }
func crxMoveKey(m crxMove) string   { return fmt.Sprint(m.ID) }
func crxBinKey(b crxBin) string     { return fmt.Sprint(b.ID) }
func crxTagKey(t crxTag) string     { return t.ID }

// crxList lists r's rows a filter document admits, each as key names it,
// in key order.
func crxList[T any](db *sql.DB, d filter.Dialect, r *parse.Resource[T], key func(T) string) func(t *testing.T, doc string) []string {
	return func(t *testing.T, doc string) []string {
		t.Helper()
		var out []string
		for _, item := range crxPage(t, db, d, r, url.Values{filter.ParamFilter: {doc}}).Items {
			out = append(out, key(item))
		}
		return out
	}
}

func crxPage[T any](t *testing.T, db *sql.DB, d filter.Dialect, r *parse.Resource[T], q url.Values) *filter.Page[T] {
	t.Helper()
	req, err := r.ParseList(q)
	require.NoError(t, err)
	plan, err := r.BuildList(d, req, parse.Scope{})
	require.NoError(t, err)
	return listPage(t, db, r, d, plan)
}

// crxUpdate applies a merge patch to the target's rows.
func crxUpdate[T any](t *testing.T, db *sql.DB, d filter.Dialect, r *parse.Resource[T], body string, target parse.Target) {
	t.Helper()
	p, err := r.ParseMergePatch([]byte(body))
	require.NoError(t, err)
	crxApply(t, db, d, r, p, target)
}

// crxUpdatePatch applies a JSON Patch to the target's rows.
func crxUpdatePatch[T any](t *testing.T, db *sql.DB, d filter.Dialect, r *parse.Resource[T], body string, target parse.Target) {
	t.Helper()
	p, err := r.ParsePatch([]byte(body))
	require.NoError(t, err)
	crxApply(t, db, d, r, p, target)
}

func crxApply[T any](t *testing.T, db *sql.DB, d filter.Dialect, r *parse.Resource[T], p *patch.ParsedPatch, target parse.Target) {
	t.Helper()
	plan, err := r.BuildUpdate(d, p, target, parse.Scope{})
	require.NoError(t, err)
	_, err = applyUpdate(t, db, r, d, plan, false)
	require.NoError(t, err)
}
