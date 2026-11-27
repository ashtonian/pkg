# parse/parsepgx

parse's PostgreSQL provider, a module of its own: `github.com/ashtonian/pkg/parse/parsepgx`, which brings `github.com/jackc/pgx/v5` into the module graph of an application that requires it, and no other driver. `Open` opens a pgx pool from `Config`, with the limits its settings give, and returns the provider on it (`Database`). `New` opens a `*parse.Provider` over a `*pgxpool.Pool` the application opened itself. Every resource built on the provider (`parse.NewResource`) compiles for `filter.PostgresDialect` and runs its lists, facets, queries and updates on that pool, the statements of each step in one pgx batch. `Tx` wraps a transaction of the application's own for a resource to run in (`Resource.In`).

The provider's API is the one every parse provider has: [`parsesql`](../parsesql/README.md) is SQLite's, and [`parseprovider`](../parseprovider/README.md) opens whichever an application's configuration names among those its binary links (`Choice`). Providers are alternatives chosen at startup: parse never moves data between them or runs one beside another.

## Quick Start

```sh
go get github.com/ashtonian/pkg/parse@latest github.com/ashtonian/pkg/parse/parsepgx@latest
```

```go
db, err := parsepgx.Open(ctx, cfg) // cfg parsepgx.Config: its DSN, and the pool's limits
defer db.Close()                   // after the HTTP server has drained
devices, err := parse.NewResource[Device](db.Provider(), "devices", parse.TenantColumn("tenant_id"), parse.PageSize(25, 100))

func (a *deviceAPI) list(w http.ResponseWriter, r *http.Request) {
	tenant, err := a.tenant(r) // from the request's authentication
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	req, err := parsehttp.ParseList(r, a.devices)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page, err := a.devices.List(r.Context(), req, parse.Scope{Tenant: tenant.String()})
	if err != nil {
		a.fail(w, r, err) // parsehttp.WriteError for parse errors, 500 otherwise
		return
	}
	a.reply(w, r, page)
}
```

The complete recipe — resource declaration, list and Merge Patch endpoints, error handling — is [`recipe_test.go`](recipe_test.go), compiled in the unit lane and served against PostgreSQL by `TestRecipe`. On a pool the application opens itself, with its own tracer or `pgxpool.Config` hooks, hand it to `New`:

```go
pool, err := pgxpool.NewWithConfig(ctx, poolConfig) // the application's pool, its limits set
p, err := parsepgx.New(ctx, pool)                   // checks the server has pg_c_utf8
```

## Architecture

```mermaid
sequenceDiagram
    participant H as HTTP handler
    participant R as parse.Resource (on the provider)
    participant E as SQL executor (parse/internal/sqlexec)
    participant P as parsepgx provider
    participant PG as PostgreSQL (pgx pool)
    H->>R: ParseList(query), List(ctx, req, Scope)
    R->>E: the composed list, with its sink
    E->>P: Send: page, count, facets
    P->>PG: one batch
    PG-->>P: rows, total, facet rows (batch closed)
    P-->>R: rows scanned through the row contract, into the sink
    E->>R: the page's keys (sink)
    E->>P: Send: one include read per relation, for the page's keys
    P->>PG: one batch
    PG-->>P: (owner, target) pairs
    R-->>H: *filter.Page[T]: items, page_info, _relations, facets
```

**Entry points**
- `Open(ctx, cfg)` — a pool from `Config` and the provider on it; `Choice(cfg)` is the same for `parseprovider.Open`.
- `New(ctx, pool, opts...)` — the provider over a pool; `Collation`, `ContainmentMaxValues` and `SettleTimeout` configure it.
- `Tx(tx)` — the application's own `pgx.Tx` as a `*parse.Tx` for `Resource.In`.
- Every call on a resource built on the provider: `List`, `Facets`, `Query`, `Update`, `UpdateBatch`, and `Provider.Begin`.

**Exit points**
- Statements to the pool through `SendBatch`, and for an update through `Begin` (a transaction of its own) or `Exec` of a savepoint (in a transaction the resource runs in). Statement text and arguments come only from the resource's plans.
- Rows scan through the resource's row contract (`parse.RowScanner`): each column into the field the resource compiled it from — the column tag `core.WithColumnTag` names (default `db`), else the column convention over the field's JSON name, with embedded structs promoted as encoding/json promotes them and nil embedded pointers allocated. Columns omitted by `parse.ReadColumns` stay zero; a `parse.ReadExpr` column scans its expression's value under its column name; declared JSON storage and the columns PostgreSQL types json or jsonb (`parse.ScanJSON`, from the result's type OIDs) use the checked JSON decoder described below.

**Failure modes**
- `Open`: a setting out of its range, an empty DSN, one of another database (`cockroach://` and `crdb://` included: CockroachDB is not supported), one pgx does not read, and one that sets the pool's limits (`pool_*`) are refused before connecting, naming the setting and never quoting the DSN: `parsepgx: dsn: empty`, `parsepgx: max_conns: 0: must be at least 1`; then whatever `New` refuses, with the pool closed.
- `New`: a nil pool, a settle timeout that is not positive or a collation that is no `filter.PostgresCollation` is refused before any statement; a server without the collation is `parsepgx: collation pg_c_utf8: …; on PostgreSQL 14 to 16 open the provider with Collation(filter.CollationC)`, and an unreachable one `parsepgx: checking collation …: …`.
- A statement error: a `*parse.StatementError` naming the table and the statement (`page`, `count`, `facets`, `include <field>`, `query`, `current`, `conflict`, `lock`, `owner`, `relations`, `reread`, or the transaction's `begin`, `savepoint`, `release savepoint`, `rollback to savepoint`, `rollback`, `commit`): `parse: devices: owner: …`; `errors.As` reaches it and the pgx/PostgreSQL error under it (`*pgconn.PgError`). In pgx's default mode a batch prepares every statement first, so a bad column fails there.
- Invalid or lossy stored JSON: the decoding or key-validation error, inside the statement's `StatementError` with the column it names. No partial page or returned row slice is exposed, and an update whose returned row fails to scan is rolled back: an update's error means nothing it wrote is kept, unless it is `parse.ErrCommitUnknown`. A `json` or `text` column holding text that is not JSON also fails an update's change predicate (`SkipUnchanged`), which reads the column as `jsonb`, with `invalid input syntax for type json` (`22P02`), and so does a merge or member write into it; an update that writes the document whole without skipping unchanged rows replaces it.
- Any failure inside `Update` or `UpdateBatch`, a cancellation included: the transaction (or savepoint) rolls back everything the update wrote, and a transaction the resource runs in stays usable.
- A `COMMIT` sent and never answered — the connection lost, or the settle timeout run out, while it was in flight, or a `FATAL` that ended the session: `parse: devices: commit: commit outcome unknown: …`, which matches `parse.ErrCommitUnknown` (`errors.Is`), and the update may have been applied. Read the rows back before retrying a write that is not idempotent. A `COMMIT` PostgreSQL refused with an `ERROR` (a deferred constraint, a serialization failure), or one pgx never sent, rolled back: an ordinary error.
- A panic while the update runs — a type's own `UnmarshalJSON` as a row is read: the rest of the batch is read, the transaction (or savepoint) rolls back, and the panic goes on to the caller, leaving the connection idle and a transaction the resource runs in usable.
- A batch chunk whose targets are not exactly its keys: `parse.ErrUpdateBatchTargets`, before any owner statement; the transaction rolls back.
- Context cancelled or past its deadline: pgx's context error; nothing is retried. Once every statement of an update has run and its rows have scanned, the update commits (or releases its savepoint) whatever the context does since, and reports success: an update's error never hides an applied one, so a retry never applies it twice. Each settling statement — `COMMIT`, `ROLLBACK`, `RELEASE SAVEPOINT`, `ROLLBACK TO SAVEPOINT` — runs under the context's values without its cancellation, bounded by the settle timeout.

**Dependencies**
- *Imports*: `parse`, `parse/filter`, `parse/parseprovider`, `parse/internal/backend`, `parse/internal/sqlexec`, `parse/internal/dialect`, `github.com/jackc/pgx/v5`
- *Imported by*: applications serving parse resources on PostgreSQL, through `Open`, `Choice` or `New`
- *Module boundary*: the PostgreSQL provider, released with parse at one version ([versions](#versions)); parse's own module requires no driver

## Configuration

`Config` is what `Open` opens, loaded through [`github.com/ashtonian/pkg/config`](../../config/README.md) (its `koanf` names and defaults) or built in code. `Open` checks every setting whichever way it was built, and refuses one out of range naming it; its errors never quote the DSN.

| Setting | Type | Default | Description |
|---|---|---|---|
| `dsn` | string (sensitive) | none | A `postgres://` or `postgresql://` URL or a keyword/value string, without `pool_*` parameters, which the pool settings set. It has no default and no `validate` tag, so an application that links this provider beside another loads its settings without one while configuration chooses the other; `Open` refuses it empty. Redacted in every config printout |
| `settle_timeout` | duration | `10s` (`parse.DefaultSettleTimeout`) | `SettleTimeout`, below. Must be positive |
| `collation` | string | `pg_c_utf8` | `Collation`, below: `pg_c_utf8` (PostgreSQL 17+) or `C` (PostgreSQL 14 to 16) |
| `containment_max_values` | int | `0` | `ContainmentMaxValues`, below |
| `max_conns` | int | `10` | Most connections the pool opens; at least 1 |
| `min_conns` | int | `0` | Connections the pool keeps open while idle; 0 to `max_conns` |
| `max_conn_lifetime` | duration | `1h` | Age at which the pool closes a connection once it is released; positive |
| `max_conn_idle_time` | duration | `30m` | Idle time after which the pool closes a connection; positive |
| `health_check_period` | duration | `1m` | How often the pool checks idle connections and closes expired ones; positive |
| `connect_timeout` | duration | `5s` | Bound on opening one connection, TLS and authentication included; positive |

`New` takes these options:

| Option | Type | Default | Description |
|---|---|---|---|
| `Collation(c)` | `filter.PostgresCollation` | `filter.CollationPgCUTF8` | The code-point collation string sorts, keyset seeks and string ranges compile under: `pg_c_utf8` (PostgreSQL 17+) or `filter.CollationC` (`"C"`, PostgreSQL 14–16). `New` checks that the database compares strings under it. Index the sorted text columns with the same collation. |
| `ContainmentMaxValues(n)` | int | `0` (`filter.DefaultContainmentMaxValues`, 256) | The largest `$in` set a jsonb path compares by containment, which `GIN (doc jsonb_path_ops)` serves; a negative value compares none so. |
| `SettleTimeout(d)` | `time.Duration` | `10s` (`parse.DefaultSettleTimeout`) | Bounds each statement that settles an update (`COMMIT`, `ROLLBACK`, `RELEASE SAVEPOINT`, `ROLLBACK TO SAVEPOINT`), which runs past the caller's cancellation. A `COMMIT` still in flight when it runs out is `parse.ErrCommitUnknown`. Must be positive. |

With `New`, the pool, its limits and its connections' settings (`MaxConns`, `MaxConnLifetime`, `MaxConnIdleTime`, `ConnConfig.ConnectTimeout`, `DefaultQueryExecMode`) belong to the application's `pgxpool.Config`; `Open` sets the limits from `Config` and leaves the rest at pgx's defaults and the DSN's.

## Execution modes

The plans' arguments are typed (`[]string`, `[]uuid.UUID`, `float64`, …), so every pgx `QueryExecMode` runs them; `TestEveryExecMode` runs a list walk and an update in each.

| `ConnConfig.DefaultQueryExecMode` | Use |
|---|---|
| `QueryExecModeCacheStatement` (default) | Recommended: prepared once per connection, binary protocol |
| `QueryExecModeCacheDescribe` | Behind PgBouncer in transaction mode with statement caching off |
| `QueryExecModeDescribeExec` | No cache; one extra round trip per statement |
| `QueryExecModeExec`, `QueryExecModeSimpleProtocol` | Poolers or proxies that cannot prepare; text encoding |

Every update runs in a transaction, so a pooler must keep a transaction on one server connection: use a direct connection or a pooler in transaction (or session) mode. A pooler in statement mode, which may send each statement to a different server connection, cannot hold one, and `Update` and `UpdateBatch` are not supported behind it. Lists need no transaction and run behind any pooler, each statement in its own snapshot.

`pgx.NamedArgs` rewrites whole statements and cannot compose with the plans' positional placeholders; it is not used.

## JSON storage

Declared JSON columns and inferred PostgreSQL JSON/JSONB results decode as typed Go values: a stored JSON string becomes a Go string, and JSON-encoded bytes become the decoded byte slice. Native text, bytea and array columns retain pgx's normal codecs.

SQL NULL resets the destination field. A present JSON document decodes into a fresh candidate, with custom JSON decoders called once; canonical map keys and recognized struct-member names are checked before assignment. Lossy spellings such as `{"01":1}` into `map[int]int` are rejected. Custom JSON subtrees, interfaces and `json.RawMessage` retain their documented opaque representation.

A present JSON `null` cannot be represented by a nil map, slice, pointer or interface without losing its distinction from SQL NULL; use `json.RawMessage` to retain that document. Decoding and key-validation failures preserve the prior destination. Lists, empty-patch reads and update results use the same decoding boundary.

## Consistency

The page, count and facet statements run in one batch, the includes in a second once the page's keys are known. On a pool each statement sees its own snapshot, so a concurrent write can make the total disagree with the page by a row. Run the list in a `pgx.Tx` begun with `IsoLevel: pgx.RepeatableRead` (read-only is enough), through `devices.In(parsepgx.Tx(tx))`, when the total, facets and includes must describe exactly the page's rows.

`Update` returns a `parse.UpdateResult`: `Changed`, the target rows whose columns the update wrote, stamped and counted; `Unchanged`, the target rows it left — every row of a plan that assigns no column (an empty patch reads its targets, a relation-only patch locks them) and, under `SkipUnchanged`, the rows the patch already described; and `RelationRows`, the junction rows its membership changes deleted and inserted, zero when none changed. Emit a per-row event for `Changed` and a membership event when `RelationRows` is not zero. No row in either slice means not found.

Every update runs in a transaction of its own on a pool or connection — `BEGIN`, its batches, then `COMMIT` once every returned row has scanned, so an update of columns alone is three round trips — and under a savepoint (`parsepgx_update`) in a transaction the resource runs in (`In`), so that transaction can still roll it back. It changes the relation memberships of the rows the owner statement returned only (changed, then unchanged), never the requested keys. For a resource with [computed columns](../docs/queries.md#computed-columns) it then reads those rows again, in the batch that changes the memberships and after the changes, so a column over a relation's pairs returns them as the update left them rather than as the owner statement read them; the changed and the unchanged rows are read again apart, so each keeps its side of the split.

A [`SkipUnchanged`](../docs/patches.md#skipping-unchanged-rows) plan sends `plan.Current` and then the guarded `UPDATE` in one batch of the update's transaction: `Current` locks every target first, the update writes only the rows it changes, and the rows `Current` read and the update did not return are exactly `Unchanged`.

`UpdateBatch` runs in one transaction (a savepoint in a transaction the resource runs in): every chunk's `Current` in one batch, locking its targets in key order, each checked against its keys; then every `Owner` in a second batch. `Changed` are the rows the owners wrote, chunk by chunk; `Unchanged` the locked rows a `SkipUnchanged` owner left.

What runs, in what order, and what the result holds is the SQL executor's (`parse/internal/sqlexec`), shared with [`parsesql`](../parsesql/README.md); the resource composes each request and scans the rows it gets back. This package holds only what is pgx's: the batches the statements are pipelined in, the rows' JSON column types, and the transaction or savepoint and how each is settled.

## Versions

parse and its provider modules release together, at one version: require `parse` and `parse/parsepgx` at the same one, as `go get` of both `@latest` does, and as each release of this module requires its own release of parse. The provider runs on parse's internal contract, which no other module implements and which changes between releases, so a build that pairs this module with another release of parse fails to compile at this module's `contract.go`, naming this module's release and then parse's as `[major][minor][patch]` arrays:

```text
parsepgx/contract.go:21:84: cannot use [releaseMajor][releaseMinor][releasePatch]struct{}{} (value of type [0][18][0]struct{}) as [0][19][0]struct{} value in variable declaration
```

Here parsepgx 0.18.0 meets parse 0.19.0: move both to one release. Dependabot updates them as one group.

## Operations

- *Health*: `Database.Ping` for readiness; the pool's health check closes expired connections every `health_check_period`.
- *Shutdown*: close the `Database` (or the application's pool) after the HTTP server has drained; the pool's `Close` blocks until every connection in use is released.
- *Timeouts*: every statement of a list, and of an update up to its settle, takes the caller's context, as does `Begin`'s wait for a pool's connection; give the request a deadline. The settle runs past the caller's cancellation, each statement bounded by `SettleTimeout` (default `10s`). Pool limits and lifetimes are `Config`'s, or with `New` the application's `pgxpool.Config`'s.
- *Cancellation*: a call whose context is cancelled, or whose deadline passes, mid-statement returns an error that `errors.Is` matches to `context.Canceled` or `context.DeadlineExceeded`. Under pgx's default context handling the connection is closed and its statement cancelled on the server, so an update's transaction rolls back and writes none of its rows or memberships; the pool opens a new connection for the next call.
- *Concurrent updates*: updates of one owner's relations serialize on the owner's row lock, taken by the owner statement before any membership change, so two replacements of one owner's set leave exactly one of them. Every update of a target that can hold several rows (`ByIDs`, `ByKeys`, `ByFilter`) locks them in key order before it writes one: `Current` when the plan has one, else `Lock` ahead of a plain `UPDATE`, which would otherwise lock rows in the order its plan reads them; a relation-only patch's owner; and every batch's `Current`. Two updates over overlapping rows therefore cannot deadlock on each other, whatever order they name the keys in or their plans read the rows in. A `40P01` that remains involves another writer of those rows that locks in its own order; PostgreSQL aborts one, and it is safe to retry.
- *Observability*: the package emits no metrics or logs of its own. Instrument the pool with a pgx `QueryTracer` / `BatchTracer` (for example an OpenTelemetry tracer on `ConnConfig.Tracer`); each list is one or two batches, each update a transaction of one batch, or two; the batch of a `SkipUnchanged` plan, and of a plain update of a target that can hold several rows (`Lock`, then the `UPDATE`), holds two statements; a batch plan is two batches in a transaction.
- *Troubleshooting*: `parsepgx: dsn: a sqlite connection string` or `parsepgx: dsn: parse: unsupported DSN: scheme "cockroach": CockroachDB is not supported` names a DSN of another database; `parsepgx: dsn: not a PostgreSQL connection string pgx reads` is a malformed one (a port that is no number, an unclosed quote), which the error does not quote: check it by hand. `parsepgx: dsn: pool_max_conns: the pool is set by …`: remove the parameter and use the setting. `parsepgx: collation pg_c_utf8: …`: set `collation` to `C` on PostgreSQL 14 to 16, or upgrade to 17; a database whose encoding is not UTF8 lacks `pg_c_utf8` on any release. `parsepgx: checking collation …: failed to connect to …`: the server is unreachable within `connect_timeout`, or refused the credentials. A `parse.ErrCommitUnknown` means the `COMMIT` went unanswered: look for a dropped connection, a server restart or failover, or a commit slower than the settle timeout (synchronous replication waiting on a standby); the update may have been applied, so read before retrying. A `parse.ErrProviderMismatch` means a resource was given a transaction another provider began or of another kind (`In`), or related to a target built on another provider; build every resource of the database on its one provider. A `*parse.StatementError`'s `Table` and `Statement` say which statement failed; log them as attributes. A `parse.ErrUpdateBatchTargets` means a batch named a key that is missing, deleted, another tenant's, outside the policy or at another revision; nothing was written. A `55P03` or a lock wait on an update is another transaction holding the target rows (`Current`, `Lock` and relation-only owners lock them); keep those transactions short. `no such savepoint` / `25P01` on `parsepgx_update`: the transaction the resource runs in ended under the update (a `COMMIT` or `ROLLBACK` from another goroutine on the same transaction). A `42703` (undefined column) means the table and the columns `T`'s fields compile to (the column tag, else the column convention over the JSON name) disagree. A list whose total looks one off under load is the per-statement snapshot; use a REPEATABLE READ transaction. A `22021` (`invalid byte sequence for encoding "UTF8"`) or `22P05` (`unsupported Unicode escape sequence`) is text holding invalid UTF-8 or U+0000 that parse never read as request text: parse refuses both in request values, keys and patches (`core.CheckText`), so look at the values the server supplies, such as a policy constraint, a stamp or an `AcceptParams` parameter the handler binds itself.

## Security

`Config.DSN` is `sensitive`: config printouts, diffs and usage tables redact it, and no error `Open` returns quotes it (pgx's own parse errors, which can, are replaced). TLS is the DSN's (`sslmode`). The tenant, authorization policy and deleted-row visibility come only from the `parse.Scope` the server passes; the provider adds no predicates and takes none from the request. Includes read junction rows in the plan's tenant. SQL text comes from the plan, never from request strings, and every value is a bound argument.

## Build, run and test

From this module's directory (the repository's `go.work` builds it against the working tree's parse):

```bash
go test -tags=unit ./...                                                     # without a database
PARSE_POSTGRES_DSN=postgres://… go test -tags=integration -count=1 ./...     # TestCorpus and every pgx execution mode, against PostgreSQL
```

`make test-parse-unit` and `make test-parse-integration` run them with parse's other modules.

`make parse-postgres-up` starts the PostGIS server the integration suite uses ([verification](../docs/operations.md#verification)). Each PostgreSQL test's deadline is `PARSE_TEST_TIMEOUT`, a Go duration (default `30s`); raise it on a contended host.

## API Reference

- `Open(ctx, cfg Config) (*Database, error)` — checks `cfg`, opens a pool on `cfg.DSN` with its limits, and the provider on it (`New`) under `ctx`; a failure closes the pool.
- `Database` — `Provider()` (the provider to build resources on), `Pool()` (the `*pgxpool.Pool`, for the application's schema, its own statements and a transaction it shares with parse), `Ping(ctx)` (a readiness probe), `Close()`.
- `Choice(cfg Config) parseprovider.Choice` — this provider by its name, opened from `cfg`, for `parseprovider.Open` to choose among the providers a binary links.
- `New(ctx, pool *pgxpool.Pool, opts ...Option) (*parse.Provider, error)` — the PostgreSQL provider over pool, which stays the application's to close.
- `Tx(tx pgx.Tx) *parse.Tx` — the application's own transaction on the provider's pool, for `Resource.In`; commit or roll back tx itself, or through the returned `Tx`.
- `Option`: `Collation`, `ContainmentMaxValues`, `SettleTimeout` (the table above).
- `Name` — `"postgres"`, the provider's `parse.Provider.Name` and its name in configuration.

## Key Files

| File | Responsibility |
|------|----------------|
| `go.mod`, `contract.go` | The module, which requires parse at its own release, and the compile-time check that the parse it builds against is that release ([versions](#versions)) |
| `open.go` | `Config`, `Open` and its checks, the pool configuration, `Database`, `Choice` |
| `parsepgx.go` | `New` and its collation check, `Option`s, `Tx`; the database and transaction the SQL executor sends steps to (`internal/sqlexec`'s `Conn`, `DB` and `Tx`): batches read to their end even past a panic (`send`), the transaction-or-savepoint wrapper and its settle under `sqlexec.Settle`, which failed `COMMIT`s are `parse.ErrCommitUnknown` (`commitFailed`), and the json/jsonb columns a result reports (`rows.Documents`) |
| `corpus_integration_test.go` | `TestCorpus`: parse's database corpus (`internal/corpus`) on PostgreSQL, as `parsesql`'s runs it on SQLite |
| `new_test.go` | What `New` refuses before any statement |
| `open_test.go`, `open_integration_test.go` | `Config`'s defaults against the provider's, every refusal of `Open` naming its setting, `Choice`; a pool `Open` opens on the test server with the configured limits, running a resource |
| `internal/pgtest` | The test servers' fresh schemas (`PARSE_POSTGRES_DSN`, `PARSE_POSTGIS_DSN`, `PARSE_REQUIRE_PG`, `PARSE_TEST_TIMEOUT`), and the provider on one |
| `plans_integration_test.go` | The plans PostgreSQL gives the set renderings the builder chooses, custom and generic: a GIN index or the column's index serves them |
| `include_integration_test.go` | An include of a native UUID key reads it back as its canonical text |
| `commit_test.go` | Which failed `COMMIT`s are `parse.ErrCommitUnknown`, and the table they name |
| `json_integration_test.go` | Typed JSON lists/updates, nulls, projections and failure isolation in every execution mode |
| `recipe_test.go` | The README recipe: `Device`, list and Merge Patch handlers, `Example` |
| `recipe_integration_test.go` | `TestRecipe`: the recipe served against PostgreSQL |
| `parsepgx_integration_test.go` | Page walks, tenancy, every exec mode, snapshots, updates (guarded ones, on the pool and in a transaction, with relations and rereads), rollback, `Provider.Begin` and `Tx`, both collations |
| `cancel_integration_test.go` | Concurrent relation replaces of one owner; a list and an update cancelled mid-statement, and an update past its deadline in a policy subquery: the error, the rollback and the pool's next call |
| `civil_date_integration_test.go`, `column_type_integration_test.go`, `sql_text_values_integration_test.go` | Native date, network, interval and text values read back through the row contract and cursors |

## References

- [Package overview](../README.md) · [Integrations](../docs/integrations.md#postgresql) · [Transactions](../docs/integrations.md#transactions) · [Resource updates](../docs/patches.md#resource-updates)
- [pgx](https://pkg.go.dev/github.com/jackc/pgx/v5) — batches, exec modes, tracers
