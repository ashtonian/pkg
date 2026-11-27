//go:build unit

package parse_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsesql"
	"github.com/ashtonian/pkg/parse/parsesqlite"
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
		return &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: field, Operator: filter.OpEq, Value: v}}}
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
