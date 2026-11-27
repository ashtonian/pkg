# Security, concurrency, and performance

[Package overview](../README.md)

## Trust boundaries

| Input or component | Responsibility |
|---|---|
| Filter and patch values | Parsed against exposed fields/operators and passed as bound arguments |
| Table/column/relation tags | Trusted schema; generated identifiers are validated at construction |
| Custom column expressions and timestamp SQL | Trusted application code; never accept these from a request |
| Base-row authorization | Supply server-side policy on every query, count, facet, and write |
| Relation tenant scope | Bind the trusted tenant separately for junction operations; never derive it from the client's filter |
| HTTP body/query limits | Bound reads at the HTTP server as well as using parser limits |
| SQL/driver errors | Handle at the application boundary without leaking private data |

`filter.ListPolicy` ANDs server constraints with the caller's filter, so a caller can narrow but cannot replace those predicates. Relation tenant options scope junction rows; they do not automatically add the base resource's tenant predicate. Unscoped relation options are explicit tools for trusted platform code, not request-controlled switches.

A cursor is a position, not a permission. Always reapply authorization, soft-delete policy, and tenant scope after decoding it. Schema discovery describes capabilities, not per-user write permission. `parse:"server"` belongs only on fields written through trusted `ParseServerMergePatch` calls.

### Server errors

Errors the root package returns read `parse: <table>: <operation or field>: <cause>`, naming the package and the table once; a sentinel (`ErrTenantRequired`, `ErrTenantInvalid`, `ErrKeyInvalid`, `ErrNoTarget`, `ErrUpdateScope`, `ErrForeign`, `ErrRegistered`, `ErrUpdateBatchTargets`) carries no prefix of its own, so branch on it with `errors.Is`. These are the server's mistakes, never the request's: none is a `core.ParseError`, and none should reach a client as a 4xx. `ErrNoSuchKey` is the exception, the request's: `ParseKey` (`parsehttp.ItemKey`) read an item route's segment that no key column can hold (`parse: devices: no key of the key columns has this text: key column id: "abc" is not a decimal integer`), so no row has that key; answer it 404.

- `ErrCommitUnknown`, `parse: devices: commit: commit outcome unknown: …`: the PostgreSQL provider sent an update's `COMMIT` and the connection failed, or the settle timeout (`parsepgx.SettleTimeout`, `parsepgx.Config`'s `settle_timeout`) ran out, before the database answered. The update may have been applied: read the rows back before retrying a write that is not idempotent. Every other update error means nothing was written. SQLite's commit never leaves its outcome unknown.
- `*StatementError`, for example `parse: devices: page: context deadline exceeded` or `parse: devices: owner: column name: …`: a statement the database failed, or whose rows did not scan. `Table` and `Statement` name it for a log attribute or a metric label, and the cause is the driver's error: check the request's deadline, the pool, and that the table's columns are the ones the resource reads.
- `ErrProviderMismatch`, `parse: devices: sqlite transaction for a resource on postgres: …`: `In` was given a transaction of another provider, or `Relate` a target built on another. Build every resource of one database on its one provider.
- `ErrTenantRequired`, `parse: devices: a tenant-scoped resource needs Scope.Tenant`: a list (without `Scope.Governance` either) or an update of a resource with a tenant column was built without the server's tenant. Check the middleware that sets `Scope.Tenant`.
- `ErrKeyInvalid`, for example `parse: devices: target key: not a value of the key column's type: expected string, got int`, or `update batch row 2 key: …` from `BuildUpdateBatch`: the key `ByID`, `ByIDs`, `ByKey`, `ByKeys` or a batch row named is not of the key column's Go type, or not a UUID for a UUID key, or a `parse.Key` has another number of values than the resource has key columns (`… 1 values for the 2 key columns (warehouse_id, sku)`). Convert the route parameter to the key's type before building; nothing was sent to the database.

- `filter.ErrPolicyInvalid`, for example `filter: invalid policy constraint: field "owner_org" is not a field of devices`: a `Scope.Policy`, `Scope.Related` or `Scope.Governance` constraint does not compile against the schema. It is the server's policy, so the list fails closed and the handler answers 500; fix the policy builder for the field, operator or value it names.
- `filter.ErrRelatedPolicyRequired`, `relation type on device_types: filter: a policy-scoped relation is built without the target's policy`: the request used a `PolicyScoped()` relation and `Scope.Related` has no entry for the target table. Add the target's policy to the scope the handler builds.
- `filter.ErrRelationTenantRequired`, `relation table board_labels (tenant column tenant_id): filter: a tenant-scoped pair table is joined without a tenant…`: a resource without a tenant column joined or looked up a pair table with one, and the scope named no tenant. Set `Scope.Tenant`, or `RelationScope.Unscoped` for platform tooling only.

### Tenant key errors

- `ErrTenantInvalid`, for example `parse: devices: Scope.Tenant is not a value of the tenant column's type: commands.tenant_id: 3000000000 overflows int32`: the server's tenant does not fit the named column, which the resource reaches through a relation (a bare column name is the resource's own). Check where the server reads the tenant. If the tenant is right, the named field is narrower than the tenant keys: widen it in Go and in the database. Nothing was sent to the database.
- A `Relate` error naming two tenant columns that "hold different types": one request would bind its tenant to a string or UUID column and an integer one. Give them one type (strings and UUIDs agree, as do integers of any width). The error names the resource that reaches both when it is not the one being related.
- A pair table's tenant column binds the tenant as the type of the typed tenant columns its resource reaches ([Tenant key](relations.md#tenant-key)). A driver error comparing it means the database column is of another type.

## Concurrency and ownership

Construct a resource once and share it. Its schemas are fixed when `NewResource` returns: customize them in `parse.CustomizeSchemas`, which runs before the read-set and column checks, so a customized field over a column `ReadColumns` withholds fails construction as a generated one does. `FilterSchema`, `SortSchema`, `SearchSchema`, `PatchSchema` and `ServerPatchSchema` return deep copies, as do the registry's `FilterSchema`, `PatchSchema` and `SortSchema`; changing a copy changes nothing the resource parses. `Relate` is the one change after construction; declare relations before serving. Registry `SchemaFor` discovery responses are independent metadata snapshots. The field walks schemas are built from (`core.StructFieldsOf`, `core.JSONFieldsOf`, `SchemaGenerator.StructFields`), and the document shapes stored documents are checked against, are shared by the whole process and read-only: their accessors return shared handles or copies the caller owns, so nothing a caller does with them reaches another generator, resource or stored-document check. `ListQuery.WithFilterConfig` and `WithSearchSchema` configure a builder: do not mutate a shared builder while requests are using it.

A resource compiles and applies only what it parsed itself. `ListRequest` and `filter.ParsedFilter` hold their fields behind methods and carry the schema that parsed them; `patch.ParsedPatch` carries its patch schema. `BuildList`, `BuildUpdate` (its patch and a `ByFilter` target), `BuildUpdateBatch` and `ApplyValidatedPatch` return `parse.ErrForeign` for a value another resource parsed, over the same table or not, and for one assembled by hand. A `WithPaths` view has its own filter schema, so a view and its base each refuse the other's list requests and filters; they share patch schemas, so each accepts the other's patches. Server code that makes a request without a transport parses it with the typed entry points: `ParseListInput`, `ParseFilterMap`, `MergePatchMap`, `ValidateOperations`. To compile a request whose memberships were resolved (`filter.ResolveMemberships`, which keeps the filter's schema), use `ListRequest.WithFilter`.

Treat parsed filters, patch operations, policies, and user-supplied maps as request-owned values. Do not mutate them during compilation/application. Custom codecs, clocks, validators, and column mappers must be safe for the concurrency with which the caller uses them.

`ApplyValidatedPatch` does not mutate the input with the built-in codec. That does not make the result a general deep-clone API; unchanged reference-valued fields can remain shared. Synchronize access to shared application state separately.

For read/modify/write patches (`test`, `copy`, `move`, nested pointers), protect the read and subsequent persistence with a transaction/lock or optimistic version check. A successful test against a Go value does not establish that the database row is still unchanged. Relation replacements likewise require transaction-level serialization where concurrent updates can conflict.

### Varied native update batches

`Resource.UpdateBatch(ctx, []RowUpdate, scope, UpdateBatchLimits, options...)`
applies distinct column patches to unique primary keys. Each `RowUpdate` has
`Key`, `Patch` and an optional narrowing `Policy` for its exact revision and
authorization predicate. The common scope's tenant, live-row and policy floors
apply to every item. The scope is checked before any item: a missing tenant
(`ErrTenantRequired`) or one the resource's tenant columns cannot hold
(`ErrTenantInvalid`) refuses the batch, empty or not, and the tenant is
normalized once for every item. It grants no authority; `BuildUpdateBatch`
returns the statements without running them.

The batch runs in one transaction (a savepoint inside the resource's, under
`In`): every chunk's `Current` first, each required to hold exactly its
chunk's keys, then every `Owner` in chunk order. A target that is missing,
stale, inaccessible or invalid returns `ErrUpdateBatchTargets` and writes
nothing. PostgreSQL locks the targets in key order; on SQLite the connection
begins write transactions (`_txlock=immediate`, see
[SQLite](integrations.md#sqlite)). The returned `UpdateResult` holds the
owners' changed postimages in `Changed` and, with `SkipUnchanged`, the targets
they left — which keep their stamps and counters — in `Unchanged`.

`UpdateBatchPlan.Chunks` holds each chunk's canonical `Keys`, its `Current`
statement and an optional `Owner` statement: what `UpdateBatch` runs, in that
order, for a log or a review of a batch before it runs.

`UpdateBatchLimits` defaults to 500 total rows and a parameter budget
of 999 for SQLite or 65535 for PostgreSQL. Positive overrides configure these
bounds; negative values refuse. Chunks contain batched statements, not one
UPDATE per item. A single item exceeding the parameter budget refuses the whole
plan. Empty input has no statements. Keys normalize by the one key rule
(`core.NormalizeKey`), each value as its column's type; duplicate normalized
keys refuse.
`UpdateBatch`'s exact check of the keys `Current` returns also rejects rows a
database collation matched under another spelling. All validation finishes
before any statement runs.

A batch takes the column patches the ordinary SQL compiler takes. It refuses
relation mutations and primary-key/tenant-column assignments. Typed JSON, arrays, nulls, change predicates, timestamps and increments
use the same canonical patch expression builder. `patch.PatchSQLResult.Assignments`
exposes those structured column expressions alongside its rendered SET
fragment; consumers never split SQL strings to recover them.

## Query costs and limits

Parser cost is a rejection heuristic, not a database execution-time estimate. The default `MaxCost` is 50; an indexed relation condition costs 8, an unindexed one 80. `indexed:"true"` describes an actual index you created; it does not create one or make a scan cheap. A filter on the resource's key costs as indexed without a tag, since a primary key is an index.

Set limits deliberately for your endpoint. [Configuration](schema-configuration.md) covers nesting, input size, filter condition/array sizes, and cumulative copied-source bytes. List limits default to 50 with a maximum of 1000; patch schemas default to 50 operations. Keep database deadlines and resource limits independently of parser limits.

## Index and query design

- Match indexes to frequently filtered columns and the stable sort plus unique tie-breaker used by keyset pagination. A keyset whose keys all walk one direction and none is nullable seeks with a row value on both dialects, `("price", "id") < (?1, ?2)`, which an index on the sort columns and then the key serves as a search from the cursor (on SQLite, `EXPLAIN QUERY PLAN` shows `SEARCH … USING INDEX`). SQLite seeks a row value only from a bare leading column, so a seek led by an expression — a decimal key's `parse_decimal_key("amount")` on SQLite, a `ColumnMapper`'s or computed column's expression, a path — also bounds that key on its own on both dialects, `parse_decimal_key("amount") >= parse_decimal_key(?1) AND (…) > (…)`, which an index on the expression and then the remaining keys serves (`CREATE INDEX … (parse_decimal_key(amount), id)`); PostgreSQL's index condition takes both. Mixed directions or a nullable key take an OR chain of the keys instead, which SQLite answers by scanning the index from its start. Text keys walk by code point: SQLite collates the cursor's value BINARY, so a text sort column's index serves the seek only when it is BINARY-collated (the default; a column declared `COLLATE NOCASE` needs `CREATE INDEX … ("name" COLLATE BINARY, "id")`). A key read through an expression (`ColumnMapper`, a computed `CursorKey.Expr`) is collated BINARY on the expression instead, which an index on that expression serves.
- Index junction owner/target columns and the tenant scope used by the emitted correlated queries.
- PostgreSQL JSON containment can use GIN indexes; choose an index/operator class for the predicates actually issued. Path `$eq` and `$in` sets of at most `parsepgx.ContainmentMaxValues` members (256 by default) on `jsonb` columns compile to containment outside `$not`, so `GIN (doc jsonb_path_ops)` serves them.
- A `$in` / `$nin` set, and a lookup's `WhereIn` list, binds as one parameter. On PostgreSQL a column's set compares with `= ANY($1)`: index columns that large sets filter, since under a generic plan an unindexed column probes the array member by member for each row. A path's set joins its members unnested, which a hash serves in any plan.
- SQLite JSON comparisons traverse document structure; measure representative document sizes and filter complexity.
- SQLite compares and orders decimals through `parse_decimal_key` ([`parsesql`](../parsesql/README.md#functions)), one Go call per row and expression: equality, `$ne`, `$in` and `$nin` compare the column's key with each operand's, as ranges, sorts and keyset seeks do. One expression index, the key and then the keyset's tiebreaker, serves them all: `CREATE INDEX prices_amount_key ON prices (parse_decimal_key(amount), id)`, if every connection that writes the table has the functions registered, since the index's maintenance calls them. Two keys are equal exactly where the canonical texts `parse_decimal` returns are, for every value a column holds (`TestDecimalKey_EqualExactlyWhereTextIsEqual`). A decimal path's key is `parse_decimal_key(doc -> '$.price')`. Facet terms key decimal and float values through `parse_decimal` (a `float32` field's through `parse_decimal_float32`), one call per row. An int or float path's member is a number only within 1000 digits in plain notation: a literal of at most 1000 characters with no exponent is, without a call, and `parse_decimal` reads any other, one call per such member.
- Facets and exact counts can scan a much larger population than the returned page. Request them only when needed.
- Includes cap targets per owner, but the caller still executes and assembles them. Avoid loading an unbounded related object graph.

Inspect real query plans and benchmark with production-like cardinalities. The package does not choose indexes, cache plans, pool connections, or promise a throughput figure.

## Verification

From the Parse module directory (`parse/` in a repository checkout):

```bash
go test -tags=unit -race ./...
go test -tags=integration -race ./...
go test -tags=unit -run '^$' -bench . ./...
```

`./...` stops at the [example server](../examples/server/), a module of its own: `make parse-example-check` from the repository root tests it against this working tree.

`go test` writes a CPU or memory profile for one package at a time. From the repository root, `tools/bench-parse.sh <dir>` runs each package's benchmarks on its own and writes `benchmarks.txt`, `cpu.pprof` and `allocs.pprof` under `<dir>/<package directory>/`, as CI does for main; `BENCH_COUNT` and `BENCH_TIME` set `-count` (default 3) and `-benchtime` (default 1s).

`go test -tags=concurrency -race ./...` is the quick race check: it builds only the tests that run parse code on several goroutines (files tagged `concurrency`). Parse starts no goroutines of its own, so the race detector can fail no other unit test.

SQLite cases run locally. PostgreSQL and PostGIS tests run against the servers `PARSE_POSTGRES_DSN` and `PARSE_POSTGIS_DSN` name, each a URL or keyword/value DSN as pgx reads it, and skip when the variable is unset; see [database test helpers](../internal/dbtest/). From a repository checkout, `make parse-postgres-up` starts one PostGIS server that serves both and prints the two exports. A named server that is unavailable fails the test within `PARSE_TEST_TIMEOUT` of the first fixture call: a Go duration, 30 seconds by default, that also bounds each PostgreSQL test of `parsepgx`; raise it on a contended host. Skipped backend tests are not a backend verification result: with `PARSE_REQUIRE_PG=1` an unset variable fails each test that asks for its server instead, which is how CI's PostgreSQL jobs run.

| Source | What it exercises |
|---|---|
| [Main example](../example_test.go) | Public typed parser/list/patch flow |
| [SQL examples](../example_guides_test.go) | Request-to-SQL examples across dialects |
| [pgx recipe](../parsepgx/recipe_test.go) | List and Merge Patch endpoints on `parsepgx`; `TestRecipe` serves them against PostgreSQL |
| [parsepgx suite](../parsepgx/parsepgx_integration_test.go) | Page walks with totals, facets and includes; tenancy; every pgx exec mode; snapshots; update rollback; guarded updates |
| [parsepgx cancellation](../parsepgx/cancel_integration_test.go) | Concurrent relation updates of one owner; lists and updates cancelled or past their deadline mid-statement |
| [Change predicate cases](../internal/corpus/patch.go) | `SkipUnchanged`'s comparisons per column kind, executed on SQLite and PostgreSQL |
| [HTTP server](../examples/server/) | SQLite requests, pagination, facets, and patching |
| [Filter conformance](../filter/filtertest/) | Shared operator/type behavior for backend implementers |
| [Database corpus](../internal/corpus/) | Every database-backed case, run by `parsesql`'s and `parsepgx`'s `TestCorpus` on their engine: executor contract, lists, keysets, facets, filters, keys, values, updates, policies, relations, tenancy, reserved identifiers, SQLite query plans, filter's builders on the database, patch SQL, string literals, PostGIS geo measures |
| [SDK fixtures](../testdata/) | Go-to-TypeScript schema/filter/page contracts |

For the SDK, run `make test-parse-sdk` from the repository root, which first checks the pinned pnpm, or run `pnpm install --frozen-lockfile`, `pnpm run check`, `pnpm run check:compat`, `pnpm run lint`, `pnpm test`, and `pnpm run build` from `sdk/ts`. Regenerate shared fixtures only when the contract change is intentional; the [SDK guide](../sdk/ts/README.md#testing) lists the commands.
