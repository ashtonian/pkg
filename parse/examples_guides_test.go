//go:build unit

package parse_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
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

// showList compiles one list request for both dialects and prints its
// WHERE, ORDER BY and bound arguments.
func showList[T any](r *parse.Resource[T], opts ...filter.ListOption) {
	for _, d := range []filter.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		lq, err := r.ListQuery(d)
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		res, err := lq.Build(opts...)
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

// showFilter prints a JSON filter, then what it compiles to on each dialect.
func showFilter[T any](r *parse.Resource[T], f string) {
	fmt.Println(f)
	showList(r, filter.ListFilter(f))
}

// Example_arraysOfScalars: a condition on one element of an array column
// unnests it; on an array path it reads the member's array elements.
func Example_arraysOfScalars() {
	orders := parse.MustResource[Order]("orders")
	showFilter(orders, `{"scores":{"$some":{"$gte":5,"$lt":10}}}`)
	showFilter(orders, `{"doc.tags":{"$some":{"$like":"a%"}}}`)
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
	orders := parse.MustResource[Order]("orders")
	showFilter(orders, `{"items":{"$some":{"sku":"a","qty":{"$gt":2}}}}`)
	// Output:
	// {"items":{"$some":{"sku":"a","qty":{"$gt":2}}}}
	// postgres WHERE EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof("orders"."items") = 'array' THEN "orders"."items" END) AS elem1__(v) WHERE ((CASE WHEN jsonb_typeof(elem1__.v -> 'qty') = 'number' THEN (elem1__.v ->> 'qty')::numeric END) > $1 AND elem1__.v @> $2::jsonb)) ORDER BY "orders"."id" COLLATE pg_c_utf8 ASC [2 {"sku":"a"}]
	// sqlite   WHERE EXISTS (SELECT 1 FROM json_each(CASE WHEN json_type("orders"."items") = 'array' THEN "orders"."items" END) AS elem1__ WHERE ((CASE WHEN json_type((CASE WHEN elem1__.type = 'object' THEN elem1__.value END), '$."qty"') IN ('integer', 'real') THEN json_extract((CASE WHEN elem1__.type = 'object' THEN elem1__.value END), '$."qty"') END) > ?1 AND (CASE WHEN json_type((CASE WHEN elem1__.type = 'object' THEN elem1__.value END), '$."sku"') = 'text' THEN json_extract((CASE WHEN elem1__.type = 'object' THEN elem1__.value END), '$."sku"') END) = ?2)) ORDER BY "orders"."id" COLLATE BINARY ASC [2 a]
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

func noteResource() *parse.Resource[Note] {
	return parse.MustResource[Note]("notes", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
}

// A governed list reads the rows a governance tree admits, of any tenant:
// here tenant A's documents and B's b1. A related row is compared with the
// tenant of the listed row it hangs from, not bound to one tenant.
func ExampleResource_BuildList_governed() {
	documents := parse.MustResource[Document]("documents", parse.TenantColumn("tenant_id"))
	documents.MustRelate("notes", parse.HasMany(noteResource(), "document_id", parse.Indexed()))
	eq := func(field string, v any) *filter.PolicyConstraints {
		return &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: field, Operator: filter.OpEq, Value: v}}}
	}
	governance := &filter.PolicyConstraints{Or: []*filter.PolicyConstraints{eq("tenant_id", "A"), eq("id", "b1")}}

	req, err := documents.ParseListInput(parse.ListInput{Filter: `{"notes":{"$some":{"body":"urgent"}}}`})
	if err != nil {
		fmt.Println(err)
		return
	}
	plan, err := documents.BuildList(filter.PostgresDialect{}, req, parse.Scope{Governance: governance})
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
	notes := noteResource()
	parsed, err := notes.ParseMergePatch([]byte(`{"title":"Agenda","rank":2}`))
	if err != nil {
		fmt.Println(err)
		return
	}
	plan, err := notes.BuildUpdate(filter.PostgresDialect{}, parsed, parse.ByID("n1"), parse.Scope{Tenant: "t1"},
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
	// UPDATE "notes" SET "rank" = $1, "title" = $2, "updated_at" = NOW(), "version" = "version" + 1 WHERE ("notes"."tenant_id" = $3 AND "notes"."deleted_at" IS NULL AND "notes"."id" = $4) AND ("rank" IS DISTINCT FROM $1 OR "rank"::text COLLATE "C" IS DISTINCT FROM ($1)::text OR "title" IS DISTINCT FROM $2 OR "title"::text COLLATE "C" IS DISTINCT FROM ($2)::text) RETURNING "notes"."id", "notes"."tenant_id", "notes"."document_id", "notes"."title", "notes"."body", "notes"."rank", "notes"."version", "notes"."updated_at", "notes"."deleted_at"
	// [2 Agenda t1 n1]
	// SELECT "notes"."id", "notes"."tenant_id", "notes"."document_id", "notes"."title", "notes"."body", "notes"."rank", "notes"."version", "notes"."updated_at", "notes"."deleted_at" FROM "notes" WHERE ("notes"."tenant_id" = $1 AND "notes"."deleted_at" IS NULL AND "notes"."id" = $2) FOR UPDATE
	// [t1 n1]
}

type Tag struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Label     string     `json:"label"      db:"label"      parse:"filter"`
	DeletedAt *time.Time `json:"-"          db:"deleted_at"`
}

// A membership change pairs only owners and targets the request may relate:
// the INSERT selects both rows, live and in the tenant, rather than binding
// raw keys, and each side's keys bind as one array parameter.
func ExampleResource_RelationStatements() {
	devices := parse.MustResource[Device]("devices", parse.TenantColumn("tenant_id"))
	tags := parse.MustResource[Tag]("tags", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	devices.MustRelate("tags", parse.ManyToMany(tags, parse.Junction{Table: "device_tags", Source: "device_id", Target: "tag_id", Tenant: "tenant_id"}, parse.Writable()))

	parsed, err := devices.ParsePatch([]byte(`[{"op":"add","path":"/tags","value":["red","blue"]}]`))
	if err != nil {
		fmt.Println(err)
		return
	}
	plan, err := devices.BuildUpdate(filter.PostgresDialect{}, parsed, parse.ByID("d1"), parse.Scope{Tenant: "t1"})
	if err != nil {
		fmt.Println(err)
		return
	}
	// The keys Owner returned; the executors read them from its rows.
	stmts, err := devices.RelationStatements(plan, []any{"d1"})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, s := range stmts {
		fmt.Println(s.SQL)
		fmt.Println(s.Args)
	}
	// Output:
	// INSERT INTO "device_tags" ("device_id", "tag_id", "tenant_id") SELECT "owner__"."id", "target__"."id", "owner__"."tenant_id" FROM "devices" owner__, "tags" target__ WHERE "owner__"."id" = ANY($1) AND "owner__"."tenant_id" = $2 AND "target__"."id" = ANY($3) AND "target__"."tenant_id" = $4 AND "target__"."deleted_at" IS NULL ON CONFLICT DO NOTHING
	// [[d1] t1 [red blue] t1]
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
	teams := parse.MustResource[Team]("teams")
	members := parse.MustResource[Member]("members", parse.ReadColumns("id"), parse.QueryOnlyColumns("team_id"))
	fmt.Println(teams.Relate("members", parse.HasMany(members, "team_id", parse.Includable())))
	// Output:
	// parse: teams: relation "members": members does not read column "team_id"
}

type Account struct {
	ID       int     `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string  `json:"-"         db:"tenant_id"`
	GroupIDs []int64 `json:"group_ids" db:"group_ids"`
}

// A computed column whose expression serves PostgreSQL only fails every
// build for SQLite, naming the table, the column and the dialect.
func ExampleReadExpr() {
	accounts := parse.MustResource[Account]("accounts",
		parse.TenantColumn("tenant_id"),
		parse.ReadExpr("group_ids", func(d filter.Dialect) (string, error) {
			if _, ok := d.(filter.PostgresDialect); ok {
				return "array(SELECT g.group_id FROM account_groups g WHERE g.account_id = accounts.id ORDER BY g.group_id)", nil
			}
			return "", filter.ErrUnsupportedDialect
		}),
	)
	columns, err := accounts.Columns(filter.PostgresDialect{})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(strings.Join(columns, ", "))
	_, err = accounts.Columns(filter.SQLiteDialect{})
	fmt.Println(err)
	// Output:
	// "accounts"."id", "accounts"."tenant_id", (array(SELECT g.group_id FROM account_groups g WHERE g.account_id = accounts.id ORDER BY g.group_id)) AS "group_ids"
	// parse: accounts: computed column "group_ids" for dialect sqlite: unsupported SQL dialect
}
