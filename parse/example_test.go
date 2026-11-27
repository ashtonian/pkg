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

// Example: one struct drives filter, sort, keyset pagination and patch
// compilation.
func Example() {
	type Device struct {
		ID     string `json:"id"     db:"id"     parse:"filter,sort"`
		Name   string `json:"name"   db:"name"   parse:"filter,sort,patch"`
		Status string `json:"status" db:"status" parse:"filter,patch" enum:"active,offline"`
	}
	tp, err := parse.NewResource[Device](onPostgres, "devices")
	if err != nil {
		panic(err)
	}

	// A list request: filter, sort and page size compile to SQL fragments.
	lq, err := tp.ListQuery()
	if err != nil {
		panic(err)
	}
	list, err := lq.Build(
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
	// ("devices"."name" LIKE $1 ESCAPE '\' AND "devices"."status" = $2)
	// ORDER BY "devices"."name" COLLATE pg_c_utf8 ASC, "devices"."id" COLLATE pg_c_utf8 ASC
	// [sensor-% active] 26
	// SET "status" = $1 [offline]
}

func ExampleNewResource_filter() {
	type Device struct {
		ID     string `json:"id"     parse:"filter"       filter_ops:"eq,in" indexed:"true"`
		Name   string `json:"name"   parse:"filter,patch" filter_ops:"eq,like" patch_ops:"replace"`
		Status string `json:"status" parse:"filter,patch" filter_ops:"eq,ne" patch_ops:"replace" enum:"active,offline"`
	}

	tp, err := parse.NewResource[Device](onPostgres, "devices")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	result, err := tp.ParseFilter(`{"status":"active"}`)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("filter conditions:", result.ConditionCount())
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

	tp, err := parse.NewResource[Item](onPostgres, "items")
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
		Name      string  `json:"name"       parse:"filter,patch,search" filter_ops:"eq,ilike" patch_ops:"replace" label:"Product Name" group:"basics" order:"1"`
		Category  string  `json:"category"   parse:"filter,facet"        filter_ops:"eq,in" facet:"terms:10" label:"Category" group:"browse" order:"1" enum:"electronics,clothing,books,home"`
		Price     float64 `json:"price"      parse:"filter,facet"        filter_ops:"gte,lte" facet:"range:0-50,50-100,100+" label:"Price" group:"browse" order:"2"`
		Condition string  `json:"condition"  parse:"filter,facet"        filter_ops:"eq,in" facet:"terms" label:"Condition" group:"details" order:"1" enum:"new,like_new,used"`
		CreatedAt string  `json:"created_at" parse:"filter,sort"         filter_ops:"gt,lt,gte,lte" sort_default:"desc" indexed:"true"`
	}

	tp, err := parse.NewResource[Product](onPostgres, "products")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	// Schema discovery.
	reg := parse.NewSchemaRegistry()
	parse.MustRegisterResource(reg, tp)
	schema := reg.SchemaFor("products")
	fmt.Println("=== Schema ===")
	fmt.Println("resource:", schema.Resource)
	fmt.Println("search enabled:", schema.Search.Enabled)
	fmt.Println("category label:", schema.Filter.Fields["category"].Label)

	// Query param parsing.
	qp, _ := filter.NewQueryParamParser(tp.FilterSchema())
	ast, _ := qp.Parse(url.Values{"category": {"electronics"}, "price_gte": {"50"}})
	fmt.Println("\n=== Query Params ===")
	fmt.Println("parsed:", ast.Root != nil)

	// List query with count + facets.
	lq, _ := tp.ListQuery()
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
	plan := filter.CursorPlan{Keys: []filter.CursorKey{{Name: "id", Column: "id", Direction: filter.SortDesc}}}
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

	tp, err := parse.NewResource[Device](onPostgres, "device", parse.PrimaryKey("device_id"), parse.ParserOptions(core.WithGeneratorOptions(
		core.WithFieldNameTag("bson"),
		core.WithColumnTag("sql"),
	)))
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("table:", tp.FilterSchema().Name)
	fmt.Println("id SQL:", filter.FieldExpr(filter.PostgresDialect{}, tp.FilterSchema().Fields["id"]))
	fmt.Println("name SQL:", filter.FieldExpr(filter.PostgresDialect{}, tp.FilterSchema().Fields["name"]))

	name, _ := tp.PatchSchema().Path("/name")
	fmt.Println("patch name col:", name.SQLColumn)
	// Output:
	// table: device
	// id SQL: "device"."device_id"
	// name SQL: "device"."device_name"
	// patch name col: device_name
}

// This example is intentionally checking composability — the user builds
// a custom WHERE by combining ListResult parts with their own SQL.
func ExampleResource_ListQuery_customComposition() {
	type Item struct {
		ID     string `json:"id,omitempty" db:"id"`
		Status string `json:"status" parse:"filter" filter_ops:"eq"`
	}

	tp := parse.MustResource[Item](onPostgres, "items")
	lq, _ := tp.ListQuery()
	result, _ := lq.Build(filter.ListFilter(`{"status":"active"}`))

	// Compose with custom SQL using the returned parts.
	customSQL := fmt.Sprintf(
		"SELECT * FROM items WHERE %s AND custom_flag = true %s LIMIT %d",
		result.Where, result.OrderBy, result.FetchLimit,
	)
	fmt.Println("has custom:", strings.Contains(customSQL, "custom_flag"))
	fmt.Println("has filter:", strings.Contains(customSQL, `"items"."status"`))
	// Output:
	// has custom: true
	// has filter: true
}

type Device struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter,sort,patch"`
	Status   string `json:"status"    db:"status"    parse:"filter,patch" enum:"active,offline"`
}

// A list request — flat and JSON filters, sort, page size — compiled for
// PostgreSQL under the server's tenant: the key is the final keyset column,
// and the page reads one row more than it returns to know there is a next.
func ExampleResource_BuildList() {
	devices := parse.MustResource[Device](onPostgres, "devices", parse.TenantColumn("tenant_id"), parse.PageSize(25, 100))

	q, _ := url.ParseQuery(`status=active&filter={"name":{"$ne":"spare"}}&sort=name&page_size=10`)
	req, err := devices.ParseList(q)
	if err != nil {
		fmt.Println(err)
		return
	}
	plan, err := devices.BuildList(req, parse.Scope{Tenant: "t1"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(plan.Page.SQL)
	fmt.Println(plan.Page.Args)
	// Output:
	// SELECT "devices"."id", "devices"."tenant_id", "devices"."name", "devices"."status" FROM "devices" WHERE (("devices"."name" <> $1 AND "devices"."status" = $2) AND "devices"."tenant_id" = $3) ORDER BY "devices"."name" COLLATE pg_c_utf8 ASC, "devices"."id" COLLATE pg_c_utf8 ASC LIMIT 11
	// [spare active t1]
}

// A merge patch applied to one device, compiled for PostgreSQL under the
// server's tenant: the SET placeholders come first, then the tenant floor and
// the target, and the statement returns the row as the update left it.
func ExampleResource_BuildUpdate() {
	devices := parse.MustResource[Device](onPostgres, "devices", parse.TenantColumn("tenant_id"))

	parsed, err := devices.ParseMergePatch([]byte(`{"status":"offline"}`))
	if err != nil {
		fmt.Println(err)
		return
	}
	plan, err := devices.BuildUpdate(parsed, parse.ByID("d1"), parse.Scope{Tenant: "t1"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(plan.Owner.SQL)
	fmt.Println(plan.Owner.Args)
	// Output:
	// UPDATE "devices" SET "status" = $1 WHERE ("devices"."tenant_id" = $2 AND "devices"."id" = $3) RETURNING "devices"."id", "devices"."tenant_id", "devices"."name", "devices"."status"
	// [offline t1 d1]
}

// SkipUnchanged writes only a device the patch changes: the WHERE also
// compares the status with the value it is set to, reusing its placeholder.
// Current, run first in the update's transaction, reads and locks the target
// as it stands, so a device it returns and the update does not is found and
// unchanged; neither returning it means it is not found.
func ExampleSkipUnchanged() {
	devices := parse.MustResource[Device](onPostgres, "devices", parse.TenantColumn("tenant_id"))

	parsed, err := devices.ParseMergePatch([]byte(`{"status":"offline"}`))
	if err != nil {
		fmt.Println(err)
		return
	}
	plan, err := devices.BuildUpdate(parsed, parse.ByID("d1"), parse.Scope{Tenant: "t1"}, parse.SkipUnchanged())
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(plan.Owner.SQL)
	fmt.Println(plan.Owner.Args)
	fmt.Println(plan.Current.SQL)
	fmt.Println(plan.Current.Args)
	// Output:
	// UPDATE "devices" SET "status" = $1 WHERE ("devices"."tenant_id" = $2 AND "devices"."id" = $3) AND ("status" IS DISTINCT FROM $1 OR "status"::text COLLATE "C" IS DISTINCT FROM ($1)::text) RETURNING "devices"."id", "devices"."tenant_id", "devices"."name", "devices"."status"
	// [offline t1 d1]
	// SELECT "devices"."id", "devices"."tenant_id", "devices"."name", "devices"."status" FROM "devices" WHERE ("devices"."tenant_id" = $1 AND "devices"."id" = $2) FOR UPDATE
	// [t1 d1]
}

type Command struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	DeviceID string `json:"device_id" db:"device_id" parse:"filter"`
	Status   string `json:"status"    db:"status"    parse:"filter"`
	Attempts int64  `json:"attempts"  db:"attempts"  parse:"filter"`
}

// Devices have commands and tags: a request filters devices by their
// commands with the commands' own filter, every predicate inside one $some
// on one command, held to the tenant; a many-to-many's target filter joins
// the target to the pair table inside the subquery.
func ExampleResource_Relate() {
	devices := parse.MustResource[Device](onPostgres, "devices", parse.TenantColumn("tenant_id"))
	commands := parse.MustResource[Command](onPostgres, "commands", parse.TenantColumn("tenant_id"))
	tags := parse.MustResource[Tag](onPostgres, "tags", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
	devices.MustRelate("tags", parse.ManyToMany(tags, parse.Junction{Table: "device_tags", Source: []string{"device_id"}, Target: []string{"tag_id"}, Tenant: "tenant_id"}, parse.Indexed()))
	commands.MustRelate("device", parse.BelongsTo(devices, "device_id"))

	for _, f := range []string{
		`{"commands":{"$some":{"status":"pending","attempts":{"$gt":2}}}}`,
		`{"commands":{"$none":{"status":"failed"}}}`,
		`{"tags":{"$some":{"label":"red"}}}`,
	} {
		req, err := devices.ParseList(url.Values{"filter": {f}})
		if err != nil {
			fmt.Println(err)
			return
		}
		plan, err := devices.BuildList(req, parse.Scope{Tenant: "t1"})
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(f)
		fmt.Println(plan.Fragment, plan.Page.Args)
	}
	// Output:
	// {"commands":{"$some":{"status":"pending","attempts":{"$gt":2}}}}
	// WHERE (EXISTS (SELECT 1 FROM "commands" WHERE "commands"."device_id" = "devices"."id" AND "commands"."tenant_id" = $1 AND ("commands"."attempts" > $2 AND "commands"."status" = $3)) AND "devices"."tenant_id" = $4) ORDER BY "devices"."id" COLLATE pg_c_utf8 ASC LIMIT 51 [t1 2 pending t1]
	// {"commands":{"$none":{"status":"failed"}}}
	// WHERE (NOT EXISTS (SELECT 1 FROM "commands" WHERE "commands"."device_id" = "devices"."id" AND "commands"."tenant_id" = $1 AND "commands"."status" = $2) AND "devices"."tenant_id" = $3) ORDER BY "devices"."id" COLLATE pg_c_utf8 ASC LIMIT 51 [t1 failed t1]
	// {"tags":{"$some":{"label":"red"}}}
	// WHERE (EXISTS (SELECT 1 FROM "device_tags" "rel1__" JOIN "tags" ON "tags"."id" = "rel1__"."tag_id" WHERE "rel1__"."device_id" = "devices"."id" AND "rel1__"."tenant_id" = $1 AND "tags"."tenant_id" = $2 AND "tags"."deleted_at" IS NULL AND "tags"."label" = $3) AND "devices"."tenant_id" = $4) ORDER BY "devices"."id" COLLATE pg_c_utf8 ASC LIMIT 51 [t1 t1 red t1]
}

type StockItem struct {
	WarehouseID string `json:"warehouse_id" db:"warehouse_id" parse:"filter,sort"`
	SKU         string `json:"sku"          db:"sku"          parse:"filter,sort"`
	Qty         int64  `json:"qty"          db:"qty"          parse:"filter,sort,patch"`
}

// A resource keyed by two columns: a list's keyset ends on both, so a page
// resumes after exactly one row; an update reaches one row by its whole
// key, and several rows by a set of keys bound as one parameter.
func ExamplePrimaryKey() {
	stock := parse.MustResource[StockItem](onPostgres, "stock", parse.PrimaryKey("warehouse_id", "sku"))

	q, _ := url.ParseQuery(`filter={"qty":{"$gt":0}}&sort=-qty&page_size=2`)
	req, err := stock.ParseList(q)
	if err != nil {
		fmt.Println(err)
		return
	}
	list, err := stock.BuildList(req, parse.Scope{})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(list.Page.SQL)
	fmt.Println(list.Page.Args)

	parsed, err := stock.ParseMergePatch([]byte(`{"qty":7}`))
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, target := range []parse.Target{
		parse.ByKey(parse.Key{"w1", "a"}),
		parse.ByKeys(parse.Key{"w1", "a"}, parse.Key{"w2", "a"}),
	} {
		update, err := stock.BuildUpdate(parsed, target, parse.Scope{})
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(update.Owner.SQL)
		fmt.Println(update.Owner.Args)
	}
	// Output:
	// SELECT "stock"."warehouse_id", "stock"."sku", "stock"."qty" FROM "stock" WHERE "stock"."qty" > $1 ORDER BY "stock"."qty" DESC, "stock"."warehouse_id" COLLATE pg_c_utf8 DESC, "stock"."sku" COLLATE pg_c_utf8 DESC LIMIT 3
	// [0]
	// UPDATE "stock" SET "qty" = $1 WHERE ("stock"."warehouse_id" = $2 AND "stock"."sku" = $3) RETURNING "stock"."warehouse_id", "stock"."sku", "stock"."qty"
	// [7 w1 a]
	// UPDATE "stock" SET "qty" = $1 WHERE ("stock"."warehouse_id", "stock"."sku") IN (SELECT "k"."warehouse_id", "k"."sku" FROM jsonb_populate_recordset(NULL::"stock", $2) AS "k") RETURNING "stock"."warehouse_id", "stock"."sku", "stock"."qty"
	// [7 [{"sku":"a","warehouse_id":"w1"},{"sku":"a","warehouse_id":"w2"}]]
}
