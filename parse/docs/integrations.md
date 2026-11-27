# Integrations

[Package overview](../README.md)

## Ownership

parse validates requests, compiles them under the server's scope, and runs them on the provider the application opened. It does not create tables, migrations or indexes, authenticate callers, retry writes, or emit audit events. Choose the smallest entry point your application needs.

| Existing application | Use |
|---|---|
| A list or update endpoint | A provider (`parsesql.Open` or `parsepgx.Open`, or `parseprovider.Open` to choose one by configuration), `parse.NewResource[T](provider, table)`, `List`, `Update` |
| A typed repository with its own statements | `Resource.Query` with `Columns`, or `ListQuery` / `ListPlan.Fragment` to compose the request into its SQL |
| Writes of its own that must commit with parse's | `Provider.Begin`, or its own transaction through `parsesql.Tx` / `parsepgx.Tx`, then `Resource.In` ([Transactions](#transactions)) |
| An existing SELECT or UPDATE builder | `filter.NewSQLBuilder` and placeholder offsets |
| A non-SQL store | Parsed filters plus your evaluator; typed patch application where supported |
| A generated API | Generate tags/schemas once, then call the same public parser APIs |
| A UI that builds filters dynamically | Schema registry plus the TypeScript validator/client |
| A generated client SDK | Its types and a thin binding over the TypeScript SDK or `parse/client` ([generated SDKs](../sdk/ts/docs/generated-sdks.md)) |

## Providers

A provider is the database every resource built on it runs on: SQLite or PostgreSQL, one API on either, chosen once at startup. They are alternatives for different runtime requirements — an embedded file or in-memory database, or a server — never layered or run side by side, and nothing moves data between them. Each provider is a module of its own, released with parse at one version, that owns its settings and how it opens: [`parsesql`](../parsesql/README.md) (`parsesql.Config`, `parsesql.Open`) and [`parsepgx`](../parsepgx/README.md) (`parsepgx.Config`, `parsepgx.Open`). An application requires the module of each provider it runs on, so a binary carries only those drivers. One that runs on either, chosen by its configuration, links both and opens the one named through [`parseprovider`](../parseprovider/README.md):

```go
// settings loaded with github.com/ashtonian/pkg/config: the provider's name,
// and each provider's own settings.
db, err := parseprovider.Open(ctx, settings.DB.Provider,
	parsesql.Choice(settings.DB.SQLite), parsepgx.Choice(settings.DB.Postgres))
defer db.Close()
devices, err := parse.NewResource[Device](db.Provider(), "devices", parse.TenantColumn("tenant_id"))
```

An application that opens its database itself hands it to the provider package instead: `parsesql.New(ctx, *sql.DB, opts...)` or `parsepgx.New(ctx, *pgxpool.Pool, opts...)`. Each checks the database once as it opens — SQLite's release (`parsesql.MinSQLiteVersion`), PostgreSQL's collation — so a database a plan cannot run on fails at startup rather than on the first request.

For lists:

1. `parsehttp.ParseList(r, devices)` decodes the query string (`parsehttp.Query`), or a POST body, and validates the JSON and flat filters, sort, cursor, page size, total, facets and includes.
2. `devices.List(ctx, req, parse.Scope{Tenant: tenant})` compiles it under the server's tenant, policy and deleted-row decision (`BuildList`), sends the page, count and facet statements together — one `pgx.Batch` on PostgreSQL — scans the rows through the [row contract](#row-contract), builds the page with its cursors, then reads the includes for the page's keys through `Page.SetInclude`. The page carries `page_info.total_count`, `facets` and `_relations` as the request asked. `devices.Facets(ctx, req, scope)` answers a facets request (`ParseFacets`) alone.

Each statement sees its own snapshot unless the resource runs in a transaction ([Transactions](#transactions)); on PostgreSQL run it in a REPEATABLE READ transaction when the total, facets and includes must describe exactly the page's rows. Give the request context a deadline.

For updates, route one row as `"PATCH /api/devices/" + pattern`, where `pattern, err := parsehttp.ItemPattern(devices)` holds a wildcard per key column in key order (`{id}`, or `{warehouse_id}/{sku}` for a key of two), and read its key with `key, err := parsehttp.ItemKey(r, devices)`. An item URL carries a path segment per key value, each escaped on its own (`/stock/w1/a%2Fb` for the key `("w1", "a/b")`), as `client.ItemPath(key...)` writes it, and `ItemKey` reads each as its column's text (`Resource.ParseKey`); a text its column cannot hold is `parse.ErrNoSuchKey`, a row not found. A batch route reads each `id[]` the same way, with `devices.ParseKey(id)`, and targets the rows with `parse.ByKeys(keys...)`. Then `parsehttp.ParsePatch(r, devices)` (the body bounded, its grammar named by its `Content-Type`: [reading a patch request](patches.md#reading-a-patch-request)) and `devices.Update(ctx, patch, parse.ByKey(key), scope)` applies the scoped `UPDATE … RETURNING`. Its `parse.UpdateResult` holds the target rows as the update left them: `Changed`, the rows whose columns it wrote, and `Unchanged`, the rows it left — every row of an empty or relation-only patch, and under `parse.SkipUnchanged()` the rows the patch already described ([Skipping unchanged rows](patches.md#skipping-unchanged-rows)). Neither holding a row means not found. A patch that changes relations runs the owner statement and the membership changes for the rows it matched in one transaction (a savepoint in the resource's transaction, under `In`), and `RelationRows` counts the junction rows they deleted and inserted. `devices.UpdateBatch(ctx, rows, scope, limits)` applies a [varied batch](operations.md#varied-native-update-batches) in one transaction.

A failed statement is a `*parse.StatementError` naming the resource's table and the statement (`page`, `owner`, `commit`, …) with the driver's error as its cause. An update's error means nothing it wrote is kept, except `parse.ErrCommitUnknown`: the `COMMIT` went unanswered and may have been applied, so read before retrying.

### PostgreSQL

[`parsepgx`](../parsepgx/README.md) is the PostgreSQL provider on a `*pgxpool.Pool`. The complete, compiled [pgx recipe](../parsepgx/recipe_test.go) declares a resource and serves its list and Merge Patch endpoints on `net/http`; `TestRecipe` runs it against PostgreSQL. Its options are the string collation (`parsepgx.Collation`, `pg_c_utf8` by default, `filter.CollationC` on PostgreSQL 14–16), the largest `$in` set a jsonb path compares by containment (`parsepgx.ContainmentMaxValues`) and the settle timeout (`parsepgx.SettleTimeout`).

The plans' arguments are typed slices and scalars, so every pgx `QueryExecMode` the pool's configuration selects — including `QueryExecModeExec` and the simple protocol — runs them. Interpolating values into SQL defeats parameterization; `pgx.NamedArgs` rewrites whole statements and is not a replacement for the plans' positional arguments.

### SQLite

[`parsesql`](../parsesql/README.md) is the SQLite provider on a `*sql.DB` of `modernc.org/sqlite`. The [example server](../examples/server/) runs on it by default: its [handlers](../examples/server/server.go) demonstrate combined filters, list pages, schema discovery, facets, and merge patches, and its tests execute the requests against SQLite.

SQLite has no row lock, so an update holds its targets only in a write transaction, and `database/sql` begins transactions in the mode the connection string names: `parsesql.Open` opens the database with `_txlock=immediate` and `_busy_timeout`, so a second writer waits for the first rather than failing with `SQLITE_BUSY`. A database opened by hand needs both (`sql.Open("sqlite", "./app.db?_txlock=immediate&_busy_timeout=5000")`), one connection for `:memory:`, which is a database per connection, and `parsesql.Register()` before it opens one: SQLite plans compare decimals, bound an int or float path's number members and decide geo filters through the functions it registers ([`parsesql` functions](../parsesql/README.md#functions)).

Each provider has its own capabilities as well as its own SQL. Read [backend semantics](queries.md#type-capabilities), especially search, case folding, and geo differences. A custom SQL expression must be appropriate for the provider's database.

## Transactions

A resource runs each `List`, `Facets` and `Query` in its own statements and each `Update` and `UpdateBatch` in a transaction of its own. To commit parse's writes with the application's, run the resource in one transaction:

```go
tx, err := db.Provider().Begin(ctx) // or parsesql.Tx(sqlTx), parsepgx.Tx(pgxTx): the application's own
defer tx.Rollback(ctx)              // nil once committed
scoped, err := devices.In(tx)
res, err := scoped.Update(ctx, patch, parse.ByID(id), scope)
// … the application's own statements in the same transaction …
err = tx.Commit(ctx)
```

`In` returns a view of the resource for the transaction's life: it parses and builds as the resource does, and declares no relation. An update in it runs under a savepoint, so a failed update leaves the transaction as it was and usable. A transaction another provider began, or a wrapped one of another kind of provider, is `parse.ErrProviderMismatch`; a wrapped transaction of the right kind must be on the provider's database, which its driver cannot tell. `Begin` begins without the context's cancellation, which `database/sql` would answer by rolling back; the transaction lasts until `Commit` or `Rollback`.

## Row contract

A row scans into the resource's type by column, never by position or by a driver's name mapping, which can disagree with the column a field compiles to: each column into the field the resource compiled it from, a stored document decoded with presence and key checks, SQLite's JSON and timestamp text decoded; fields of columns `parse.ReadColumns` leaves out stay zero, and a `parse.ReadExpr` column scans its expression's value. A statement of the application's own over the table selects `devices.Columns()`, in that order, and reads its rows through `devices.Query(ctx, parse.Statement{SQL: …, Args: …})`; one it runs on its own driver scans each row through `devices.RowScanner()`.

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

parse serves lists, facets, patches and schemas, not create, get or delete. A server's other routes are reached with `request`, through the client's transport, payload encoder and error decoding: `client.request(method, path, opts)` sends to any path below `baseURL`, a handle's `request(method, { key, path, query, body })` to its collection or to a row's path, a segment per key value as `patch` writes it. The caller states the response and body types, and `response: "none"` reads a 204 as `undefined` ([other routes](../sdk/ts/README.md#other-routes)). A generated TypeScript SDK keeps its types and a binding per operation and takes the client, the resource methods, `request`, the errors, iteration and validation from the package: [generated SDKs](../sdk/ts/docs/generated-sdks.md). A generated Go SDK's split is in `parse/client`'s README, [For generated SDKs](../client/README.md#for-generated-sdks).

## Go client

[`parse/client`](../client/README.md) builds requests from Go and decodes their answers. It is a module of its own (`go get github.com/ashtonian/pkg/parse/client@latest`) that requires nothing beyond the standard library, so a Go SDK built on it carries no server code. A catalog of typed fields per resource, usually generated, fixes each field's name and operand type, so a misspelt field or a wrongly typed value does not compile:

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

The answers decode into the client's types, generic over the row type: `client.Page[Product]` for a list, `client.FacetsAnswer` for its facets, `client.PatchAnswer[Product]` for a patch of one row or a batch, `client.Schema` for the schema document. A refused request reads with `client.DecodeError(status, body)`, an `*client.Error` matched with `errors.As` whose code is an `errors.Is` target (`client.CodeUnknownField`). The client's tests decode every answer this package's handlers recorded on every engine (`testdata/sdk_wire.json`, regenerated with `go test -tags=integration -run 'TestCorpus/SDKWire' ./parsesql -update`) and encode it back unchanged. The client README's [For generated SDKs](../client/README.md#for-generated-sdks) says what a generator emits per resource.

## Custom schemas and SQL expressions

Use `core.WithGeneratorOptions` to change table/column conventions or supply a `core.SQLColumnMapper`. For lower-level control, build `filter.ResourceSchema`, sort/search schemas, and patch paths explicitly. SQL expressions supplied by your mapper are trusted application code, never request text.

Declare a value the table holds in no column with `parse.ReadExpr`, one SQL expression per dialect, rather than a hand-written projection ([computed columns](queries.md#computed-columns)). Declare typed document paths with `Resource.WithPaths` / `filter.PathDecl` to share filter, sort, facet and cursor semantics. A manual `FieldSchema.Expr` remains trusted application code and needs a matching type and backend interpretation. Prefer an indexed column when that is the intended storage model.
