//go:build unit

package parse_test

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// Example is the README Quick Start: one struct drives filter, sort,
// keyset pagination and patch compilation.
func Example() {
	type Device struct {
		ID     string `json:"id"     db:"id"     parse:"filter,sort"`
		Name   string `json:"name"   db:"name"   parse:"filter,sort,patch"`
		Status string `json:"status" db:"status" parse:"filter,patch" enum:"active,offline"`
	}
	tp, err := parse.NewResource[Device]("devices")
	if err != nil {
		panic(err)
	}

	// A list request: filter, sort and page size compile to SQL fragments.
	list, err := tp.ListQuery(filter.PostgresDialect{}).Build(
		filter.ListFilter(`{"status":"active","name":{"$like":"sensor-%"}}`),
		filter.ListSort("name"),
		filter.ListLimit(25),
	)
	if err != nil {
		panic(err)
	}
	fmt.Println(list.Where)
	fmt.Println(list.OrderBy)
	fmt.Println(list.Args, list.FetchLimit)

	// An update: a JSON Merge Patch compiles to one SET clause.
	update, err := tp.ParseMergePatch([]byte(`{"status":"offline"}`))
	if err != nil {
		panic(err)
	}
	set, err := patch.BuildPatchSQL(update, filter.PostgresDialect{})
	if err != nil {
		panic(err)
	}
	fmt.Println("SET", set.SetClause, set.Args)
	// Output:
	// (devices.name LIKE $1 ESCAPE '\' AND devices.status = $2)
	// ORDER BY devices.name COLLATE pg_c_utf8 ASC, devices.id COLLATE pg_c_utf8 ASC
	// [sensor-% active] 26
	// SET status = $1 [offline]
}

func ExampleNewResource_filter() {
	type Device struct {
		ID     string `json:"id"     parse:"filter"       filter_ops:"eq,in" indexed:"true"`
		Name   string `json:"name"   parse:"filter,patch" filter_ops:"eq,like" patch_ops:"replace"`
		Status string `json:"status" parse:"filter,patch" filter_ops:"eq,ne" patch_ops:"replace" enum:"active,offline"`
	}

	tp, err := parse.NewResource[Device]("devices")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	result, err := tp.ParseFilter(`{"status":"active"}`)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("filter conditions:", result.ConditionCount)
	fmt.Println("filter schema:", tp.FilterSchema().Name)
	// Output:
	// filter conditions: 1
	// filter schema: devices
}

func ExampleResource_ApplyPatch() {
	type Item struct {
		ID     string `json:"id"     parse:"filter"`
		Name   string `json:"name"   parse:"filter,patch" filter_ops:"eq" patch_ops:"replace"`
		Status string `json:"status" parse:"patch" patch_ops:"replace" enum:"active,inactive"`
	}

	tp, err := parse.NewResource[Item]("items")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	original := Item{ID: "i1", Name: "Original", Status: "active"}
	body := []byte(`[{"op":"replace","path":"/name","value":"Updated"}]`)

	updated, err := tp.ApplyPatch(body, original)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("id unchanged:", updated.ID == "i1")
	fmt.Println("name:", updated.Name)
	fmt.Println("status unchanged:", updated.Status == "active")
	// Output:
	// id unchanged: true
	// name: Updated
	// status unchanged: true
}

func ExampleSchemaRegistry() {
	type Product struct {
		ID       string `json:"id"       parse:"filter"       filter_ops:"eq" indexed:"true"`
		Name     string `json:"name"     parse:"filter,patch,search" filter_ops:"eq,ilike" patch_ops:"replace" label:"Name" group:"basics"`
		Category string `json:"category" parse:"filter,facet"  filter_ops:"eq,in" facet:"terms" enum:"electronics,books"`
	}

	reg := parse.NewSchemaRegistry()
	parse.MustRegister[Product](reg)

	schema := reg.SchemaFor("products")
	fmt.Println("resource:", schema.Resource)
	fmt.Println("has filter:", schema.Filter != nil)
	fmt.Println("has patch:", schema.Patch != nil)
	fmt.Println("has search:", schema.Search != nil)
	fmt.Println("name label:", schema.Filter.Fields["name"].Label)
	fmt.Println("category facetable:", schema.Filter.Fields["category"].Facetable)
	// Output:
	// resource: products
	// has filter: true
	// has patch: true
	// has search: true
	// name label: Name
	// category facetable: true
}

func ExampleNewResource_facetedListPage() {
	type Product struct {
		ID        string  `json:"id"         parse:"filter"              filter_ops:"eq,in" indexed:"true"`
		Name      string  `json:"name"       parse:"filter,patch,search" filter_ops:"eq,ilike" patch_ops:"replace" search_weight:"A" label:"Product Name" group:"basics" order:"1"`
		Category  string  `json:"category"   parse:"filter,facet"        filter_ops:"eq,in" facet:"terms:10" label:"Category" group:"browse" order:"1" enum:"electronics,clothing,books,home"`
		Price     float64 `json:"price"      parse:"filter,facet"        filter_ops:"gte,lte" facet:"range:0-50,50-100,100+" label:"Price" group:"browse" order:"2"`
		Condition string  `json:"condition"  parse:"filter,facet"        filter_ops:"eq,in" facet:"terms" label:"Condition" group:"details" order:"1" enum:"new,like_new,used"`
		CreatedAt string  `json:"created_at" parse:"filter,sort"         filter_ops:"gt,lt,gte,lte" sort_default:"desc" indexed:"true"`
	}

	tp, err := parse.NewResource[Product]("products")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	// Schema discovery.
	reg := parse.NewSchemaRegistry()
	parse.MustRegister[Product](reg)
	schema := reg.SchemaFor("products")
	fmt.Println("=== Schema ===")
	fmt.Println("resource:", schema.Resource)
	fmt.Println("search enabled:", schema.Search.Enabled)
	fmt.Println("category label:", schema.Filter.Fields["category"].Label)

	// Query param parsing.
	qp := filter.NewQueryParamParser(tp.FilterSchema())
	ast, _ := qp.Parse(url.Values{"category": {"electronics"}, "price_gte": {"50"}})
	fmt.Println("\n=== Query Params ===")
	fmt.Println("parsed:", ast.Root != nil)

	// List query with count + facets.
	lq := tp.ListQuery(filter.PostgresDialect{})
	result, _ := lq.Build(
		filter.ListFilterAST(ast),
		filter.ListSort("-created_at"),
		filter.ListLimit(25),
		filter.ListWithCount(true),
		filter.ListFacets("category", "condition", "price"),
	)

	fmt.Println("\n=== List Query ===")
	fmt.Println("has WHERE:", result.Where != "1=1")
	fmt.Println("limit:", result.Limit)
	fmt.Println("has count:", result.CountSQL != "")
	fmt.Println("facets:", result.Facets != nil)

	// Cursor pagination — the Build result's keyset plan turns the fetched
	// limit+1 rows into a page with cursors.
	type Row struct{ ID string }
	items := []Row{{"p1"}, {"p2"}, {"p3"}}
	plan := filter.CursorPlan{Keys: []filter.CursorKey{{Name: "id", SQLExpr: "id", Column: "id", Direction: filter.SortDesc}}}
	page, _ := filter.NewPage(items, 2, plan, func(r Row, _ string) (any, error) { return r.ID, nil })
	fmt.Println("\n=== Pagination ===")
	fmt.Println("items:", len(page.Items))
	fmt.Println("has more:", page.PageInfo.HasNextPage)

	// Output:
	// === Schema ===
	// resource: products
	// search enabled: true
	// category label: Category
	//
	// === Query Params ===
	// parsed: true
	//
	// === List Query ===
	// has WHERE: true
	// limit: 25
	// has count: true
	// facets: true
	//
	// === Pagination ===
	// items: 2
	// has more: true
}

func ExampleNewResource_conventions() {
	type Device struct {
		ID   string `bson:"id"   parse:"filter" filter_ops:"eq" sql:"device_id"`
		Name string `bson:"name" parse:"filter,patch" filter_ops:"eq" patch_ops:"replace" sql:"device_name"`
	}

	tp, err := parse.NewResource[Device]("device", parse.PrimaryKey("device_id"), parse.ParserOptions(core.WithGeneratorOptions(
		core.WithFieldNameTag("bson"),
		core.WithColumnTag("sql"),
	)))
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("table:", tp.FilterSchema().Name)
	fmt.Println("id SQL:", tp.FilterSchema().Fields["id"].SQLExpr)
	fmt.Println("name SQL:", tp.FilterSchema().Fields["name"].SQLExpr)

	nameCol := tp.PatchSchema().AllowedPaths["/name"].SQLColumn
	fmt.Println("patch name col:", nameCol)
	// Output:
	// table: device
	// id SQL: device.device_id
	// name SQL: device.device_name
	// patch name col: device_name
}

// This example is intentionally checking composability — the user builds
// a custom WHERE by combining ListResult parts with their own SQL.
func ExampleResource_ListQuery_customComposition() {
	type Item struct {
		ID     string `json:"id,omitempty" db:"id"`
		Status string `json:"status" parse:"filter" filter_ops:"eq"`
	}

	tp := parse.MustResource[Item]("items")
	lq := tp.ListQuery(filter.PostgresDialect{})
	result, _ := lq.Build(filter.ListFilter(`{"status":"active"}`))

	// Compose with custom SQL using the returned parts.
	customSQL := fmt.Sprintf(
		"SELECT * FROM items WHERE %s AND custom_flag = true %s LIMIT %d",
		result.Where, result.OrderBy, result.FetchLimit,
	)
	fmt.Println("has custom:", strings.Contains(customSQL, "custom_flag"))
	fmt.Println("has filter:", strings.Contains(customSQL, "items.status"))
	// Output:
	// has custom: true
	// has filter: true
}
