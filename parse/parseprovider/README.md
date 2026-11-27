# parse/parseprovider

## Overview

Opens the parse provider an application's configuration names — SQLite ([`parsesql`](../parsesql/README.md)) or PostgreSQL ([`parsepgx`](../parsepgx/README.md)) — once, at startup. `Config` is the setting block, tagged for [`github.com/ashtonian/pkg/config`](../../config/README.md); `Open` checks every setting, opens the database with the connection limits the settings give, and returns a `Database`: the `*parse.Provider` to build resources on, and the database handle under it for the application's own SQL (its schema, its own statements, a transaction it shares with parse). The two providers are alternatives with one API: nothing here moves data between them, migrates from one to the other, or runs one beside the other.

```go
type Settings struct {
	DB parseprovider.Config `koanf:"db"`
	// …
}

settings, err := config.Load[Settings](ctx, config.WithEnvPrefix("APP"))
db, err := parseprovider.Open(ctx, settings.DB)
defer db.Close() // after the HTTP server has drained
devices, err := parse.NewResource[Device](db.Provider(), "devices", parse.TenantColumn("tenant_id"))
page, err := devices.List(ctx, req, scope)
```

With no setting given the defaults open an in-memory SQLite database; `APP_DB_PROVIDER=postgres APP_DB_DSN=postgres://…` runs the same application on PostgreSQL.

## Configuration

Keys are relative to where the application places `Config` (`db.` above). Every setting has a default, `dsn` included, so the block loads with nothing set.

| Key | Type | Default | Description |
|---|---|---|---|
| `provider` | string | `sqlite` | The database parse runs on: `sqlite` or `postgres`. |
| `dsn` | string (sensitive) | `:memory:` | The provider's connection string. sqlite: a file path that is absolute or starts `./` or `../` and ends `.db`, `.sqlite` or `.sqlite3`, a `file:` URI, or `:memory:`, with any driver parameters after `?` but `_txlock`, `_busy_timeout` and `_timeout`, which `Open` sets. postgres: a `postgres://` or `postgresql://` URL or a keyword/value string, without `pool_*` parameters, which the `postgres.*` settings set. Redacted in every config printout. |
| `settle_timeout` | duration | `10s` | Bound on each statement that settles an update's transaction — commit, rollback, savepoint release and rollback — which runs past the request's cancellation (`parse.DefaultSettleTimeout`). |
| `sqlite.max_open_conns` | int | `1` | Connections the SQLite database holds open, all kept idle and never closed for age. An in-memory database without `cache=shared` (`:memory:`, `file:…?mode=memory`) is a database per connection and must keep `1`; a file database in WAL mode reads concurrently on more. |
| `sqlite.busy_timeout` | duration | `5s` | How long a statement waits for another connection's write lock before failing with `SQLITE_BUSY` (`_busy_timeout`). `0` fails at once. |
| `postgres.collation` | string | `pg_c_utf8` | The code-point collation string sorts, keyset seeks and ranges compile under: `pg_c_utf8` (PostgreSQL 17+) or `C` (PostgreSQL 14 to 16). Declare text columns with the same collation so indexes serve them. |
| `postgres.containment_max_values` | int | `0` | The largest `$in` set a jsonb path compares by containment, which a GIN index serves: `0` is `filter.DefaultContainmentMaxValues` (256), a negative value compares none. |
| `postgres.max_conns` | int | `10` | Most connections the pool opens. |
| `postgres.min_conns` | int | `0` | Connections the pool keeps open while idle; at most `postgres.max_conns`. |
| `postgres.max_conn_lifetime` | duration | `1h` | Age at which the pool closes a connection once it is released. |
| `postgres.max_conn_idle_time` | duration | `30m` | Idle time after which the pool closes a connection. |
| `postgres.health_check_period` | duration | `1m` | How often the pool checks its idle connections and closes expired ones. |
| `postgres.connect_timeout` | duration | `5s` | Bound on opening one connection, TLS and authentication included. |

`Open` refuses, before connecting, a setting outside these ranges, naming its key: `parseprovider: sqlite.max_open_conns: 2: the DSN names an in-memory database without cache=shared, a database per connection; keep 1`.

## External Dependencies

- SQLite: `modernc.org/sqlite` (pure Go, no cgo), with parse's SQL functions registered on it (`parsesqlite.Register`), at least the release `parsesql.MinSQLiteVersion` names.
- PostgreSQL: a server, 17 or later for the default collation (14 to 16 with `postgres.collation: C`), reached through `github.com/jackc/pgx/v5/pgxpool`. PostGIS for geo filters.

## API

- `Config`, `SQLiteConfig`, `PostgresConfig`: the settings above.
- `Open(ctx, cfg) (*Database, error)`: checks `cfg`, opens the database and the provider under `ctx` (it connects: `parsesql.New` reads `sqlite_version()`, `parsepgx.New` checks the collation), and returns them. On an error nothing stays open.
- `Database.Provider() *parse.Provider`: build every resource on it (`parse.NewResource`).
- `Database.SQLite() (*sql.DB, bool)`, `Database.Postgres() (*pgxpool.Pool, bool)`: the handle of the provider the configuration named, for the application's own SQL; the other reports false. A transaction the application begins on it joins parse through `parsesql.Tx` or `parsepgx.Tx` and `Resource.In`, or begin one through `Provider().Begin`.
- `Database.Ping(ctx) error`: whether the database answers, for a readiness probe.
- `Database.Close() error`: closes the database once the work on it is done; a pool's `Close` waits for every connection in use to be released.

What `Open` does to the database:

- sqlite: registers parse's functions with modernc.org/sqlite (once per process), opens the DSN with `_txlock=immediate` — every transaction begins as a write transaction, so no writer commits between an update's read and its write — and `_busy_timeout` from `sqlite.busy_timeout`, and keeps `sqlite.max_open_conns` connections open with no lifetime: SQLite's connections are in-process handles with no server session or network path to refresh, and an in-memory database lives only while a connection to it is open.
- postgres: parses the DSN with pgx, sets the pool's limits from the `postgres.*` settings, and opens the pool; `parsepgx.New` then checks that the database compares strings under `postgres.collation`.

## Build and test

From the parse module directory:

```sh
go test -tags unit ./parseprovider/                    # SQLite, and every refusal
eval "$(make -s parse-postgres-up)"                    # a PostgreSQL server for the integration lane
go test -tags integration ./parseprovider/             # PostgreSQL too
```

`TestConfig_TagsMatchTheProviders` holds the tags to the providers (`settle_timeout` to `parse.DefaultSettleTimeout`, `provider`'s values to `parsesql.Name` and `parsepgx.Name`), and `TestOpen_DefaultsRunOnInMemorySQLite` opens and runs a resource on the defaults alone.

## Operations

- *Health*: `Database.Ping` for readiness. A provider has no background work of its own; the pgx pool's health check closes expired connections every `postgres.health_check_period`.
- *Metrics and logs*: none of its own. Instrument the pool (pgx tracers) or the driver, and label statement failures by `parse.StatementError.Statement`.
- *Shutdown*: close the `Database` after the HTTP server has drained, so no request is still using a connection; a pool's `Close` blocks until every connection is released.
- *Failure modes*:
  - `parseprovider: provider: "…": want sqlite or postgres`: the provider setting names another database. CockroachDB and MySQL are not providers.
  - `parseprovider: dsn: a postgres connection string for provider sqlite` (or the reverse): the DSN and the provider disagree; set both.
  - `parseprovider: dsn: parse: unsupported DSN: …`: a sqlite DSN of a form no SQLite file name takes unambiguously (a bare `app.db` is also a keyword/value or MySQL DSN's tail); write `./app.db`. A URL of another scheme names it.
  - `parseprovider: dsn: _txlock: Open sets it …`, `… pool_max_conns: the pool is set by the postgres.* settings`: remove the parameter from the DSN and use the setting.
  - `parseprovider: dsn: not a PostgreSQL connection string pgx reads`: a malformed URL or keyword/value string (a port that is no number, an unclosed quote). The error never quotes the DSN, which may carry a password; check it by hand.
  - `parseprovider: parsesql: SQLite … is older than 3.47.0`: see [parsesql's supported SQLite](../parsesql/README.md#supported-sqlite).
  - `parseprovider: parsepgx: collation pg_c_utf8: …; on PostgreSQL 14 to 16 open the provider with Collation(filter.CollationC)`: set `postgres.collation: C`, or upgrade to PostgreSQL 17. A database whose encoding is not UTF8 lacks `pg_c_utf8` on any release.
  - `parseprovider: parsepgx: checking collation …: failed to connect to …`: the server is unreachable within `postgres.connect_timeout`, or refused the credentials.

## Security

The DSN is `sensitive`: config printouts, diffs and usage tables redact it, and no error `Open` returns quotes it (pgx's own parse errors, which can, are replaced). Credentials can come from a secret store through config's `secret` tag on the application's field. `Open` adds no credentials or network access of its own; TLS is the DSN's (`sslmode`).

## Code Map

| File | Responsibility |
|------|----------------|
| [`parseprovider.go`](parseprovider.go) | `Config` and its blocks, `Open` and its checks, the SQLite DSN parameters and connection limits, the pgx pool configuration, `Database` |
| [`open_test.go`](open_test.go) | The defaults, the write-transaction mode, shared in-memory databases, every refusal |
| [`postgres_integration_test.go`](postgres_integration_test.go) | A pool on the test server with the configured limits, and a resource on it |
