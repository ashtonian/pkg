# Integrations

[Package overview](../README.md)

## Ownership

The core translates requests into validated values and plans. It does not select your database connection, scan rows, authenticate callers, open transactions, retry writes, or emit application audit events. Choose the smallest entry point your existing repository needs.

| Existing application | Use |
|---|---|
| A list or update endpoint on PostgreSQL | `parse.NewResource[T]` and `parsepgx` |
| A list or update endpoint on another driver | `parse.NewResource[T]`: `ParseList`, `BuildList`, `Page`, `BuildUpdate` |
| A typed resource repository with its own statements | `parse.NewResource[T](table)` and `ListQuery` |
| An existing SELECT or UPDATE builder | `filter.NewSQLBuilder` and placeholder offsets |
| A non-SQL store | Parsed filters plus your evaluator; typed patch application where supported |
| A generated API | Generate tags/schemas once, then call the same public parser APIs |
| A UI that builds filters dynamically | Schema registry plus the TypeScript validator/client |

## pgx

[`parsepgx`](../parsepgx/README.md) runs a resource's plans on PostgreSQL. `*pgxpool.Pool`, `*pgx.Conn` and `pgx.Tx` all satisfy its `Querier` / `DB` interfaces. The complete, compiled [pgx recipe](../parsepgx/recipe_test.go) declares a resource and serves its list and Merge Patch endpoints on `net/http`; `TestRecipe` runs it against PostgreSQL.

For lists:

1. `devices.ParseList(r.URL.Query())` validates the JSON and flat filters, sort, cursor, page size, total, facets and includes.
2. `devices.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: tenant})` compiles it under the server's tenant, policy and deleted-row decision.
3. `parsepgx.QueryPage(ctx, pool, devices, plan)` sends the page, count and facet statements in one `pgx.Batch`, scans rows with `pgx.RowToStructByNameLax[T]` (the `db` tags that named the columns; fields of columns `parse.ReadColumns` leaves out stay zero), builds the page, then reads the includes for the page's keys in a second batch. The page carries `page_info.total_count`, `facets` and `_relations` as the request asked.

Each statement sees its own snapshot on a pool; pass a REPEATABLE READ `pgx.Tx` when the total, facets and includes must describe exactly the page's rows. Give the request context a deadline.

For updates, `devices.ParseMergePatch(body)` then `devices.BuildUpdate(filter.PostgresDialect{}, patch, parse.ByID(id), scope)` compiles the scoped `UPDATE … RETURNING`, and `parsepgx.Update(ctx, pool, devices, plan)` applies it: no rows means not found. A patch that changes relations runs the owner statement and the membership changes for the rows it matched in one transaction.

The plans' arguments are typed slices and scalars, so every pgx `QueryExecMode` — including `QueryExecModeExec` and the simple protocol — runs them. Preserve the driver's argument handling: interpolating values into SQL defeats parameterization. `pgx.NamedArgs` rewrites whole statements and is not a replacement for the plan's positional arguments.

## database/sql and SQLite

The [example server](../examples/server/) uses `net/http`, `database/sql`, and SQLite. Its [handlers](../examples/server/server.go) demonstrate combined filters, list pages, schema discovery, facets, and merge patches; its tests execute the requests against SQLite.

Run `plan.Page` with `QueryContext`, scan each row into `T` in `devices.Columns(d)` order — `devices.ColumnNames()` names each value, whatever expression the dialect read it with — and pass the rows to `devices.Page(rows, plan)`. Run `plan.Count` and `plan.Facets` (`filter.ScanFacetUnion`, then `filter.FacetMap` into `page.Facets`) when present, then `devices.IncludeQueries(plan, keys)` for the page's keys (`devices.Key`), reading each with its `Scan` into `page.Relations` and `page.Truncated`. Updates run `plan.Owner` and then `devices.RelationStatements(plan, keys)` for the returned rows in one `*sql.Tx`.

Use `QueryContext` / `ExecContext` with SQL and arguments from the plan. Decode JSON text into the corresponding Go document or slice; scan scalar/date representations consistently with the model. Close rows and check iteration errors. The compiler does not create tables, migrations, indexes, or SQLite connections.

Changing dialect selects SQL generation, not identical capabilities. Read [backend semantics](queries.md#type-capabilities), especially search, case folding, and geo differences. A custom SQL expression must be appropriate for the selected database.

## TypeScript client

The [TypeScript SDK](../sdk/ts/README.md) provides `createClient`, `filter`, schema discovery, client-side validation, cursor pagination, facets, and Merge Patch requests. See its README for package setup, every client option, endpoints, and complete examples.

```typescript
import { createClient, filter } from "@ashtonian/parse";

interface Product {
  id: string;
  name: string;
  price: number;
}

const products = createClient({
  baseURL: "https://api.example.com",
  validateBeforeSend: true,
}).resource<Product>("products");

const page = await products.list({
  filter: filter<Product>().where("price", { $lt: 20 }).build(),
  sort: "name",
  pageSize: 25,
});
console.log(page.items);
```

The client needs a server that implements the [HTTP contract](http-errors.md). It does not turn arbitrary endpoints into that contract. Schema validation is a convenience, never authorization: the Go server remains authoritative. Client patch validation covers the advertised path, value, enum, numeric-kind, relation and element metadata. Server authorization and runtime constraints remain authoritative; JavaScript validation does not replace backend execution contracts. JavaScript numbers cannot represent every Go integer exactly; keep that limitation in mind when designing numeric identifiers and payloads.

## Go client

[`parse/client`](../client/README.md) builds requests from Go with only the standard library. A catalog of typed fields per resource, usually generated, fixes each field's name and operand type, so a misspelt field or a wrongly typed value does not compile:

```go
var Products = struct {
	Name  client.SortableText
	Price client.SortableOrdered[float64]
	Tags  client.Relation[string]
}{client.NewSortableText("name"), client.NewSortableOrdered[float64]("price"), client.NewRelation[string]("tags")}

q, err := client.ListParams{
	Filter:   client.And(Products.Price.Lt(20), Products.Tags.In("sale")),
	Sort:     client.Sort{Products.Name.Asc()},
	PageSize: 25,
}.Values()
```

Patches are a struct of `client.Nullable` members with the `omitzero` option (a merge patch), or a `[]client.Operation` (a JSON Patch, which also adds and removes relation members). The client sends only what the grammar can express and leaves every other check to the server; its tests hold it to the same filter and patch corpora as the TypeScript SDK.

## Custom schemas and SQL expressions

Use `core.WithGeneratorOptions` to change table/column conventions or supply a `core.SQLColumnMapper`. For lower-level control, build `filter.ResourceSchema`, sort/search schemas, and patch paths explicitly. SQL expressions supplied by your mapper are trusted application code, never request text.

Declare typed document paths with `Resource.WithPaths` / `filter.PathDecl` to share filter, sort, facet and cursor semantics. A manual `FieldSchema.SQLExpr` remains trusted application code and needs a matching type and backend interpretation. Prefer an indexed column when that is the intended storage model.
