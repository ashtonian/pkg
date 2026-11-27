//go:build integration

package corpus

import (
	"database/sql"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

// The products fixture runs the builders end to end on each SQL engine: a
// filter parsed, compiled and executed, and a list's page, count and
// facets. The Memory engine, which compiles nothing, lists the same
// requests through a resource of the same fields (product), and must
// select, order, count and facet the same rows.

// product is a row of the products, its fields productSchema's.
type product struct {
	ID       int64   `json:"id"       db:"id"`
	Name     string  `json:"name"     db:"name"     parse:"filter,sort" filter_ops:"eq,like,ilike"`
	Category string  `json:"category" db:"category" parse:"filter,facet" filter_ops:"eq,in"`
	Price    float64 `json:"price"    db:"price"    parse:"filter,sort" filter_ops:"eq,gt,gte,lt,lte"`
	Status   string  `json:"status"   db:"status"   parse:"filter" filter_ops:"eq,ne,in"`
}

// productsDB is a fresh database of b holding the products: three
// electronics (Keyboard inactive), two clothing (Jacket inactive) and two
// books. On the Memory engine, products is the resource listing them; on a
// SQL engine it is nil.
func productsDB(t *testing.T, b sqlBackend) (products *parse.Resource[product], db *sql.DB) {
	t.Helper()
	p, db := b.open(t)
	if b.engine == Memory {
		products = parse.MustResource[product](p, "products")
	}
	table := sqlTable{name: "products", key: []string{"id"}, columns: []sqlColumn{
		column[int64]("id"), column[string]("name").required(), column[string]("category").required(),
		column[float64]("price").required(), column[string]("status").required(),
	}}
	b.load(t, db, table, [][]any{
		{int64(1), "Laptop", "electronics", 999.99, "active"},
		{int64(2), "Mouse", "electronics", 29.99, "active"},
		{int64(3), "Keyboard", "electronics", 89.99, "inactive"},
		{int64(4), "T-Shirt", "clothing", 19.99, "active"},
		{int64(5), "Jacket", "clothing", 65.00, "inactive"},
		{int64(6), "Go Book", "books", 39.99, "active"},
		{int64(7), "Clean Code", "books", 32.00, "active"},
	})
	return products, db
}

// productPage lists in under s through products.
func productPage(t *testing.T, products *parse.Resource[product], in parse.ListInput, s parse.Scope) *filter.Page[product] {
	t.Helper()
	req, err := products.ParseListInput(in)
	require.NoError(t, err)
	return listPage(t, products, req, s)
}

// productNames is the names of page's products, in order.
func productNames(page *filter.Page[product]) []string {
	return parsetest.ItemIDs(page.Items, func(p product) string { return p.Name })
}

func productSchema() *filter.ResourceSchema {
	schema := &filter.ResourceSchema{
		Name:       "products",
		PrimaryKey: []string{"id"},
		SystemFields: map[string]*filter.FieldSchema{
			"id": {ExternalName: "id", Table: "products", Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64},
		},
		Fields: map[string]*filter.FieldSchema{
			"name":     {ExternalName: "name", Table: "products", Column: "name", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpLike, filter.OpILike}},
			"category": {ExternalName: "category", Table: "products", Column: "category", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpIn}, Facetable: true},
			"price":    {ExternalName: "price", Table: "products", Column: "price", DataType: core.FieldTypeFloat, AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpGt, filter.OpGte, filter.OpLt, filter.OpLte}},
			"status":   {ExternalName: "status", Table: "products", Column: "status", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpIn}, EnumValues: []string{"active", "inactive"}},
		},
	}
	return schema
}

func productSortSchema() *filter.SortSchema {
	sorts := &filter.SortSchema{
		Fields: map[string]*filter.SortFieldSchema{
			"name":  {Table: "products", Column: "name"},
			"price": {Table: "products", Column: "price"},
		},
		DefaultField: "name",
		DefaultDir:   filter.SortAsc,
	}
	return sorts
}

// filteredProducts is the names of the products input, a filter, selects
// on a fresh database of b.
func filteredProducts(t *testing.T, b sqlBackend, input string) []string {
	t.Helper()
	products, db := productsDB(t, b)
	if b.engine == Memory {
		return productNames(productPage(t, products, parse.ListInput{Filter: input, PageSize: 100}, parse.Scope{}))
	}
	schema := productSchema()
	ast, err := filter.NewFilterParser(schema, nil).Parse(input)
	require.NoError(t, err)
	result, err := filter.NewSQLBuilder(schema, b.dialect).Build(ast)
	require.NoError(t, err)
	return dbtest.QueryRows[string](t, db, "SELECT name FROM products WHERE "+result.Where, result.Args...)
}

func integrationSimpleEq(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		assert.ElementsMatch(t, []string{"Laptop", "Mouse", "Keyboard"}, filteredProducts(t, b, `{"category":"electronics"}`))
	})
}

func integrationMultiCondition(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		assert.Equal(t, []string{"Mouse"}, filteredProducts(t, b, `{"category":"electronics","price":{"$lte":50}}`))
	})
}

func integrationInOperator(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		assert.ElementsMatch(t, []string{"T-Shirt", "Jacket", "Go Book", "Clean Code"}, filteredProducts(t, b, `{"category":{"$in":["books","clothing"]}}`))
	})
}

func integrationOrOperator(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		assert.ElementsMatch(t, []string{"Laptop", "Go Book", "Clean Code"}, filteredProducts(t, b, `{"$or":[{"category":"books"},{"price":{"$gt":500}}]}`))
	})
}

func integrationNotOperator(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		assert.ElementsMatch(t, []string{"Laptop", "Mouse", "T-Shirt", "Go Book", "Clean Code"}, filteredProducts(t, b, `{"$not":{"status":"inactive"}}`))
	})
}

// A list page holds the cheapest active products, read with one row more
// than the page for has-more detection.
func integrationListQuerySortAndLimit(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		products, db := productsDB(t, b)
		if b.engine == Memory {
			page := productPage(t, products, parse.ListInput{Filter: `{"status":"active"}`, Sort: "price", PageSize: 3}, parse.Scope{})
			assert.Equal(t, []string{"T-Shirt", "Mouse", "Clean Code"}, productNames(page), "cheapest first")
			assert.True(t, page.PageInfo.HasNextPage, "two active products more")
			return
		}
		lq := mustListQuery(t, productSchema(), productSortSchema(), b.dialect)
		result, err := lq.Build(
			filter.ListFilter(`{"status":"active"}`),
			filter.ListSort("price"),
			filter.ListLimit(3),
		)
		require.NoError(t, err)
		names := dbtest.QueryRows[string](t, db, "SELECT products.name FROM products "+result.Fragment, result.Args...)
		require.LessOrEqual(t, len(names), result.FetchLimit, "at most the fetch limit: %v", names)
		require.GreaterOrEqual(t, len(names), result.Limit, "a full page: %v", names)
		assert.Equal(t, []string{"T-Shirt", "Mouse", "Clean Code"}, names[:result.Limit], "cheapest first")
	})
}

// A policy constraint narrows the user's filter: of the electronics, the
// active ones.
func integrationListQueryPolicyConstraints(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		products, db := productsDB(t, b)
		pc := &filter.PolicyConstraints{
			Constraints: []*filter.PolicyConstraint{
				{Field: "status", Operator: filter.OpEq, Value: "active"},
			},
		}
		if b.engine == Memory {
			page := productPage(t, products, parse.ListInput{Filter: `{"category":"electronics"}`, PageSize: 100}, parse.Scope{Policy: pc})
			assert.ElementsMatch(t, []string{"Laptop", "Mouse"}, productNames(page), "Keyboard is inactive")
			return
		}
		result, err := mustListQuery(t, productSchema(), nil, b.dialect).Build(
			filter.ListFilter(`{"category":"electronics"}`),
			filter.ListPolicy(pc),
			filter.ListLimit(100),
		)
		require.NoError(t, err)
		names := dbtest.QueryRows[string](t, db, "SELECT products.name FROM products "+result.Fragment, result.Args...)
		assert.ElementsMatch(t, []string{"Laptop", "Mouse"}, names, "Keyboard is inactive")
	})
}

// A list's count statement counts every row the filter selects, whatever
// the page's limit.
func integrationListQueryWithCount(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		products, db := productsDB(t, b)
		if b.engine == Memory {
			page := productPage(t, products, parse.ListInput{Filter: `{"status":"active"}`, IncludeTotal: true, PageSize: 2}, parse.Scope{})
			require.NotNil(t, page.PageInfo.TotalCount)
			assert.Equal(t, int64(5), *page.PageInfo.TotalCount)
			return
		}
		result, err := mustListQuery(t, productSchema(), nil, b.dialect).Build(
			filter.ListFilter(`{"status":"active"}`),
			filter.ListWithCount(true),
			filter.ListLimit(2),
		)
		require.NoError(t, err)
		assert.Equal(t, []int64{5}, dbtest.QueryRows[int64](t, db, result.CountSQL, result.CountArgs...))
	})
}

// A WHERE numbered after an UPDATE's SET parameters (WithStartParam) binds
// after them: the electronics are renamed and no other row.
func integrationWithStartParam(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		_, db := productsDB(t, b)
		schema := productSchema()
		setArgs := []any{"updated_name", "active"}
		ast, err := filter.NewFilterParser(schema, nil).Parse(`{"category":"electronics"}`)
		require.NoError(t, err)
		result, err := filter.NewSQLBuilder(schema, b.dialect, filter.WithStartParam(len(setArgs))).Build(ast)
		require.NoError(t, err)
		args := slices.Concat(setArgs, result.Args)
		query := "UPDATE products SET name = " + b.dialect.Placeholder(1) + ", status = " + b.dialect.Placeholder(2) + " WHERE " + result.Where + " RETURNING name"
		assert.Equal(t, []string{"updated_name", "updated_name", "updated_name"}, dbtest.QueryRows[string](t, db, query, args...), query)
	})
}

// A list's facet statement counts the rows its filter selects by category.
func integrationFacets(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		products, db := productsDB(t, b)
		if b.engine == Memory {
			page := productPage(t, products, parse.ListInput{Filter: `{"status":"active"}`, Facets: []string{"category"}, PageSize: 100}, parse.Scope{})
			assert.Equal(t, map[string][]filter.FacetBucket{"category": {{Key: "books", Count: 2}, {Key: "electronics", Count: 2}, {Key: "clothing", Count: 1}}}, page.Facets)
			return
		}
		result, err := mustListQuery(t, productSchema(), nil, b.dialect).Build(
			filter.ListFilter(`{"status":"active"}`),
			filter.ListFacets("category"),
			filter.ListLimit(100),
		)
		require.NoError(t, err)
		require.NotNil(t, result.Facets, "a facet query")
		facets := queryFacets(t, db, result.Facets.SQL, result.Facets.Args...)
		assert.Equal(t, map[string]map[string]int64{"category": {"electronics": 2, "clothing": 1, "books": 2}}, bucketCounts(facets))
	})
}
