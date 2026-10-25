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

## Concurrency and ownership

Construct parsers once and reuse them with immutable schemas/configuration. Direct filter/sort/patch schema accessors expose pointers; they do not return isolated copies. Registry `SchemaFor` discovery responses are independent metadata snapshots. Complete manual schema customization before serving concurrent requests. `ListQuery.WithFilterConfig` and `WithSearchSchema` configure a builder: do not mutate a shared builder while requests are using it.

Treat parsed filters, patch operations, policies, and user-supplied maps as request-owned values. Do not mutate them during compilation/application. Custom codecs, clocks, validators, and column mappers must be safe for the concurrency with which the caller uses them.

`ApplyValidatedPatch` does not mutate the input with the built-in codec. That does not make the result a general deep-clone API; unchanged reference-valued fields can remain shared. Synchronize access to shared application state separately.

For read/modify/write patches (`test`, `copy`, `move`, nested pointers), protect the read and subsequent persistence with a transaction/lock or optimistic version check. A successful test against a Go value does not establish that the database row is still unchanged. Relation replacements likewise require transaction-level serialization where concurrent updates can conflict.

## Query costs and limits

Parser cost is a rejection heuristic, not a database execution-time estimate. The default `MaxCost` is 50; an indexed relation condition costs 8, an unindexed one 80. `indexed:"true"` describes an actual index you created; it does not create one or make a scan cheap.

Set limits deliberately for your endpoint. [Configuration](schema-configuration.md) covers nesting, input size, filter condition/array sizes, and cumulative copied-source bytes. List limits default to 50 with a maximum of 1000; patch schemas default to 50 operations. Keep database deadlines and resource limits independently of parser limits.

## Index and query design

- Match indexes to frequently filtered columns and the stable sort plus unique tie-breaker used by keyset pagination.
- Index junction owner/target columns and the tenant scope used by the emitted correlated queries.
- PostgreSQL JSON containment can use GIN indexes; choose an index/operator class for the predicates actually issued.
- SQLite JSON comparisons traverse document structure; measure representative document sizes and filter complexity.
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

SQLite cases run locally. PostgreSQL/PostGIS tests require the corresponding DSN or opt-in container setup (`PARSE_POSTGRES_DSN`, `PARSE_POSTGIS_DSN`, or `PARSE_INTEG_PG=1`); see [database test helpers](../internal/dbtest/). An explicitly requested container that cannot start fails the test. Skipped backend tests are not a backend verification result.

| Source | What it exercises |
|---|---|
| [Main example](../example_test.go) | Public typed parser/list/patch flow |
| [SQL examples](../examples_sql_test.go) | Request-to-SQL examples across dialects |
| [pgx recipe](../parsepgx/recipe_test.go) | List and Merge Patch endpoints on `parsepgx`; `TestRecipe` serves them against PostgreSQL |
| [parsepgx suite](../parsepgx/parsepgx_integration_test.go) | Page walks with totals, facets and includes; tenancy; every pgx exec mode; snapshots; update rollback |
| [HTTP server](../examples/server/) | SQLite requests, pagination, facets, and patching |
| [Filter conformance](../filter/filtertest/) | Shared operator/type behavior for backend implementers |
| [Typed JSON parity](../typed_document_integration_test.go) | Stored SQLite document versus typed application |
| [SDK fixtures](../testdata/) | Go-to-TypeScript schema/filter/page contracts |

For the SDK, run `npm ci`, `npm run check`, `npm run lint`, `npm test`, and `npm run build` from `sdk/ts`. Regenerate shared fixtures only when the contract change is intentional; the [SDK guide](../sdk/ts/README.md#testing) lists the commands.
