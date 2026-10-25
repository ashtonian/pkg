//go:build unit

package parse_test

import (
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// Product is the resource the README's request → SQL examples compile
// against. Its relation fields are virtual (db:"-"): Collections is a
// many-to-many through product_collections; Reviews is a one-to-many whose
// child table stands in the junction's place, so it filters and includes
// but must never take patch.
type Product struct {
	ID          uuid.UUID      `json:"id"          db:"id"          parse:"filter,sort"`
	Name        string         `json:"name"        db:"name"        parse:"filter,sort,patch,search" search_weight:"A"`
	Category    string         `json:"category"    db:"category"    parse:"filter,facet,patch" facet:"terms:10" enum:"tools,books,garden" indexed:"true"`
	Price       float64        `json:"price"       db:"price"       parse:"filter,sort,facet,patch" facet:"range:0-50,50-100,100+"`
	Stock       *int64         `json:"stock"       db:"stock"       parse:"filter,patch" filter_ops:"eq,gt,lt,null"`
	Tags        []string       `json:"tags"        db:"tags"        parse:"filter,patch" indexed:"true"`
	Attrs       map[string]any `json:"attrs"       db:"attrs"       parse:"filter,patch" indexed:"true"`
	SupplierID  uuid.UUID      `json:"supplier_id" db:"supplier_id" parse:"filter" indexed:"true"`
	CreatedAt   time.Time      `json:"created_at"  db:"created_at"  parse:"filter,sort" sort_default:"desc"`
	Collections []uuid.UUID    `json:"collections" db:"-" parse:"filter,patch,include" relation:"product_collections,product_id,collection_id" indexed:"true"`
	Reviews     []uuid.UUID    `json:"reviews"     db:"-" parse:"filter,include" relation:"reviews,product_id,id" indexed:"true"`
}

// products is the parser the examples share, with a fixed clock so
// relative times print the same every run.
func products() *parse.Resource[Product] {
	clock := func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }
	tp, err := parse.NewResource[Product]("products", parse.ParserOptions(core.WithClock(clock)))
	if err != nil {
		panic(err)
	}
	return tp
}

// show compiles one list request for both dialects and prints its WHERE,
// ORDER BY and bound arguments.
func show(tp *parse.Resource[Product], opts ...filter.ListOption) {
	for _, d := range []filter.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		res, err := tp.ListQuery(d).Build(opts...)
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		name := "postgres"
		if _, ok := d.(filter.SQLiteDialect); ok {
			name = "sqlite  "
		}
		fmt.Printf("%s WHERE %s %s %v\n", name, res.Where, res.OrderBy, res.Args)
	}
}

// Example_atAGlance is the README's first example: a filter and a sort,
// with a unique tie-breaker for the cursors.
func Example_atAGlance() {
	show(products(),
		filter.ListFilter(`{"price":{"$gte":10,"$lt":100},"stock":{"$null":false}}`),
		filter.ListSort("-price"), filter.ListLimit(20))
	// Output:
	// postgres WHERE ((products.price >= $1 AND products.price < $2) AND products.stock IS NOT NULL) ORDER BY products.price DESC, products.id DESC [10 100]
	// sqlite   WHERE ((products.price >= ?1 AND products.price < ?2) AND products.stock IS NOT NULL) ORDER BY products.price DESC, products.id DESC [10 100]
}

// Example_filterOperators: comparison, membership, pattern, NULL and boolean
// operators, and a relative time resolved against the parser clock.
func Example_filterOperators() {
	tp := products()
	show(tp, filter.ListFilter(`{"category":"tools","name":{"$in":["saw","drill"]}}`))
	show(tp, filter.ListFilter(`{"price":{"$gte":10,"$lt":100},"stock":{"$null":false}}`))
	show(tp, filter.ListFilter(`{"$or":[{"category":"books"},{"$not":{"price":{"$gt":50}}}]}`))
	show(tp, filter.ListFilter(`{"name":{"$ilike":"%pro%"},"created_at":{"$gte":"now-7d"}}`))
	// Output:
	// postgres WHERE (products.category = $1 AND products.name IN ($2, $3)) ORDER BY products.created_at DESC, products.id DESC [tools saw drill]
	// sqlite   WHERE (products.category = ?1 AND products.name IN (?2, ?3)) ORDER BY products.created_at DESC, products.id DESC [tools saw drill]
	// postgres WHERE ((products.price >= $1 AND products.price < $2) AND products.stock IS NOT NULL) ORDER BY products.created_at DESC, products.id DESC [10 100]
	// sqlite   WHERE ((products.price >= ?1 AND products.price < ?2) AND products.stock IS NOT NULL) ORDER BY products.created_at DESC, products.id DESC [10 100]
	// postgres WHERE (products.category = $1 OR NOT (products.price > $2)) ORDER BY products.created_at DESC, products.id DESC [books 50]
	// sqlite   WHERE (products.category = ?1 OR NOT (products.price > ?2)) ORDER BY products.created_at DESC, products.id DESC [books 50]
	// postgres WHERE (products.created_at >= $1 AND products.name ILIKE $2 ESCAPE '\') ORDER BY products.created_at DESC, products.id DESC [2026-10-07T12:00:00.000000000Z %pro%]
	// sqlite   WHERE (products.created_at >= ?1 AND products.name LIKE ?2 ESCAPE '\') ORDER BY products.created_at DESC, products.id DESC [2026-10-07T12:00:00.000000000Z %pro%]
}

// Example_flatQueryParameters: field parameters (status=active,
// price_gte=10) parse to the same filter a JSON filter does.
func Example_flatQueryParameters() {
	tp := products()
	ast, err := filter.NewQueryParamParser(tp.FilterSchema()).Parse(url.Values{
		"category":  {"tools"},
		"price_gte": {"10"},
		"stock_lt":  {"5"},
	})
	if err != nil {
		panic(err)
	}
	show(tp, filter.ListFilterAST(ast))
	// Output:
	// postgres WHERE (products.category = $1 AND products.price >= $2 AND products.stock < $3) ORDER BY products.created_at DESC, products.id DESC [tools 10 5]
	// sqlite   WHERE (products.category = ?1 AND products.price >= ?2 AND products.stock < ?3) ORDER BY products.created_at DESC, products.id DESC [tools 10 5]
}

// Example_keysetPages: a sort plus a unique tie-breaker gives a total
// order; the next page seeks past the last row's key instead of offsetting.
func Example_keysetPages() {
	tp := products()
	first, err := tp.ListQuery(filter.PostgresDialect{}).Build(
		filter.ListSort("-price"), filter.ListLimit(2))
	if err != nil {
		panic(err)
	}
	fmt.Println("first:", first.Fragment, first.Args)

	// The rows the first page fetched: limit + 1, so the page knows a next one exists.
	rows := []Product{
		{ID: uuid.MustParse("0190f3c2-0000-7000-8000-000000000011"), Price: 99.5},
		{ID: uuid.MustParse("0190f3c2-0000-7000-8000-000000000012"), Price: 80},
		{ID: uuid.MustParse("0190f3c2-0000-7000-8000-000000000013"), Price: 70},
	}
	page, err := filter.NewPage(rows, first.Limit, first.Cursor, func(p Product, col string) (any, error) {
		return filter.CursorValueByTag(p, "db", col)
	})
	if err != nil {
		panic(err)
	}
	fmt.Println("items:", len(page.Items), "has_next_page:", page.PageInfo.HasNextPage)
	show(tp, filter.ListSort("-price"), filter.ListLimit(2),
		filter.ListCursor(string(page.PageInfo.EndCursor)))
	// Output:
	// first: WHERE 1=1 ORDER BY products.price DESC, products.id DESC LIMIT 3 []
	// items: 2 has_next_page: true
	// postgres WHERE (1=1 AND (products.price, products.id) < ($1, $2)) ORDER BY products.price DESC, products.id DESC [80 0190f3c2-0000-7000-8000-000000000012]
	// sqlite   WHERE (1=1 AND ((products.price < ?1) OR (products.price = ?1 AND products.id < ?2))) ORDER BY products.price DESC, products.id DESC [80 0190f3c2-0000-7000-8000-000000000012]
}

// Example_countFacetsSearch: one request can also produce its total, its
// facet counts and a full-text predicate, all over the same filter.
func Example_countFacetsSearch() {
	tp := products()
	res, err := tp.ListQuery(filter.PostgresDialect{}).Build(
		filter.ListFilter(`{"price":{"$lt":200}}`),
		filter.ListSearch("cordless drill"),
		filter.ListWithCount(true),
		filter.ListFacets("category"),
	)
	if err != nil {
		panic(err)
	}
	fmt.Println("where:", res.Where, res.Args)
	fmt.Println("count:", res.CountSQL, res.CountArgs)
	fmt.Println("facets:", res.Facets.SQL, res.Facets.Args)
	// Output:
	// where: (products.price < $1 AND to_tsvector('english', coalesce(products.name, '')) @@ plainto_tsquery('english', $2)) [200 cordless drill]
	// count: SELECT COUNT(*) FROM products WHERE (products.price < $1 AND to_tsvector('english', coalesce(products.name, '')) @@ plainto_tsquery('english', $2)) [200 cordless drill]
	// facets: WITH base AS MATERIALIZED (SELECT products.category AS facet__0 FROM products WHERE (products.price < $1 AND to_tsvector('english', coalesce(products.name, '')) @@ plainto_tsquery('english', $2))) SELECT facet_field, facet_key, facet_count FROM (SELECT 'category' AS facet_field, k AS facet_key, c AS facet_count, 0 AS facet_ord, 0 AS facet_rank FROM (SELECT facet__0 AS k, COUNT(*) AS c FROM base WHERE facet__0 IS NOT NULL GROUP BY facet__0 ORDER BY c DESC, facet__0 COLLATE pg_c_utf8 LIMIT 10) s0) facets__ ORDER BY facet_ord, facet_rank, facet_count DESC, facet_key COLLATE pg_c_utf8 [200 cordless drill]
}

// Example_relations: a many-to-many filter, a one-to-many filter through
// the child table, and a belongs-to, which is the plain foreign key.
func Example_relations() {
	tp := products()
	show(tp, filter.ListFilter(`{"collections":"0190f3c2-0000-7000-8000-000000000001"}`))
	show(tp, filter.ListFilter(`{"collections":{"$nin":["0190f3c2-0000-7000-8000-000000000001"]}}`))
	show(tp, filter.ListFilter(`{"reviews":{"$null":false}}`))
	show(tp, filter.ListFilter(`{"supplier_id":"0190f3c2-0000-7000-8000-0000000000aa"}`))
	// Output:
	// postgres WHERE EXISTS (SELECT 1 FROM product_collections rel__ WHERE rel__.product_id = products.id AND rel__.collection_id = $1) ORDER BY products.created_at DESC, products.id DESC [0190f3c2-0000-7000-8000-000000000001]
	// sqlite   WHERE EXISTS (SELECT 1 FROM product_collections rel__ WHERE rel__.product_id = products.id AND rel__.collection_id = ?1) ORDER BY products.created_at DESC, products.id DESC [0190f3c2-0000-7000-8000-000000000001]
	// postgres WHERE NOT EXISTS (SELECT 1 FROM product_collections rel__ WHERE rel__.product_id = products.id AND rel__.collection_id IN ($1)) ORDER BY products.created_at DESC, products.id DESC [0190f3c2-0000-7000-8000-000000000001]
	// sqlite   WHERE NOT EXISTS (SELECT 1 FROM product_collections rel__ WHERE rel__.product_id = products.id AND rel__.collection_id IN (?1)) ORDER BY products.created_at DESC, products.id DESC [0190f3c2-0000-7000-8000-000000000001]
	// postgres WHERE EXISTS (SELECT 1 FROM reviews rel__ WHERE rel__.product_id = products.id) ORDER BY products.created_at DESC, products.id DESC []
	// sqlite   WHERE EXISTS (SELECT 1 FROM reviews rel__ WHERE rel__.product_id = products.id) ORDER BY products.created_at DESC, products.id DESC []
	// postgres WHERE products.supplier_id = $1 ORDER BY products.created_at DESC, products.id DESC [0190f3c2-0000-7000-8000-0000000000aa]
	// sqlite   WHERE products.supplier_id = ?1 ORDER BY products.created_at DESC, products.id DESC [0190f3c2-0000-7000-8000-0000000000aa]
}

// Example_include: the related IDs for a page's owners, capped per owner
// in SQL.
func Example_include() {
	tp := products()
	q, err := filter.BuildIncludeQuery(tp.FilterSchema().Fields["collections"], filter.PostgresDialect{},
		[]any{"0190f3c2-0000-7000-8000-000000000011", "0190f3c2-0000-7000-8000-000000000012"})
	if err != nil {
		panic(err)
	}
	fmt.Println(q.SQL)
	fmt.Println(q.Args)
	// Output:
	// SELECT CAST(include__.owner_pk AS TEXT), CAST(include__.target_id AS TEXT) FROM (SELECT owner_pk, target_id, ROW_NUMBER() OVER (PARTITION BY owner_pk ORDER BY target_id) AS include_rank FROM (SELECT DISTINCT j.product_id AS owner_pk, j.collection_id AS target_id FROM product_collections j WHERE j.product_id IN ($1, $2)) pairs__) include__ WHERE include_rank <= 101 ORDER BY include__.owner_pk, include__.target_id
	// [0190f3c2-0000-7000-8000-000000000011 0190f3c2-0000-7000-8000-000000000012]
}

// Example_jsonDocuments: a JSON column is compared or contained as a whole
// document; a plain object is equality, $contains is jsonb @> at any depth.
func Example_jsonDocuments() {
	tp := products()
	show(tp, filter.ListFilter(`{"attrs":{"$contains":{"color":"red","dims":{"w":10}}}}`))
	show(tp, filter.ListFilter(`{"tags":{"$contains":["sale"]}}`))
	_, err := tp.ParseFilter(`{"attrs.color":"red"}`)
	fmt.Println(err)
	// Output:
	// postgres WHERE products.attrs @> $1::jsonb ORDER BY products.created_at DESC, products.id DESC [{"color":"red","dims":{"w":10}}]
	// sqlite   WHERE (CASE WHEN products.attrs IS NULL THEN NULL ELSE (CASE WHEN json_type(products.attrs) = 'object' THEN (EXISTS (SELECT 1 FROM json_each(products.attrs) je1 WHERE je1.key = ?1 AND (je1.type = 'text' AND je1.atom = ?2)) AND EXISTS (SELECT 1 FROM json_each(products.attrs) je2 WHERE je2.key = ?3 AND (CASE WHEN je2.type = 'object' THEN (EXISTS (SELECT 1 FROM json_each(je2.value) je3 WHERE je3.key = ?4 AND (je3.type IN ('integer', 'real') AND je3.atom = ?5))) ELSE 0 END))) ELSE 0 END) END) ORDER BY products.created_at DESC, products.id DESC [color red dims w 10]
	// postgres WHERE products.tags @> $1 ORDER BY products.created_at DESC, products.id DESC [[sale]]
	// sqlite   WHERE (CASE WHEN products.tags IS NULL THEN NULL ELSE (CASE WHEN json_type(products.tags) = 'array' THEN (EXISTS (SELECT 1 FROM json_each(products.tags) je1 WHERE (je1.type = 'text' AND je1.atom = ?1))) ELSE 0 END) END) ORDER BY products.created_at DESC, products.id DESC [sale]
	// [E2071] field 'attrs.color' is not filterable
}

// Example_patches: a merge patch and a JSON Patch, each compiled to one
// SET clause with one expression per column.
func Example_patches() {
	tp := products()
	merge, err := tp.ParseMergePatch([]byte(`{"name":"Cordless drill","stock":null,"attrs":{"color":"blue","legacy":null}}`))
	if err != nil {
		panic(err)
	}
	for _, d := range []patch.SQLDialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		set, buildErr := patch.BuildPatchSQL(merge, d)
		if buildErr != nil {
			panic(buildErr)
		}
		fmt.Println(set.SetClause, set.Args)
	}
	ops, err := tp.ParsePatch([]byte(`[{"op":"array_append","path":"/tags","value":"clearance"},{"op":"replace","path":"/price","value":19.99}]`))
	if err != nil {
		panic(err)
	}
	set, err := patch.BuildPatchSQL(ops, filter.PostgresDialect{})
	if err != nil {
		panic(err)
	}
	fmt.Println(set.SetClause, set.Args)
	_, err = tp.ParsePatch([]byte(`[{"op":"add","path":"/reviews","value":["0190f3c2-0000-7000-8000-000000000002"]}]`))
	fmt.Println(err)
	// Output:
	// attrs = (COALESCE(jsonb_path_query_first(attrs, 'strict $ ? (@.type() == "object")'), '{}'::jsonb) - $1::text[] || $2::jsonb), name = $3, stock = NULL [[legacy] {"color":"blue"} Cordless drill]
	// attrs = json_patch(COALESCE(attrs, '{}'), ?1), name = ?2, stock = NULL [{"color":"blue","legacy":null} Cordless drill]
	// tags = array_append(COALESCE(tags, '{}'), $1), price = $2 [clearance 19.99]
	// [E2076] path '/reviews' at index 0: path not allowed
}

// ParseRequestFilter ANDs the JSON filter with the flat field parameters and
// bounds the combination as one filter: each part may be within MaxCost and
// the two together over it.
func TestParseRequestFilter(t *testing.T) {
	tp := products()
	parsed, err := tp.ParseRequestFilter(url.Values{
		"filter":    {`{"category":"tools"}`},
		"price_gte": {"10"},
		"sort":      {"-price"}, // a list parameter, not a filter
		"debug":     {"true"},   // the endpoint's own parameter
	}, filter.QueryParamAccept("debug"))
	require.NoError(t, err)
	res, err := tp.ListQuery(filter.PostgresDialect{}).Build(filter.ListFilterAST(parsed.AST))
	require.NoError(t, err)
	assert.Equal(t, "(products.category = $1 AND products.price >= $2)", res.Where)
	assert.Equal(t, []any{"tools", float64(10)}, res.Args)

	// Each part is affordable alone; together they are not.
	strict, err := parse.NewResource[Product]("products", parse.ParserOptions(core.WithMaxCost(25)))
	require.NoError(t, err)
	q := url.Values{"filter": {`{"name":{"$like":"a%"}}`}, "price_gte": {"10"}, "created_at_gte": {"2026-01-01T00:00:00Z"}}
	_, err = strict.ParseRequestFilter(url.Values{"filter": q["filter"]})
	require.NoError(t, err)
	_, err = strict.ParseRequestFilter(url.Values{"price_gte": q["price_gte"], "created_at_gte": q["created_at_gte"]})
	require.NoError(t, err)
	_, err = strict.ParseRequestFilter(q)
	assert.True(t, errors.Is(err, filter.ECodeQueryTooExpensive), "combined cost: %v", err)
}
