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

Errors the root package returns read `parse: <table>: <operation or field>: <cause>`, naming the package and the table once; a sentinel (`ErrTenantRequired`, `ErrTenantInvalid`, `ErrKeyInvalid`, `ErrNoTarget`, `ErrUpdateScope`, `ErrForeign`, `ErrRegistered`, `ErrUpdateBatchTargets`) carries no prefix of its own, so branch on it with `errors.Is`. These are the server's mistakes, never the request's: none is a `core.ParseError`, and none should reach a client as a 4xx.

- `ErrTenantRequired`, `parse: devices: a tenant-scoped resource needs Scope.Tenant`: a list (without `Scope.Governance` either) or an update of a resource with a tenant column was built without the server's tenant. Check the middleware that sets `Scope.Tenant`.
- `ErrKeyInvalid`, for example `parse: devices: target key: not a value of the key column's type: expected string, got int`, or `update batch row 2 key: …` from `BuildUpdateBatch`: the key `ByID`, `ByIDs` or a batch row named is not of the key column's Go type, or not a UUID for a UUID key. Convert the route parameter to the key's type before building; nothing was sent to the database.

- `filter.ErrPolicyInvalid`, for example `filter: invalid policy constraint: field "owner_org" is not a field of devices`: a `Scope.Policy`, `Scope.Related` or `Scope.Governance` constraint does not compile against the schema. It is the server's policy, so the list fails closed and the handler answers 500; fix the policy builder for the field, operator or value it names.
- `filter.ErrRelatedPolicyRequired`, `relation type on device_types: filter: a policy-scoped relation is built without the target's policy`: the request used a `PolicyScoped()` relation and `Scope.Related` has no entry for the target table. Add the target's policy to the scope the handler builds.
- `filter.ErrRelationTenantRequired`, `relation junction board_labels (tenant column tenant_id): filter: a tenant-scoped pair table is joined without a tenant…`: a resource without a tenant column joined a pair table with one, and the scope named no tenant. Set `Scope.Tenant`, or `RelationScope.Unscoped` for platform tooling only.

### Tenant key errors

- `ErrTenantInvalid`, for example `parse: devices: Scope.Tenant is not a value of the tenant column's type: commands.tenant_id: 3000000000 overflows int32`: the server's tenant does not fit the named column, which the resource reaches through a relation (a bare column name is the resource's own). Check where the server reads the tenant. If the tenant is right, the named field is narrower than the tenant keys: widen it in Go and in the database. Nothing was sent to the database.
- A `Relate` error naming two tenant columns that "hold different types": one request would bind its tenant to a string or UUID column and an integer one. Give them one type (strings and UUIDs agree, as do integers of any width). The error names the resource that reaches both when it is not the one being related.
- A pair table's tenant column binds the tenant as the type of the typed tenant columns its resource reaches ([Tenant key](relations.md#tenant-key)). A driver error comparing it means the database column is of another type.

## Concurrency and ownership

Construct a resource once and share it. Its schemas are fixed when `NewResource` returns: customize them in `parse.CustomizeSchemas`, which runs before the read-set and column checks, so a customized field over a column `ReadColumns` withholds fails construction as a generated one does. `FilterSchema`, `SortSchema`, `SearchSchema`, `PatchSchema` and `ServerPatchSchema` return deep copies, as do the registry's `FilterSchema`, `PatchSchema` and `SortSchema`; changing a copy changes nothing the resource parses. `Relate` is the one change after construction; declare relations before serving. Registry `SchemaFor` discovery responses are independent metadata snapshots. `ListQuery.WithFilterConfig` and `WithSearchSchema` configure a builder: do not mutate a shared builder while requests are using it.

A resource compiles and applies only what it parsed itself. `ListRequest` and `filter.ParsedFilter` hold their fields behind methods and carry the schema that parsed them; `patch.ParsedPatch` carries its patch schema. `BuildList`, `BuildUpdate` (its patch and a `ByFilter` target), `BuildUpdateBatch` and `ApplyValidatedPatch` return `parse.ErrForeign` for a value another resource parsed, over the same table or not, and for one assembled by hand. A `WithPaths` view has its own filter schema, so a view and its base each refuse the other's list requests and filters; they share patch schemas, so each accepts the other's patches. Server code that makes a request without a transport parses it with the typed entry points: `ParseListInput`, `ParseFilterMap`, `MergePatchMap`, `ValidateOperations`. To compile a request whose memberships were resolved (`filter.ResolveMemberships`, which keeps the filter's schema), use `ListRequest.WithFilter`.

Treat parsed filters, patch operations, policies, and user-supplied maps as request-owned values. Do not mutate them during compilation/application. Custom codecs, clocks, validators, and column mappers must be safe for the concurrency with which the caller uses them.

`ApplyValidatedPatch` does not mutate the input with the built-in codec. That does not make the result a general deep-clone API; unchanged reference-valued fields can remain shared. Synchronize access to shared application state separately.

For read/modify/write patches (`test`, `copy`, `move`, nested pointers), protect the read and subsequent persistence with a transaction/lock or optimistic version check. A successful test against a Go value does not establish that the database row is still unchanged. Relation replacements likewise require transaction-level serialization where concurrent updates can conflict.

### Varied native update batches

`Resource.BuildUpdateBatch(dialect, []RowUpdate, scope, UpdateBatchLimits, options...)`
compiles distinct column patches for unique primary keys. Each `RowUpdate` has
`Key`, `Patch` and an optional narrowing `Policy` for its exact revision and
authorization predicate. The common scope's tenant, live-row and policy floors
apply to every item. The scope is checked before any item: a missing tenant
(`ErrTenantRequired`) or one the resource's tenant columns cannot hold
(`ErrTenantInvalid`) refuses the batch, empty or not, and the tenant is
normalized once for every item. This is a server-side compiler; it grants no
authority.

Apply the plan with `parsepgx.UpdateBatch(ctx, pool, r, plan)` on PostgreSQL
or `parsesql.UpdateBatch(ctx, db, r, plan)` on SQLite. Either runs it in one
transaction (a savepoint inside the caller's): every chunk's `Current` first,
each required to hold exactly its chunk's keys (`UpdateBatchChunk.CheckCurrent`),
then every `Owner` in chunk order. A target that is missing, stale,
inaccessible or invalid returns `ErrUpdateBatchTargets` and writes nothing.
PostgreSQL locks the targets in key order; on SQLite the connection begins
write transactions (`_txlock=immediate`, see
[database/sql](integrations.md#databasesql-and-sqlite)). The returned
`UpdateResult` holds the owners' changed postimages in `Changed` and, with
`SkipUnchanged`, the targets they left — which keep their stamps and counters —
in `Unchanged`.

`UpdateBatchPlan.Chunks` holds each chunk's canonical `Keys`, its `Current`
statement and an optional `Owner` statement, for an executor of your own: it
runs the statements in that order in one write transaction and rolls back on
the first error.

`UpdateBatchLimits` defaults to 500 total rows and a dialect parameter budget
of 999 for SQLite or 65535 for PostgreSQL. Positive overrides configure these
bounds; negative values refuse. Chunks contain batched statements, not one
UPDATE per item. A single item exceeding the parameter budget refuses the whole
plan. Empty input has no statements. Keys normalize through the resource's
canonical UUID/string/integer scalar type; duplicate normalized keys refuse.
Database collation aliases must also be rejected by the caller's exact Current
key check. All validation finishes before returning an executable plan.

A batch takes the column patches the ordinary SQL compiler takes. It refuses
relation mutations and primary-key/tenant-column assignments. Typed JSON, arrays, nulls, change predicates, timestamps and increments
use the same canonical patch expression builder. `patch.PatchSQLResult.Assignments`
exposes those structured column expressions alongside its rendered SET
fragment; consumers never split SQL strings to recover them.

## Query costs and limits

Parser cost is a rejection heuristic, not a database execution-time estimate. The default `MaxCost` is 50; an indexed relation condition costs 8, an unindexed one 80. `indexed:"true"` describes an actual index you created; it does not create one or make a scan cheap.

Set limits deliberately for your endpoint. [Configuration](schema-configuration.md) covers nesting, input size, filter condition/array sizes, and cumulative copied-source bytes. List limits default to 50 with a maximum of 1000; patch schemas default to 50 operations. Keep database deadlines and resource limits independently of parser limits.

## Index and query design

- Match indexes to frequently filtered columns and the stable sort plus unique tie-breaker used by keyset pagination.
- Index junction owner/target columns and the tenant scope used by the emitted correlated queries.
- PostgreSQL JSON containment can use GIN indexes; choose an index/operator class for the predicates actually issued. Path `$eq` and `$in` sets of at most `PostgresDialect.ContainmentMaxValues` members on `jsonb` columns compile to containment outside `$not`, so `GIN (doc jsonb_path_ops)` serves them.
- A `$in` / `$nin` set binds as one parameter. On PostgreSQL a column's set compares with `= ANY($1)`: index columns that large sets filter, since under a generic plan an unindexed column probes the array member by member for each row. A path's set joins its members unnested, which a hash serves in any plan.
- SQLite JSON comparisons traverse document structure; measure representative document sizes and filter complexity.
- SQLite compares decimals through `parse_decimal` and orders them through `parse_decimal_key` ([`parsesqlite`](../parsesqlite/README.md)), one Go call per row and expression. An expression index serves them, `parse_decimal(amount)` for equality and sets, `parse_decimal_key(amount)` for ranges, sorts and keyset seeks, if every connection that writes the table has the functions registered: the index's maintenance calls them. A float facet keys its values through `parse_decimal` (a `float32` field's through `parse_decimal_float32`), one call per row.
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

`go test -tags=concurrency -race ./...` is the quick race check: it builds only the tests that run parse code on several goroutines (files tagged `concurrency`). Parse starts no goroutines of its own, so the race detector can fail no other unit test.

SQLite cases run locally. PostgreSQL and PostGIS tests run against the servers `PARSE_POSTGRES_DSN` and `PARSE_POSTGIS_DSN` name, and skip when the variable is unset; see [database test helpers](../internal/dbtest/). From a repository checkout, `make parse-postgres-up` starts one PostGIS server that serves both and prints the two exports. A named server that is unavailable fails the test, within 30 seconds of the first fixture call. Skipped backend tests are not a backend verification result: with `PARSE_REQUIRE_PG=1` an unset variable fails each test that asks for its server instead, which is how CI's PostgreSQL jobs run.

| Source | What it exercises |
|---|---|
| [Main example](../example_test.go) | Public typed parser/list/patch flow |
| [SQL examples](../examples_sql_test.go) | Request-to-SQL examples across dialects |
| [pgx recipe](../parsepgx/recipe_test.go) | List and Merge Patch endpoints on `parsepgx`; `TestRecipe` serves them against PostgreSQL |
| [parsepgx suite](../parsepgx/parsepgx_integration_test.go) | Page walks with totals, facets and includes; tenancy; every pgx exec mode; snapshots; update rollback; guarded updates |
| [Change predicate cases](../patch/change_predicate_cases_test.go) | `SkipUnchanged`'s comparisons per column kind, executed on SQLite and PostgreSQL |
| [HTTP server](../examples/server/) | SQLite requests, pagination, facets, and patching |
| [Filter conformance](../filter/filtertest/) | Shared operator/type behavior for backend implementers |
| [Typed JSON parity](../typed_document_integration_test.go) | Stored SQLite document versus typed application |
| [SDK fixtures](../testdata/) | Go-to-TypeScript schema/filter/page contracts |

For the SDK, run `make test-parse-sdk` from the repository root, which first checks the pinned pnpm, or run `pnpm install --frozen-lockfile`, `pnpm run check`, `pnpm run check:compat`, `pnpm run lint`, `pnpm test`, and `pnpm run build` from `sdk/ts`. Regenerate shared fixtures only when the contract change is intentional; the [SDK guide](../sdk/ts/README.md#testing) lists the commands.
