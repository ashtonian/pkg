//go:build unit

package filter_test

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// Example is the README Quick Start: a generated schema, a parsed filter
// and the SQL each dialect runs.
func Example() {
	type Device struct {
		ID     string         `json:"id"     db:"id"     parse:"filter"`
		Status string         `json:"status" db:"status" parse:"filter" enum:"active,offline"`
		Labels map[string]any `json:"labels" db:"labels" parse:"filter" indexed:"true"`
	}
	schema, err := filter.GenerateFilterSchema[Device](core.NewSchemaGenerator())
	if err != nil {
		panic(err)
	}
	ast, err := filter.NewFilterParser(schema, nil).Parse(
		`{"status":{"$in":["active"]},"labels":{"$contains":{"env":"prod"}}}`)
	if err != nil {
		panic(err)
	}
	pg, err := filter.NewSQLBuilder(schema, filter.PostgresDialect{}).Build(ast)
	if err != nil {
		panic(err)
	}
	fmt.Println(pg.Where, pg.Args)
	lite, err := filter.NewSQLBuilder(schema, filter.SQLiteDialect{}).Build(ast)
	if err != nil {
		panic(err)
	}
	fmt.Println(len(lite.Args), "SQLite args")
	// Output:
	// (devices.labels @> $1::jsonb AND devices.status IN ($2)) [{"env":"prod"} active]
	// 3 SQLite args
}

func ExampleNewSQLBuilder() {
	schema := &filter.ResourceSchema{
		Name: "users",
		Fields: map[string]*filter.FieldSchema{
			"status": {
				ExternalName: "status", SQLExpr: "users.status",
				DataType:         core.FieldTypeString,
				AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpIn},
				EnumValues:       []string{"active", "inactive"},
			},
		},
	}

	fp := filter.NewFilterParser(schema, nil)
	ast, err := fp.Parse(`{"status":"active"}`)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	builder := filter.NewSQLBuilder(schema, filter.PostgresDialect{})
	result, err := builder.Build(ast)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("WHERE:", result.Where)
	fmt.Println("args:", result.Args)
	// Output:
	// WHERE: users.status = $1
	// args: [active]
}

func ExampleNewListQuery() {
	schema := &filter.ResourceSchema{
		Name: "products",
		Fields: map[string]*filter.FieldSchema{
			"category": {
				ExternalName: "category", SQLExpr: "products.category",
				DataType:         core.FieldTypeString,
				AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpIn},
				Facetable:        true,
			},
			"price": {
				ExternalName: "price", SQLExpr: "products.price",
				DataType:         core.FieldTypeFloat,
				AllowedOperators: []filter.FilterOperator{filter.OpGte, filter.OpLte},
			},
		},
	}
	sortSchema := &filter.SortSchema{
		Fields: map[string]*filter.SortFieldSchema{
			"price": {SQLExpr: "products.price"},
		},
		DefaultField: "price",
		DefaultDir:   filter.SortDesc,
		MaxFields:    3,
	}

	lq := filter.NewListQuery(schema, sortSchema, filter.PostgresDialect{})
	result, err := lq.Build(
		filter.ListFilter(`{"category":"electronics"}`),
		filter.ListSort("-price"),
		filter.ListLimit(10),
		filter.ListWithCount(true),
		filter.ListFacets("category"),
	)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("has WHERE:", result.Where != "1=1")
	fmt.Println("has ORDER BY:", result.OrderBy != "")
	fmt.Println("limit:", result.Limit)
	fmt.Println("has count SQL:", result.CountSQL != "")
	fmt.Println("has facets:", result.Facets != nil)
	// Output:
	// has WHERE: true
	// has ORDER BY: true
	// limit: 10
	// has count SQL: true
	// has facets: true
}

func ExampleNewQueryParamParser() {
	schema := &filter.ResourceSchema{
		Name: "items",
		Fields: map[string]*filter.FieldSchema{
			"status": {
				ExternalName: "status", SQLExpr: "items.status",
				DataType:         core.FieldTypeString,
				AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpIn},
			},
			"price": {
				ExternalName: "price", SQLExpr: "items.price",
				DataType:         core.FieldTypeFloat,
				AllowedOperators: []filter.FilterOperator{filter.OpGte, filter.OpLte},
			},
		},
	}

	qp := filter.NewQueryParamParser(schema)
	ast, err := qp.Parse(url.Values{
		"status":    {"active"},
		"price_gte": {"50"},
	})
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	builder := filter.NewSQLBuilder(schema, filter.PostgresDialect{})
	result, err := builder.Build(ast)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("arg count:", len(result.Args))
	fmt.Println("has AND:", strings.Contains(result.Where, "AND"))
	// Output:
	// arg count: 2
	// has AND: true
}

func ExampleParseSort() {
	schema := &filter.SortSchema{
		Fields: map[string]*filter.SortFieldSchema{
			"name":       {SQLExpr: "items.name"},
			"created_at": {SQLExpr: "items.created_at", Indexed: true},
		},
		DefaultField: "created_at",
		DefaultDir:   filter.SortDesc,
		MaxFields:    3,
	}

	result, err := filter.ParseSort(schema, "name,-created_at")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("SQL:", result.ToSQL(filter.PostgresDialect{}))
	// Output:
	// SQL: ORDER BY items.name ASC, items.created_at DESC
}

func ExampleNewPage() {
	items := []string{"a", "b", "c"} // limit+1 pattern: 3 items, limit=2

	// The plan comes from the Build call that produced the rows
	// (ListResult.Cursor); the getter resolves each key's value from the
	// boundary row by row column.
	plan := filter.CursorPlan{Keys: []filter.CursorKey{{Name: "id", SQLExpr: "id", Column: "id", Direction: filter.SortAsc}}}
	page, err := filter.NewPage(items, 2, plan, func(s string, _ string) (any, error) {
		return s, nil
	})
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("items:", len(page.Items))
	fmt.Println("has more:", page.PageInfo.HasNextPage)
	fmt.Println("has cursor:", !page.PageInfo.EndCursor.IsZero())
	// Output:
	// items: 2
	// has more: true
	// has cursor: true
}

func ExampleGenerateFilterSchema() {
	type Product struct {
		ID       string  `json:"id"       parse:"filter"       filter_ops:"eq,in" indexed:"true"`
		Name     string  `json:"name"     parse:"filter,search" filter_ops:"eq,ilike" label:"Name" group:"basics" order:"1"`
		Category string  `json:"category" parse:"filter,facet"  filter_ops:"eq,in" facet:"terms" enum:"electronics,books"`
		Price    float64 `json:"price"    parse:"filter"        filter_ops:"gte,lte" db:"unit_price"`
	}

	gen := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[Product](gen)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("table:", schema.Name)
	fmt.Println("name label:", schema.Fields["name"].Label)
	fmt.Println("category facetable:", schema.Fields["category"].Facetable)
	fmt.Println("price SQL:", schema.Fields["price"].SQLExpr)
	// Output:
	// table: products
	// name label: Name
	// category facetable: true
	// price SQL: products.unit_price
}

func ExampleApplyPolicyConstraints() {
	schema := &filter.ResourceSchema{
		Name: "devices",
		Fields: map[string]*filter.FieldSchema{
			"status": {
				ExternalName: "status", SQLExpr: "devices.status",
				DataType:         core.FieldTypeString,
				AllowedOperators: []filter.FilterOperator{filter.OpEq},
			},
		},
		SystemFields: map[string]*filter.FieldSchema{
			"tenant_id": {
				ExternalName: "tenant_id", SQLExpr: "devices.tenant_id",
				DataType:         core.FieldTypeString,
				AllowedOperators: []filter.FilterOperator{filter.OpEq},
			},
		},
	}

	userAST := &filter.FilterAST{Root: &filter.FilterNode{
		Field: schema.Fields["status"], Op: filter.OpEq, Value: "active",
	}}

	pc := &filter.PolicyConstraints{
		Constraints: []*filter.PolicyConstraint{
			{Field: "tenant_id", Operator: filter.OpEq, Value: "t1"},
		},
	}

	merged, err := filter.ApplyPolicyConstraints(schema, userAST, pc)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	builder := filter.NewSQLBuilder(schema, filter.PostgresDialect{})
	result, _ := builder.Build(merged)

	fmt.Println("WHERE:", result.Where)
	fmt.Println("args:", result.Args)
	// Output:
	// WHERE: (devices.status = $1 AND devices.tenant_id = $2)
	// args: [active t1]
}
