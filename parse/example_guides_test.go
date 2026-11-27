//go:build unit

package parse_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsesql"
	"github.com/ashtonian/pkg/parse/parsesqlite"
	"github.com/ashtonian/pkg/parse/patch"
)

// The guides' remaining SQL: each example's output is quoted by a block in
// docs/*.md, which TestGuideBlocksMatchTheirExamples compares with it.

type LineItem struct {
	SKU string `json:"sku" parse:"filter"`
	Qty int64  `json:"qty" parse:"filter"`
}

type Order struct {
	ID     string          `json:"id"     db:"id"     parse:"filter,sort"`
	Items  []LineItem      `json:"items"  db:"items"  parse:"filter"`
	Scores []int64         `json:"scores" db:"scores" parse:"filter"`
	Doc    json.RawMessage `json:"doc"    db:"doc"    parse:"filter" paths:"tags:[]string"`
}

// guideProvider is a provider a guide example compiles for, with the name
// its lines start with.
type guideProvider struct {
	name     string
	provider *parse.Provider
}

// guideProviders are the providers a guide example compiles for, in the
// order it prints them.
func guideProviders() []guideProvider {
	providers := []guideProvider{{"postgres", onPostgres}, {"sqlite  ", onSQLite}}
	return providers
}

// showList compiles one list request on each guide provider, the resource
// built on it by resource, and prints its WHERE, ORDER BY and bound
// arguments.
func showList[T any](resource func(*parse.Provider) *parse.Resource[T], opts ...filter.ListOption) {
	for _, g := range guideProviders() {
		lq, err := resource(g.provider).ListQuery()
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		res, err := lq.Build(opts...)
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		fmt.Printf("%s WHERE %s %s %v\n", g.name, res.Where, res.OrderBy, res.Args)
	}
}

// showFilter prints a JSON filter, then what it compiles to on each guide
// provider.
func showFilter[T any](resource func(*parse.Provider) *parse.Resource[T], f string) {
	fmt.Println(f)
	showList(resource, filter.ListFilter(f))
}

func guideOrders(p *parse.Provider) *parse.Resource[Order] {
	return parse.MustResource[Order](p, "orders")
}

// Example_arraysOfScalars: a condition on one element of an array column
// unnests it; on an array path it reads the member's array elements.
func Example_arraysOfScalars() {
	showFilter(guideOrders, `{"scores":{"$some":{"$gte":5,"$lt":10}}}`)
	showFilter(guideOrders, `{"doc.tags":{"$some":{"$like":"a%"}}}`)
	// Output:
	// {"scores":{"$some":{"$gte":5,"$lt":10}}}
	// postgres WHERE EXISTS (SELECT 1 FROM unnest("orders"."scores") AS elem1__(v) WHERE elem1__.v IS NOT NULL AND (elem1__.v >= $1 AND elem1__.v < $2)) ORDER BY "orders"."id" COLLATE pg_c_utf8 ASC [5 10]
	// sqlite   WHERE EXISTS (SELECT 1 FROM json_each(CASE WHEN json_type("orders"."scores") = 'array' THEN "orders"."scores" END) AS elem1__ WHERE (CASE WHEN elem1__.type IN ('integer', 'real') THEN elem1__.value END) IS NOT NULL AND ((CASE WHEN elem1__.type IN ('integer', 'real') THEN elem1__.value END) >= ?1 AND (CASE WHEN elem1__.type IN ('integer', 'real') THEN elem1__.value END) < ?2)) ORDER BY "orders"."id" COLLATE BINARY ASC [5 10]
	// {"doc.tags":{"$some":{"$like":"a%"}}}
	// postgres WHERE EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof("orders"."doc" -> 'tags') = 'array' THEN "orders"."doc" -> 'tags' END) AS elem1__(v) WHERE (CASE WHEN jsonb_typeof(elem1__.v) = 'string' THEN (elem1__.v #>> '{}') END) IS NOT NULL AND (CASE WHEN jsonb_typeof(elem1__.v) = 'string' THEN (elem1__.v #>> '{}') END) LIKE $1 ESCAPE '\') ORDER BY "orders"."id" COLLATE pg_c_utf8 ASC [a%]
	// sqlite   WHERE EXISTS (SELECT 1 FROM json_each(CASE WHEN json_type("orders"."doc", '$."tags"') = 'array' THEN "orders"."doc" END, '$."tags"') AS elem1__ WHERE (CASE WHEN elem1__.type = 'text' THEN elem1__.value END) IS NOT NULL AND (CASE WHEN elem1__.type = 'text' THEN elem1__.value END) GLOB ?1) ORDER BY "orders"."id" COLLATE BINARY ASC [a*]
}

// Example_arraysOfObjects: every predicate inside one $some holds for one
// element of a JSON array of objects.
func Example_arraysOfObjects() {
	showFilter(guideOrders, `{"items":{"$some":{"sku":"a","qty":{"$gt":2}}}}`)
	// Output:
	// {"items":{"$some":{"sku":"a","qty":{"$gt":2}}}}
	// postgres WHERE EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof("orders"."items") = 'array' THEN "orders"."items" END) AS elem1__(v) WHERE ((CASE WHEN (jsonb_typeof(elem1__.v -> 'qty') = 'number' AND length(split_part(ltrim((elem1__.v ->> 'qty'), '-'), '.', 1)) + length(rtrim(split_part((elem1__.v ->> 'qty'), '.', 2), '0')) <= 1000) THEN (elem1__.v ->> 'qty')::numeric END) > $1 AND elem1__.v @> $2::jsonb)) ORDER BY "orders"."id" COLLATE pg_c_utf8 ASC [2 {"sku":"a"}]
	// sqlite   WHERE EXISTS (SELECT 1 FROM json_each(CASE WHEN json_type("orders"."items") = 'array' THEN "orders"."items" END) AS elem1__ WHERE ((CASE WHEN (json_type((CASE WHEN elem1__.type = 'object' THEN elem1__.value END), '$."qty"') IN ('integer', 'real') AND ((length(((CASE WHEN elem1__.type = 'object' THEN elem1__.value END) -> '$."qty"')) <= 1000 AND ((CASE WHEN elem1__.type = 'object' THEN elem1__.value END) -> '$."qty"') NOT GLOB '*[eE]*') OR parse_decimal(((CASE WHEN elem1__.type = 'object' THEN elem1__.value END) -> '$."qty"')) IS NOT NULL)) THEN json_extract((CASE WHEN elem1__.type = 'object' THEN elem1__.value END), '$."qty"') END) > ?1 AND (CASE WHEN json_type((CASE WHEN elem1__.type = 'object' THEN elem1__.value END), '$."sku"') = 'text' THEN json_extract((CASE WHEN elem1__.type = 'object' THEN elem1__.value END), '$."sku"') END) = ?2)) ORDER BY "orders"."id" COLLATE BINARY ASC [2 a]
}

type Document struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Title    string `json:"title"     db:"title"     parse:"filter"`
}

type Note struct {
	ID         string     `json:"id"          db:"id"          parse:"filter,sort"`
	TenantID   string     `json:"tenant_id"   db:"tenant_id"`
	DocumentID string     `json:"document_id" db:"document_id" parse:"filter"`
	Title      string     `json:"title"       db:"title"       parse:"filter,patch"`
	Body       string     `json:"body"        db:"body"        parse:"filter,patch"`
	Rank       int64      `json:"rank"        db:"rank"        parse:"filter,sort,patch"`
	Version    int64      `json:"version"     db:"version"`
	UpdatedAt  time.Time  `json:"updated_at"  db:"updated_at"`
	DeletedAt  *time.Time `json:"-"           db:"deleted_at"`
}

func noteResource(p *parse.Provider) *parse.Resource[Note] {
	return parse.MustResource[Note](p, "notes", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
}

// A governed list reads the rows a governance tree admits, of any tenant:
// here tenant A's documents and B's b1. A related row is compared with the
// tenant of the listed row it hangs from, not bound to one tenant.
func ExampleResource_BuildList_governed() {
	documents := parse.MustResource[Document](onPostgres, "documents", parse.TenantColumn("tenant_id"))
	documents.MustRelate("notes", parse.HasMany(noteResource(onPostgres), "document_id", parse.Indexed()))
	eq := func(field string, v any) *filter.PolicyConstraints {
		policy := &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: field, Operator: filter.OpEq, Value: v}}}
		return policy
	}
	governance := &filter.PolicyConstraints{Or: []*filter.PolicyConstraints{eq("tenant_id", "A"), eq("id", "b1")}}

	req, err := documents.ParseListInput(parse.ListInput{Filter: `{"notes":{"$some":{"body":"urgent"}}}`})
	if err != nil {
		fmt.Println(err)
		return
	}
	plan, err := documents.BuildList(req, parse.Scope{Governance: governance})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(plan.Fragment)
	fmt.Println(plan.Page.Args)
	// Output:
	// WHERE (EXISTS (SELECT 1 FROM "notes" WHERE "notes"."document_id" = "documents"."id" AND "notes"."tenant_id" = "documents"."tenant_id" AND "notes"."deleted_at" IS NULL AND "notes"."body" = $1) AND ("documents"."tenant_id" = $2 OR "documents"."id" = $3)) ORDER BY "documents"."id" COLLATE pg_c_utf8 ASC LIMIT 51
	// [urgent A b1]
}

// SkipUnchanged with a stamp and a counter: the change predicate compares
// only the columns the patch assigns, so a note whose title and rank are
// already these is neither stamped nor counted.
func ExampleSkipUnchanged_stamped() {
	notes := noteResource(onPostgres)
	parsed, err := notes.ParseMergePatch([]byte(`{"title":"Agenda","rank":2}`))
	if err != nil {
		fmt.Println(err)
		return
	}
	plan, err := notes.BuildUpdate(parsed, parse.ByID("n1"), parse.Scope{Tenant: "t1"},
		parse.StampSQL("updated_at", "NOW()"), parse.Increment("version"), parse.SkipUnchanged())
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(plan.Owner.SQL)
	fmt.Println(plan.Owner.Args)
	fmt.Println(plan.Current.SQL)
	fmt.Println(plan.Current.Args)
	// Output:
	// UPDATE "notes" SET "rank" = $1, "title" = $2, "updated_at" = NOW(), "version" = "version" + 1 WHERE (("notes"."tenant_id" = $3 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = $4) AND ("rank" IS DISTINCT FROM $1 OR "rank"::text COLLATE "C" IS DISTINCT FROM ($1)::text OR "title" IS DISTINCT FROM $2 OR "title"::text COLLATE "C" IS DISTINCT FROM ($2)::text) RETURNING "notes"."id", "notes"."tenant_id", "notes"."document_id", "notes"."title", "notes"."body", "notes"."rank", "notes"."version", "notes"."updated_at", "notes"."deleted_at"
	// [2 Agenda t1 n1]
	// SELECT "notes"."id", "notes"."tenant_id", "notes"."document_id", "notes"."title", "notes"."body", "notes"."rank", "notes"."version", "notes"."updated_at", "notes"."deleted_at" FROM "notes" WHERE (("notes"."tenant_id" = $1 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = $2) FOR UPDATE
	// [t1 n1]
}

type Tag struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Label     string     `json:"label"      db:"label"      parse:"filter"`
	DeletedAt *time.Time `json:"-"          db:"deleted_at"`
}

// A membership change pairs only owners and targets the request may relate:
// its INSERT selects both rows, live and in the tenant, rather than binding
// raw keys, so of red (live, in t1), blue (deleted) and green (in t2) the
// update pairs d1 with red alone. It runs here on an in-memory SQLite
// database.
func ExampleResource_Update_membership() {
	ctx := context.Background()
	err := parsesqlite.Register()
	if err != nil {
		fmt.Println(err)
		return
	}
	db, err := sql.Open("sqlite", ":memory:?_txlock=immediate")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = db.Close() }()
	// Every connection to :memory: is a database of its own.
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`CREATE TABLE devices (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL)`,
		`CREATE TABLE tags (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, label TEXT NOT NULL, deleted_at TEXT)`,
		`CREATE TABLE device_tags (device_id TEXT NOT NULL, tag_id TEXT NOT NULL, tenant_id TEXT NOT NULL, PRIMARY KEY (device_id, tag_id))`,
		`INSERT INTO devices VALUES ('d1', 't1', 'sensor', 'active')`,
		`INSERT INTO tags VALUES ('red', 't1', 'Red', NULL), ('blue', 't1', 'Blue', '2026-01-01T00:00:00.000000000Z'), ('green', 't2', 'Green', NULL)`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		if err != nil {
			fmt.Println(err)
			return
		}
	}
	provider, err := parsesql.New(ctx, db)
	if err != nil {
		fmt.Println(err)
		return
	}
	devices := parse.MustResource[Device](provider, "devices", parse.TenantColumn("tenant_id"))
	tags := parse.MustResource[Tag](provider, "tags", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	devices.MustRelate("tags", parse.ManyToMany(tags, parse.Junction{Table: "device_tags", Source: []string{"device_id"}, Target: []string{"tag_id"}, Tenant: "tenant_id"}, parse.Writable()))

	parsed, err := devices.ParsePatch([]byte(`[{"op":"add","path":"/tags","value":["red","blue","green"]}]`))
	if err != nil {
		fmt.Println(err)
		return
	}
	res, err := devices.Update(ctx, parsed, parse.ByID("d1"), parse.Scope{Tenant: "t1"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("pairs written:", res.RelationRows)
	var device, tag, tenant string
	err = db.QueryRowContext(ctx, `SELECT device_id, tag_id, tenant_id FROM device_tags`).Scan(&device, &tag, &tenant)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(device, tag, tenant)
	// Output:
	// pairs written: 1
	// d1 red t1
}

type Team struct {
	ID string `json:"id" db:"id" parse:"filter,sort"`
}

type Member struct {
	ID     string `json:"id"      db:"id"      parse:"filter,sort"`
	TeamID string `json:"team_id" db:"team_id"`
}

// An include returns the related pairs outright, so an includable relation
// needs its correlation column read; QueryOnlyColumns admits a filter only.
func ExampleResource_Relate_readColumns() {
	teams := parse.MustResource[Team](onPostgres, "teams")
	members := parse.MustResource[Member](onPostgres, "members", parse.ReadColumns("id"), parse.QueryOnlyColumns("team_id"))
	fmt.Println(teams.Relate("members", parse.HasMany(members, "team_id", parse.Includable())))
	// Output:
	// parse: teams: relation "members": members does not read column "team_id"
}

type Account struct {
	ID       int     `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string  `json:"-"         db:"tenant_id"`
	GroupIDs []int64 `json:"group_ids" db:"group_ids"`
}

// A computed column whose expression serves PostgreSQL only selects as
// (sql) AS column on PostgreSQL, and a resource on SQLite is refused when it
// is built, naming the table, the column and the dialect.
func ExampleReadExpr() {
	groups := parse.ReadExpr("group_ids", func(d filter.Dialect) (string, error) {
		_, postgres := d.(filter.PostgresDialect)
		if !postgres {
			return "", filter.ErrUnsupportedDialect
		}
		return "array(SELECT g.group_id FROM account_groups g WHERE g.account_id = accounts.id ORDER BY g.group_id)", nil
	})
	accounts := parse.MustResource[Account](onPostgres, "accounts", parse.TenantColumn("tenant_id"), groups)
	fmt.Println(strings.Join(accounts.Columns(), ", "))
	_, err := parse.NewResource[Account](onSQLite, "accounts", parse.TenantColumn("tenant_id"), groups)
	fmt.Println(err)
	// Output:
	// "accounts"."id", "accounts"."tenant_id", (array(SELECT g.group_id FROM account_groups g WHERE g.account_id = accounts.id ORDER BY g.group_id)) AS "group_ids"
	// parse: accounts: computed column "group_ids" for dialect sqlite: unsupported SQL dialect
}

// Product is the resource the README's request → SQL examples compile
// against. Its relation fields are virtual (db:"-"): Collections is a
// many-to-many through product_collections; Reviews is a one-to-many whose
// child table stands in the junction's place, so it filters and includes
// but must never take patch.
type Product struct {
	ID          uuid.UUID      `json:"id"          db:"id"          parse:"filter,sort"`
	Name        string         `json:"name"        db:"name"        parse:"filter,sort,patch,search"`
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

// products is the resource the examples share, on p, with a fixed clock so
// relative times print the same every run.
func products(p *parse.Provider) *parse.Resource[Product] {
	clock := func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }
	tp, err := parse.NewResource[Product](p, "products", parse.ParserOptions(core.WithClock(clock)))
	if err != nil {
		panic(err)
	}
	return tp
}

// Example_atAGlance: a filter and a sort, with the key as the unique
// tie-breaker the cursors need.
func Example_atAGlance() {
	showList(products,
		filter.ListFilter(`{"price":{"$gte":10,"$lt":100},"stock":{"$null":false}}`),
		filter.ListSort("-price"), filter.ListLimit(20))
	// Output:
	// postgres WHERE (("products"."price" >= $1 AND "products"."price" < $2) AND "products"."stock" IS NOT NULL) ORDER BY "products"."price" DESC, "products"."id" DESC [10 100]
	// sqlite   WHERE (("products"."price" >= ?1 AND "products"."price" < ?2) AND "products"."stock" IS NOT NULL) ORDER BY "products"."price" DESC, "products"."id" DESC [10 100]
}

// Example_filterOperators: comparison, membership, pattern, NULL and boolean
// operators, and a relative time resolved against the parser clock.
func Example_filterOperators() {
	showFilter(products, `{"category":"tools","name":{"$in":["saw","drill"]}}`)
	showFilter(products, `{"price":{"$gte":10,"$lt":100},"stock":{"$null":false}}`)
	showFilter(products, `{"$or":[{"category":"books"},{"$not":{"price":{"$gt":50}}}]}`)
	showFilter(products, `{"name":{"$ilike":"%pro%"},"created_at":{"$gte":"now-7d"}}`)
	// Output:
	// {"category":"tools","name":{"$in":["saw","drill"]}}
	// postgres WHERE ("products"."category" = $1 AND "products"."name" = ANY($2)) ORDER BY "products"."created_at" DESC, "products"."id" DESC [tools [saw drill]]
	// sqlite   WHERE ("products"."category" = ?1 AND "products"."name" IN (SELECT value AS v FROM json_each(?2))) ORDER BY "products"."created_at" DESC, "products"."id" DESC [tools ["saw","drill"]]
	// {"price":{"$gte":10,"$lt":100},"stock":{"$null":false}}
	// postgres WHERE (("products"."price" >= $1 AND "products"."price" < $2) AND "products"."stock" IS NOT NULL) ORDER BY "products"."created_at" DESC, "products"."id" DESC [10 100]
	// sqlite   WHERE (("products"."price" >= ?1 AND "products"."price" < ?2) AND "products"."stock" IS NOT NULL) ORDER BY "products"."created_at" DESC, "products"."id" DESC [10 100]
	// {"$or":[{"category":"books"},{"$not":{"price":{"$gt":50}}}]}
	// postgres WHERE ("products"."category" = $1 OR NOT ("products"."price" > $2)) ORDER BY "products"."created_at" DESC, "products"."id" DESC [books 50]
	// sqlite   WHERE ("products"."category" = ?1 OR NOT ("products"."price" > ?2)) ORDER BY "products"."created_at" DESC, "products"."id" DESC [books 50]
	// {"name":{"$ilike":"%pro%"},"created_at":{"$gte":"now-7d"}}
	// postgres WHERE ("products"."created_at" >= $1 AND "products"."name" ILIKE $2 ESCAPE '\') ORDER BY "products"."created_at" DESC, "products"."id" DESC [2026-10-07T12:00:00.000000000Z %pro%]
	// sqlite   WHERE ("products"."created_at" >= ?1 AND "products"."name" LIKE ?2 ESCAPE '\') ORDER BY "products"."created_at" DESC, "products"."id" DESC [2026-10-07T12:00:00.000000000Z %pro%]
}

// Example_flatQueryParameters: field parameters (status=active,
// price_gte=10) parse to the same filter a JSON filter does.
func Example_flatQueryParameters() {
	tp := products(onPostgres)
	q, err := url.ParseQuery("category=tools&price_gte=10&stock_lt=5")
	if err != nil {
		panic(err)
	}
	qp, err := filter.NewQueryParamParser(tp.FilterSchema())
	if err != nil {
		panic(err)
	}
	ast, err := qp.Parse(q)
	if err != nil {
		panic(err)
	}
	fmt.Println("?" + q.Encode())
	showList(products, filter.ListFilterAST(ast))
	// Output:
	// ?category=tools&price_gte=10&stock_lt=5
	// postgres WHERE ("products"."category" = $1 AND "products"."price" >= $2 AND "products"."stock" < $3) ORDER BY "products"."created_at" DESC, "products"."id" DESC [tools 10 5]
	// sqlite   WHERE ("products"."category" = ?1 AND "products"."price" >= ?2 AND "products"."stock" < ?3) ORDER BY "products"."created_at" DESC, "products"."id" DESC [tools 10 5]
}

// Example_keysetPages: a sort plus a unique tie-breaker gives a total
// order; the next page seeks past the last row's key instead of offsetting.
func Example_keysetPages() {
	lq, err := products(onPostgres).ListQuery()
	if err != nil {
		panic(err)
	}
	first, err := lq.Build(
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
	showList(products, filter.ListSort("-price"), filter.ListLimit(2),
		filter.ListCursor(string(page.PageInfo.EndCursor)))
	// Output:
	// first: WHERE 1=1 ORDER BY "products"."price" DESC, "products"."id" DESC LIMIT 3 []
	// items: 2 has_next_page: true
	// postgres WHERE (1=1 AND ("products"."price", "products"."id") < ($1, $2)) ORDER BY "products"."price" DESC, "products"."id" DESC [80 0190f3c2-0000-7000-8000-000000000012]
	// sqlite   WHERE (1=1 AND ("products"."price", "products"."id") < (?1, ?2)) ORDER BY "products"."price" DESC, "products"."id" DESC [80 0190f3c2-0000-7000-8000-000000000012]
}

// Example_countFacetsSearch: one request can also produce its total, its
// facet counts and a full-text predicate, all over the same filter.
func Example_countFacetsSearch() {
	lq, err := products(onPostgres).ListQuery()
	if err != nil {
		panic(err)
	}
	res, err := lq.Build(
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
	// where: ("products"."price" < $1 AND to_tsvector('english', coalesce("products"."name", '')) @@ plainto_tsquery('english', $2)) [200 cordless drill]
	// count: SELECT COUNT(*) FROM "products" WHERE (("products"."price" < $1) AND to_tsvector('english', coalesce("products"."name", '')) @@ plainto_tsquery('english', $2)) [200 cordless drill]
	// facets: WITH base AS MATERIALIZED (SELECT "products"."category" AS facet__0 FROM "products" WHERE (("products"."price" < $1) AND to_tsvector('english', coalesce("products"."name", '')) @@ plainto_tsquery('english', $2))) SELECT facet_field, facet_key, facet_count FROM (SELECT 'category' AS facet_field, k AS facet_key, c AS facet_count, 0 AS facet_ord, ROW_NUMBER() OVER (ORDER BY c DESC, k COLLATE pg_c_utf8) AS facet_rank FROM (SELECT facet__0 AS k, COUNT(*) AS c FROM base WHERE facet__0 IS NOT NULL GROUP BY facet__0 ORDER BY c DESC, facet__0 COLLATE pg_c_utf8 LIMIT 10) s0) facets__ ORDER BY facet_ord, facet_rank [200 cordless drill]
}

// Example_relations: a many-to-many filter, a one-to-many filter through
// the child table, and a belongs-to, which is the plain foreign key.
func Example_relations() {
	showFilter(products, `{"collections":"0190f3c2-0000-7000-8000-000000000001"}`)
	showFilter(products, `{"collections":{"$nin":["0190f3c2-0000-7000-8000-000000000001"]}}`)
	showFilter(products, `{"reviews":{"$null":false}}`)
	showFilter(products, `{"supplier_id":"0190f3c2-0000-7000-8000-0000000000aa"}`)
	// Output:
	// {"collections":"0190f3c2-0000-7000-8000-000000000001"}
	// postgres WHERE EXISTS (SELECT 1 FROM "product_collections" WHERE "product_collections"."product_id" = "products"."id" AND "product_collections"."collection_id" = $1) ORDER BY "products"."created_at" DESC, "products"."id" DESC [0190f3c2-0000-7000-8000-000000000001]
	// sqlite   WHERE EXISTS (SELECT 1 FROM "product_collections" WHERE "product_collections"."product_id" = "products"."id" AND "product_collections"."collection_id" = ?1) ORDER BY "products"."created_at" DESC, "products"."id" DESC [0190f3c2-0000-7000-8000-000000000001]
	// {"collections":{"$nin":["0190f3c2-0000-7000-8000-000000000001"]}}
	// postgres WHERE NOT EXISTS (SELECT 1 FROM "product_collections" WHERE "product_collections"."product_id" = "products"."id" AND "product_collections"."collection_id" = ANY($1)) ORDER BY "products"."created_at" DESC, "products"."id" DESC [[0190f3c2-0000-7000-8000-000000000001]]
	// sqlite   WHERE NOT EXISTS (SELECT 1 FROM "product_collections" WHERE "product_collections"."product_id" = "products"."id" AND "product_collections"."collection_id" IN (SELECT value AS v FROM json_each(?1))) ORDER BY "products"."created_at" DESC, "products"."id" DESC [["0190f3c2-0000-7000-8000-000000000001"]]
	// {"reviews":{"$null":false}}
	// postgres WHERE EXISTS (SELECT 1 FROM "reviews" WHERE "reviews"."product_id" = "products"."id") ORDER BY "products"."created_at" DESC, "products"."id" DESC []
	// sqlite   WHERE EXISTS (SELECT 1 FROM "reviews" WHERE "reviews"."product_id" = "products"."id") ORDER BY "products"."created_at" DESC, "products"."id" DESC []
	// {"supplier_id":"0190f3c2-0000-7000-8000-0000000000aa"}
	// postgres WHERE "products"."supplier_id" = $1 ORDER BY "products"."created_at" DESC, "products"."id" DESC [0190f3c2-0000-7000-8000-0000000000aa]
	// sqlite   WHERE "products"."supplier_id" = ?1 ORDER BY "products"."created_at" DESC, "products"."id" DESC [0190f3c2-0000-7000-8000-0000000000aa]
}

// Example_include: the related IDs for a page's owners, capped per owner
// in SQL.
func Example_include() {
	tp := products(onPostgres)
	q, err := filter.BuildIncludeQuery(tp.FilterSchema().Fields["collections"], filter.PostgresDialect{},
		[]core.Key{{"0190f3c2-0000-7000-8000-000000000011"}, {"0190f3c2-0000-7000-8000-000000000012"}})
	if err != nil {
		panic(err)
	}
	fmt.Println(q.SQL)
	fmt.Println(q.Args)
	// Output:
	// SELECT CAST(include__.owner_pk AS TEXT), CAST(include__.target_id AS TEXT) FROM (SELECT owner_pk, target_id, ROW_NUMBER() OVER (PARTITION BY owner_pk ORDER BY target_id) AS include_rank FROM (SELECT DISTINCT "j"."product_id" AS owner_pk, "j"."collection_id" AS target_id FROM "product_collections" j WHERE "j"."product_id" = ANY($1)) pairs__) include__ WHERE include_rank <= 101 ORDER BY include__.owner_pk, include__.target_id
	// [[0190f3c2-0000-7000-8000-000000000011 0190f3c2-0000-7000-8000-000000000012]]
}

// Example_jsonDocuments: a JSON column is compared or contained as a whole
// document; a plain object is equality, $contains is jsonb @> at any depth.
func Example_jsonDocuments() {
	showFilter(products, `{"attrs":{"$contains":{"color":"red","dims":{"w":10}}}}`)
	showFilter(products, `{"tags":{"$contains":["sale"]}}`)
	undeclared := `{"attrs.color":"red"}`
	_, err := products(onPostgres).ParseFilter(undeclared)
	fmt.Println(undeclared)
	fmt.Println(err)
	// Output:
	// {"attrs":{"$contains":{"color":"red","dims":{"w":10}}}}
	// postgres WHERE "products"."attrs" @> $1::jsonb ORDER BY "products"."created_at" DESC, "products"."id" DESC [{"color":"red","dims":{"w":10}}]
	// sqlite   WHERE (CASE WHEN "products"."attrs" IS NULL THEN NULL ELSE (CASE WHEN json_type("products"."attrs") = 'object' THEN (EXISTS (SELECT 1 FROM json_each("products"."attrs") je1 WHERE je1.key = ?1 AND (je1.type = 'text' AND je1.atom = ?2)) AND EXISTS (SELECT 1 FROM json_each("products"."attrs") je2 WHERE je2.key = ?3 AND (CASE WHEN je2.type = 'object' THEN (EXISTS (SELECT 1 FROM json_each(je2.value) je3 WHERE je3.key = ?4 AND (je3.type IN ('integer', 'real') AND je3.atom = ?5))) ELSE 0 END))) ELSE 0 END) END) ORDER BY "products"."created_at" DESC, "products"."id" DESC [color red dims w 10]
	// {"tags":{"$contains":["sale"]}}
	// postgres WHERE "products"."tags" @> $1 ORDER BY "products"."created_at" DESC, "products"."id" DESC [[sale]]
	// sqlite   WHERE (CASE WHEN "products"."tags" IS NULL THEN NULL WHEN json_type("products"."tags") = 'array' THEN ((SELECT count(DISTINCT (CASE WHEN elem1__.type = 'text' THEN elem1__.value END)) FROM json_each(CASE WHEN json_type("products"."tags") = 'array' THEN "products"."tags" END) AS elem1__ WHERE (CASE WHEN elem1__.type = 'text' THEN elem1__.value END) IN (SELECT value AS v FROM json_each(?1))) = (SELECT count(DISTINCT v) FROM (SELECT value AS v FROM json_each(?1)) AS elem1__set)) ELSE 0 END) ORDER BY "products"."created_at" DESC, "products"."id" DESC [["sale"]]
	// {"attrs.color":"red"}
	// [E2071] field 'attrs.color' is not filterable
}

// Example_patches: a merge patch and a JSON Patch, each compiled to one
// SET clause with one expression per column.
func Example_patches() {
	tp := products(onPostgres)
	mergeBody := `{"name":"Cordless drill","stock":null,"attrs":{"color":"blue","legacy":null}}`
	merge, err := tp.ParseMergePatch([]byte(mergeBody))
	if err != nil {
		panic(err)
	}
	fmt.Println(mergeBody)
	for _, d := range []patch.SQLDialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		set, buildErr := patch.BuildPatchSQL(merge, d)
		if buildErr != nil {
			panic(buildErr)
		}
		fmt.Printf("%-8s SET %s %v\n", d.Name(), set.SetClause, set.Args)
	}
	opsBody := `[{"op":"array_append","path":"/tags","value":"clearance"},{"op":"replace","path":"/price","value":19.99}]`
	ops, err := tp.ParsePatch([]byte(opsBody))
	if err != nil {
		panic(err)
	}
	set, err := patch.BuildPatchSQL(ops, filter.PostgresDialect{})
	if err != nil {
		panic(err)
	}
	fmt.Println(opsBody)
	fmt.Printf("%-8s SET %s %v\n", "postgres", set.SetClause, set.Args)
	refusedBody := `[{"op":"add","path":"/reviews","value":["0190f3c2-0000-7000-8000-000000000002"]}]`
	_, err = tp.ParsePatch([]byte(refusedBody))
	fmt.Println(refusedBody)
	fmt.Println(err)
	// Output:
	// {"name":"Cordless drill","stock":null,"attrs":{"color":"blue","legacy":null}}
	// postgres SET "attrs" = (COALESCE(jsonb_path_query_first("attrs", 'strict $ ? (@.type() == "object")'), '{}'::jsonb) - $1::text[] || $2::jsonb), "name" = $3, "stock" = NULL [[legacy] {"color":"blue"} Cordless drill]
	// sqlite   SET "attrs" = json_patch(COALESCE("attrs", '{}'), ?1), "name" = ?2, "stock" = NULL [{"color":"blue","legacy":null} Cordless drill]
	// [{"op":"array_append","path":"/tags","value":"clearance"},{"op":"replace","path":"/price","value":19.99}]
	// postgres SET "tags" = array_append(COALESCE("tags", '{}'), $1), "price" = $2 [clearance 19.99]
	// [{"op":"add","path":"/reviews","value":["0190f3c2-0000-7000-8000-000000000002"]}]
	// [E2076] path '/reviews' at index 0: path not allowed
}

// ParseRequestFilter ANDs the JSON filter with the flat field parameters and
// bounds the combination as one filter: each part may be within MaxCost and
// the two together over it.
func TestParseRequestFilter(t *testing.T) {
	tp := products(onPostgres)
	parsed, err := tp.ParseRequestFilter(url.Values{
		"filter":    {`{"category":"tools"}`},
		"price_gte": {"10"},
		"sort":      {"-price"}, // a list parameter, not a filter
		"debug":     {"true"},   // the endpoint's own parameter
	}, filter.QueryParamAccept("debug"))
	require.NoError(t, err)
	lq, err := tp.ListQuery()
	require.NoError(t, err)
	res, err := lq.Build(filter.ListFilterAST(parsed.AST()))
	require.NoError(t, err)
	assert.Equal(t, `("products"."category" = $1 AND "products"."price" >= $2)`, res.Where)
	assert.Equal(t, []any{"tools", float64(10)}, res.Args)

	// Each part is affordable alone; together they are not.
	strict, err := parse.NewResource[Product](onPostgres, "products", parse.ParserOptions(core.WithMaxCost(25)))
	require.NoError(t, err)
	q := url.Values{"filter": {`{"name":{"$like":"a%"}}`}, "price_gte": {"10"}, "created_at_gte": {"2026-01-01T00:00:00Z"}}
	_, err = strict.ParseRequestFilter(url.Values{"filter": q["filter"]})
	require.NoError(t, err)
	_, err = strict.ParseRequestFilter(url.Values{"price_gte": q["price_gte"], "created_at_gte": q["created_at_gte"]})
	require.NoError(t, err)
	_, err = strict.ParseRequestFilter(q)
	assert.True(t, errors.Is(err, filter.ECodeQueryTooExpensive), "combined cost: %v", err)
}
