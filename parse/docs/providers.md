# Choosing a provider

[Package overview](../README.md)

A provider is the database every resource built on it runs on, chosen once at startup. The providers are alternatives with one API for different runtime requirements: an application opens one, builds its resources on it, and serves every request from it. Nothing in parse layers one provider over another, runs two side by side for one resource, or moves data between them; an application that needs its data in another provider moves it itself. Each provider is a module of its own, released with parse at one version, so a binary carries only the drivers of the providers it links; [`parseprovider`](../parseprovider/README.md) opens the one a configuration names among those.

## Which provider serves which requirement

| | Memory | SQLite | PostgreSQL | CockroachDB |
|---|---|---|---|---|
| Module | [`parsememory`](../parsememory/README.md) | [`parsesql`](../parsesql/README.md) | [`parsepgx`](../parsepgx/README.md) | Planned (#394) |
| Opened by | `parsememory.Open(ctx, parsememory.Config)` | `parsesql.Open(ctx, parsesql.Config)`, or `parsesql.New` on a `*sql.DB` | `parsepgx.Open(ctx, parsepgx.Config)`, or `parsepgx.New` on a `*pgxpool.Pool` | `parsepgx.Open` refuses `cockroach://` and `crdb://` |
| Serves | A process whose data lives and dies with it: tests, embedded tools, WebAssembly, demonstrations | One process with its own database: in memory, or a file | A service on a database server, several replicas on one database | A distributed SQL cluster |
| Data outlives the process | No | With a file DSN | Yes | Yes |
| Schema | Declared by the resources from their types | The application's | The application's | The application's |
| Runs without a filesystem or driver | Yes: `GOOS=wasip1 GOARCH=wasm`, no directory preopened | No | No | No |
| Concurrency | Reads never wait; one writer at a time | One writer at a time (`_txlock=immediate`) | Row locks | Decided in #394 |
| Bound on its size | `max_rows` per table | The disk | The server | The cluster |
| Driver linked | None | `modernc.org/sqlite` (pure Go) | `github.com/jackc/pgx/v5` | Decided in #394 |

## Capabilities

Every provider runs the same requests through the same resource API, and parse's database corpus ([`internal/corpus`](../internal/corpus/)) holds each to the same assertions: `TestCorpus` in [`parsememory`](../parsememory/corpus_integration_test.go), [`parsesql`](../parsesql/corpus_integration_test.go) and [`parsepgx`](../parsepgx/corpus_integration_test.go). The memory provider runs every case that does not need SQL and skips each that does, naming why. An operation a provider does not support is refused before anything runs, with an `*parse.UnsupportedError` naming the provider and the operation (`errors.Is(err, parse.ErrUnsupported)`). Each refusal below links the test that holds it.

| Capability | Memory | SQLite | PostgreSQL |
|---|---|---|---|
| Lists: filters, sorts, keyset pages both ways, counts, terms and range facets, includes, field masks | Yes, evaluated in Go (`filter.Evaluate`) | Yes | Yes |
| Relation and membership filters, relation facets, governed lists by row tenant | Yes, resolved through lookups of the related tables | Yes | Yes |
| Server policies of field constraints | Yes | Yes | Yes |
| Server policies holding raw SQL (`filter.PolicyConstraint.RawSQL`) | Refused: [`TestCapabilities_RefuseWhatNeedsSQL`](../parsememory/parsememory_test.go) | Yes | Yes |
| Updates and batches: JSON Patch, Merge Patch, `SkipUnchanged`, stamps and counters, membership changes | Yes, applied to the stored values (`patch.ComposedPatch.Apply`) | Yes | Yes |
| `StampSQL` (a stamp the database's clock sets) | Refused: [`TestCapabilities_RefuseWhatNeedsSQL`](../parsememory/parsememory_test.go) | Yes | Yes |
| Full-text search (`search`) | Refused: [`TestCapabilities_RefuseWhatNeedsSQL`](../parsememory/parsememory_test.go) | Phrase search, ASCII case folding | Full-text search |
| Computed columns (`parse.ReadExpr`) | Refused: [`TestCapabilities_RefuseWhatNeedsSQL`](../parsememory/parsememory_test.go) | Yes, an expression its dialect reads ([`TestReadExpr_RefusedDialect`](../read_expr_test.go)) | Yes, likewise |
| SQL builders (`BuildList`, `BuildFacets`, `IncludeQueries`, `Page`, `PagePointers`, `ListQuery`, `BuildUpdate`, `BuildUpdateBatch`) | Refused: [`TestCapabilities_RefuseWhatNeedsSQL`](../parsememory/parsememory_test.go) | Yes ([`TestCapabilities_SQLProvidersSupportEvery`](../capability_test.go)) | Yes |
| `Resource.Query` (the application's own statement) | Refused: [`TestCapabilities_RefuseWhatNeedsSQL`](../parsememory/parsememory_test.go) | Yes | Yes |
| Geo filters on a JSONB point | Yes (`filter.Evaluate`) | Yes, through `parsesql`'s functions | Yes, with PostGIS |
| Native `geography` / `geometry` storage | Refused, E2115: [`TestEvaluateRefusals`](../filter/evaluate_test.go) | Refused, E2115: [`TestGeoSQL_SQLite_NativeStorage`](../filter/geo_test.go) | Yes, with PostGIS |
| The application's writes in parse's transaction | `parsememory.Tx` on a `Txn` | `parsesql.Tx` on a `*sql.Tx` | `parsepgx.Tx` on a `pgx.Tx` |
| `parse.ErrCommitUnknown` (a commit that went unanswered) | Never: the commit runs in the process | Never: SQLite's commit always answers | Possible ([`commit_test.go`](../parsepgx/commit_test.go)) |

CockroachDB is not a provider yet: `parsepgx.Open` refuses its URL schemes ([`TestOpen_RefusesNamingTheSetting`](../parsepgx/open_test.go)), and a `postgres://` URL to a CockroachDB cluster opens the PostgreSQL provider, whose collation check fails. Its provider is planned in #394. The full matrix of every capability against every provider, each with the test that holds it, is #384.

## Choosing

- The data must outlive the process, or several processes share it: PostgreSQL, or SQLite at a file path for one process.
- The process has no filesystem or may load no driver (a WebAssembly module under WASI, a sandbox), or every test should run without a database: memory. Its tables hold at most `max_rows` rows each, so size it from the largest table the process holds.
- Requests need full-text search, computed columns, the application's own SQL or SQL a handler composes (`ListQuery`, the builders): SQLite or PostgreSQL.
- One binary that runs on any of them, chosen by its configuration: link each provider's module and pass each `Choice` to `parseprovider.Open` (the [server example](../examples/server/README.md) links all three).

Where the providers differ in what a request returns — text order, timestamps, empty facets, isolation — the [memory provider's semantics](../parsememory/README.md#semantics), [backend semantics](queries.md#type-capabilities) and each provider's README say how, and why it is not a defect.
