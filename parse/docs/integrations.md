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
| A generated client SDK | Its types and a thin binding over the TypeScript SDK or `parse/client` ([generated SDKs](../sdk/ts/docs/generated-sdks.md)) |

## pgx

[`parsepgx`](../parsepgx/README.md) runs a resource's plans on PostgreSQL. `*pgxpool.Pool`, `*pgx.Conn` and `pgx.Tx` all satisfy its `Querier` / `DB` interfaces. The complete, compiled [pgx recipe](../parsepgx/recipe_test.go) declares a resource and serves its list and Merge Patch endpoints on `net/http`; `TestRecipe` runs it against PostgreSQL.

For lists:

1. `parsehttp.ParseList(r, devices)` decodes the query string (`parsehttp.Query`) and validates the JSON and flat filters, sort, cursor, page size, total, facets and includes.
2. `devices.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: tenant})` compiles it under the server's tenant, policy and deleted-row decision.
3. `parsepgx.QueryPage(ctx, pool, devices, plan)` sends the page, count and facet statements in one `pgx.Batch`, scans rows through the resource's row contract (`devices.RowScanner`: each column into the field the resource compiled it from; fields of columns `parse.ReadColumns` leaves out stay zero, and a `parse.ReadExpr` column scans its expression's value), builds the page, then reads the includes for the page's keys in a second batch through `Page.SetInclude`. The page carries `page_info.total_count`, `facets` and `_relations` as the request asked.

Each statement sees its own snapshot on a pool; pass a REPEATABLE READ `pgx.Tx` when the total, facets and includes must describe exactly the page's rows. Give the request context a deadline.

For updates, route one row as `"PATCH /api/devices/" + pattern`, where `pattern, err := parsehttp.ItemPattern(devices)` holds a wildcard per key column in key order (`{id}`, or `{warehouse_id}/{sku}` for a key of two), and read its key with `key, err := parsehttp.ItemKey(r, devices)`. An item URL carries a path segment per key value, each escaped on its own (`/stock/w1/a%2Fb` for the key `("w1", "a/b")`), as `client.ItemPath(key...)` writes it, and `ItemKey` reads each as its column's text (`Resource.ParseKey`); a text its column cannot hold is `parse.ErrKeyInvalid`, a row not found. Then `parsehttp.ParsePatch(r, devices)` (the body bounded, its grammar named by its `Content-Type`: [reading a patch request](patches.md#reading-a-patch-request)) and `devices.BuildUpdate(filter.PostgresDialect{}, patch, parse.ByKey(key), scope)` compiles the scoped `UPDATE … RETURNING`, and `parsepgx.Update(ctx, pool, devices, plan)` applies it. Its `parse.UpdateResult` holds the target rows as the update left them: `Changed`, the rows whose columns it wrote, and `Unchanged`, the rows it left — every row of an empty or relation-only patch, and under `parse.SkipUnchanged()` the rows the patch already described ([Skipping unchanged rows](patches.md#skipping-unchanged-rows)). Neither holding a row means not found. A patch that changes relations runs the owner statement and the membership changes for the rows it matched in one transaction (a savepoint in a caller's `pgx.Tx`), and `RelationRows` counts the junction rows they deleted and inserted. `parsepgx.UpdateBatch(ctx, pool, devices, batch)` applies a [batch plan](operations.md#varied-native-update-batches) in one transaction.

The plans' arguments are typed slices and scalars, so every pgx `QueryExecMode` — including `QueryExecModeExec` and the simple protocol — runs them. Preserve the driver's argument handling: interpolating values into SQL defeats parameterization. `pgx.NamedArgs` rewrites whole statements and is not a replacement for the plan's positional arguments.

## database/sql and SQLite

The [example server](../examples/server/) uses `net/http`, `database/sql`, and SQLite. Its [handlers](../examples/server/server.go) demonstrate combined filters, list pages, schema discovery, facets, and merge patches; its tests execute the requests against SQLite.

[`parsesql`](../parsesql/README.md) runs SQLite plans through `database/sql`, with the same calls and results as `parsepgx`: `parsesql.QueryPage(ctx, db, devices, plan)` returns the page with its total, facets and includes (through `Page.SetInclude`, which omits empty projections), `parsesql.Update(ctx, db, devices, plan)` returns the `parse.UpdateResult`, and `parsesql.UpdateBatch` applies a batch plan. Rows scan through the resource's row contract (`devices.RowScanner(d)`: every column into the field the resource compiled it from, SQLite's JSON text and timestamp text decoded). `db` is a `*sql.DB` or `*sql.Conn`, on which an update that reads before it writes or changes relations begins its own transaction, or a `*sql.Tx`, in which it runs under a savepoint the caller's rollback undoes.

SQLite has no row lock, so such an update holds its targets only in a write transaction, and `database/sql` begins transactions in the mode the connection string names: open the database with `_txlock=immediate`, and with `_busy_timeout` so a second writer waits for the first rather than failing with `SQLITE_BUSY` (`sql.Open("sqlite", "app.db?_txlock=immediate&_busy_timeout=5000")`; `modernc.org/sqlite` and `mattn/go-sqlite3` both read them). Begin a `*sql.Tx` you pass in on such a connection.

Call `parsesqlite.Register()` before the database opens a connection: SQLite plans compare decimals and decide geo filters through the functions it registers with `modernc.org/sqlite` ([`parsesqlite`](../parsesqlite/README.md)); another driver registers every entry of `parsesqlite.Functions()` under its name, as deterministic ([another SQLite driver](../parsesqlite/README.md#another-sqlite-driver)). A statement of your own over the table reads `devices.Columns(d)` and scans through `parsesql.Query` or `devices.RowScanner(d)`, never by position or by a driver's own name mapping, which can disagree with the column a field compiles to. The compiler does not create tables, migrations, indexes, or SQLite connections.

Changing dialect selects SQL generation, not identical capabilities. Read [backend semantics](queries.md#type-capabilities), especially search, case folding, and geo differences. A custom SQL expression must be appropriate for the selected database.

## TypeScript client

The [TypeScript SDK](../sdk/ts/README.md) provides `createClient`, `filter`, schema discovery, client-side validation, cursor pagination, facets, Merge Patch requests, and a typed request method for a server's other routes. See its README for package setup, every client option, endpoints, and complete examples.

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

The client needs a server that implements the [HTTP contract](http-errors.md). It does not turn arbitrary endpoints into that contract. Schema validation is a convenience, never authorization: the Go server remains authoritative. Client patch validation covers the advertised path, value, enum, bounds, numeric-kind, relation and element metadata. Server authorization and runtime constraints remain authoritative; JavaScript validation does not replace backend execution contracts. JavaScript numbers cannot represent every Go integer exactly; keep that limitation in mind when designing numeric identifiers and payloads. A `NaN` or infinite number anywhere in a patch or filter, a JSON document's members included, is refused before sending (`NonFiniteNumberError`, naming the member), as Go's `encoding/json` refuses one, rather than sent as the `null` a merge patch reads as a removal.

`patch(key, input)` takes a row's key: one value, or for a key of several columns a tuple of a value per column in key order, the handle typed `resource<Stock, [string, string]>("stock")` so a key of another arity does not compile. It sends `PATCH /stock/w1/a%2Fb`, the wire form `parsehttp.ItemKey` reads. `patchMany`'s `id[]` holds one value per row, so it addresses only a key of one column.

parse serves lists, facets, patches and schemas, not create, get or delete. A server's other routes are reached with `request`, through the client's transport, payload encoder and error decoding: `client.request(method, path, opts)` sends to any path below `baseURL`, a handle's `request(method, { key, path, query, body })` to its collection or to a row's path, a segment per key value as `patch` writes it. The caller states the response and body types, and `response: "none"` reads a 204 as `undefined` ([other routes](../sdk/ts/README.md#other-routes)). A generated TypeScript SDK keeps its types and a binding per operation and takes the client, the resource methods, `request`, the errors, iteration and validation from the package: [generated SDKs](../sdk/ts/docs/generated-sdks.md). A generated Go SDK's split is in [`parse/client`'s README](../client/README.md), in its section for generated SDKs.

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

Declare a value the table holds in no column with `parse.ReadExpr`, one SQL expression per dialect, rather than a hand-written projection ([computed columns](queries.md#computed-columns)). Declare typed document paths with `Resource.WithPaths` / `filter.PathDecl` to share filter, sort, facet and cursor semantics. A manual `FieldSchema.Expr` remains trusted application code and needs a matching type and backend interpretation. Prefer an indexed column when that is the intended storage model.
