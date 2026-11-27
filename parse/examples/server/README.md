# parse/examples/server

Serve a seeded product API from the database its settings name: an in-memory SQLite database by default, or PostgreSQL. The example exercises Resource lists over both transports (a GET query string, or the same request as a POST body for a filter too large for a URL), keyset pages, schema discovery, facets and Merge Patch of one product or a batch. [`parseprovider`](../../parseprovider/README.md) opens the provider the `db` settings select, once at startup, and the products resource runs every statement on it; the handlers are the same on either. It serves every route the TypeScript SDK calls ([server contract](../../sdk/ts/README.md#server-contract)), and a readiness probe.

## Architecture

```mermaid
flowchart LR
    client["HTTP client"] -->|"Send list, schema or patch request"| server["examples/server [Service]<br/>Resource API on a parse provider"]
    server -->|"Return JSON or a native error envelope"| client
    server -->|"Parameterized SQL"| db[("SQLite (embedded) or PostgreSQL")]
```

**Entry points:** `main` loads settings and runs the HTTP server; `openDB` opens the configured database through `parseprovider.Open` and seeds it; `newServer` builds the products resource on its provider and wires the routes.

**Exit points:** Parameterized SQL goes to SQLite or PostgreSQL; JSON pages, schema documents, facets and native error envelopes go to the HTTP client.

**Failure modes:** Invalid settings fail startup, and so does a database setting `parseprovider.Open` refuses (`parseprovider: <setting>: …`, never quoting the DSN), a database that does not answer, a SQLite older than 3.47.0, or a PostgreSQL without the configured collation (`pg_c_utf8` before 17: set `db.postgres.collation` to `C`); invalid requests return Parse E-codes; a patch of a missing product, or of a key its columns cannot hold, is 404 `E4004`, and a batch's missing keys, and those its column cannot hold, are its `failed` items; a request body the client did not finish sending is 400 `E4001`, logged at WARN; a request that outlives `--request-timeout` is 504 `E5004`, logged at WARN, and a request whose client left is not answered and is logged at DEBUG; any other SQL failure is 500 `E5001`, logged at ERROR; a database that stops answering makes `GET /readyz` 503 `E5003`, logged at WARN; response failures are observed by the handler; an expired shutdown grace forces connections closed.

**Dependencies:** The example is a module of its own. It imports Parse, replaced by the working tree it sits in, the released `pkg/config` and Kong provider, Kong, pgx for the PostgreSQL seed, and, through `parseprovider`, the pure-Go SQLite driver and pgx's pool; Parse itself requires none of the CLI and configuration modules. Applications supply their own resource, storage, authenticated scope and authorization policy.

## Quick Start

From this directory (`parse/examples/server` in a repository checkout), a module outside the repository's Go workspace, so with `GOWORK=off`:

```bash
GOWORK=off go run . --addr 127.0.0.1:8080
curl 'http://127.0.0.1:8080/api/products?category=books&sort=-price&page_size=2'
curl -H 'Content-Type: application/json' \
  -d '{"filter":{"category":"books"},"sort":"-price","page_size":2}' \
  http://127.0.0.1:8080/api/products
```

The second request is the same list as a POST body ([HTTP contract](../../docs/http-errors.md#the-same-request-as-a-post-body)). `POST /api/products` lists, and the example has no create route, so nothing else claims it. A server that creates through `POST` on its collection path needs another path for POST lists.

On PostgreSQL 17 or later, the database selected by the environment (the `db` settings have no flags, so a DSN's password stays out of the process list and shell history):

```bash
docker run -d --name parse-example-pg -e POSTGRES_PASSWORD=example -p 127.0.0.1:5432:5432 postgres:18
PARSE_SERVER_DB_PROVIDER=postgres \
PARSE_SERVER_DB_DSN='postgres://postgres:example@127.0.0.1:5432/postgres?sslmode=disable' \
GOWORK=off go run . --addr 127.0.0.1:8080
```

Startup creates the `products` table when it is missing (on PostgreSQL with a GIN index on `labels`, the index its `indexed` tag declares) and inserts the ten demonstration products it does not hold, so a database that persists — PostgreSQL, or SQLite at a file path such as `PARSE_SERVER_DB_DSN=./products.db` — keeps the changes made to them across restarts. On PostgreSQL 14 to 16 add `PARSE_SERVER_DB_POSTGRES_COLLATION=C`.

## Routes

| Route | SDK call | Served by |
|---|---|---|
| `GET /api/products` | `list` | `parsehttp.ParseList`, `Resource.List`, `parsehttp.WriteList`: the page, with the count and facets when asked, each product narrowed to the members `fields` names ([field mask](../../docs/queries.md#field-mask)) |
| `POST /api/products` | `list` over the body transport | The same handler |
| `OPTIONS /api/products?facets=…` | `facets` | `parsehttp.ParseFacets`, `Resource.Facets`: `{"facets": {…}}`, `{}` when nothing matches; a page parameter (`cursor`, `sort`, `page_size`, …) is 422 `E2067` |
| `PATCH /api/products/{id}` | `patch` | The pattern is the collection's path, a slash and `parsehttp.ItemPattern(products)`, a wildcard per key column; `parsehttp.ItemKey` reads the key, `parsehttp.ParsePatch` the patch, then `Resource.Update(…, parse.ByKey(key), …)`: a `parsehttp.PatchAnswer`, `{"updated": [product]}`, or 404 `E4004` for a key that names no product, one its columns cannot hold included |
| `PATCH /api/products?id[]=…` | `patchMany` | `parsehttp.Query`, `Resource.ParseKey` per `id[]`, `parsehttp.ParsePatch`, `Resource.Update(…, parse.ByKeys(…), …)` for 1 to 100 keys: `{"updated": [...], "failed": [{"id", "code": "E4004", "error"}]}`; no key or more than 100 is 400 `E4001` |
| `GET /api/{resource}/schema` | `schema` | `parsehttp.SchemaHandler`: the resource's document, or 404 `E2078` |
| `GET /api/schemas` | `schemas` | `parsehttp.SchemaListHandler`: `{"schemas": [...]}`, every registered resource's document |
| `GET /readyz` | none: the readiness probe | `parseprovider.Database.Ping` under the request timeout: 200 `{"status": "ready"}`, or 503 `E5003` while the database does not answer |

The patch routes read no query parameter but a batch's `id[]`, and answer any other with 400 `E4001`: a parameter a patch ignored, a filter meant to narrow it, would let it write rows the client did not mean to.

The item route derives from the resource's key: products, keyed by `id`, are `/api/products/{id}`, and a resource keyed by several columns, `parse.PrimaryKey("warehouse_id", "sku")`, would be `/api/stock/{warehouse_id}/{sku}` from the same code. Each key value is a path segment escaped on its own, so the SKU `a/b` travels as `a%2Fb` and stays in its segment: `PATCH /api/stock/w1/a%2Fb`, as the TypeScript SDK's `patch(["w1", "a/b"], …)` sends it. `ItemKey` reads each segment as its column's text (`Resource.ParseKey`, an integer column's as decimal digits). A batch's `id[]` holds one value per row, so `patchMany` addresses only a key of one column.

`E4001`, `E4004`, `E5001` (internal error), `E5003` (the database does not answer) and `E5004` (the request timeout expired) are the example's own codes: parse's catalog holds request errors, and whether a row exists or the server failed is the application's to say. Every other code is parse's ([HTTP contract](../../docs/http-errors.md)).

## Logging and shutdown

The process writes JSON logs to stderr. Its first entry, `starting`, is written at any level: the service, its version, the effective log level and the settings, with secrets redacted (`db.dsn`, which may hold a password). At the default level, `warn`, a healthy process writes nothing else. Each level below adds detail:

| Level | Adds |
|---|---|
| `info` | `database opened` with the `provider`, `listening` with the address bound (after binding it, so a port in use fails startup without it), one `request` entry per answered request (`method`, `route` template, `status`, `duration_seconds`, `request_id`), `shutting down` and `stopped` |
| `debug` | On each `request` entry: `request_bytes`, `response_bytes`, `client_addr` and the `query_params` names; a cancelled request's `request cancelled` |
| `trace` | On each `request` entry: the request and response bodies, each capped at 4 KiB with a `…_truncated` flag; the `request` entries of schema discovery (`GET /api/{resource}/schema`, `GET /api/schemas`) and of the readiness probe (`GET /readyz`), which clients and orchestrators poll |

Every entry logged while serving a request carries its `request_id`: the `X-Request-ID` header a proxy sent, when it is 1 to 128 letters, digits, `-`, `_` or `.`, or one the server mints; the answer carries it in `X-Request-ID` too. The example's bodies hold no credentials or personal data; a service whose bodies do redacts them before they reach the trace entry.

With `--config`, the settings file is watched, and SIGHUP reloads the settings too: a new `log_level` applies at once and logs `log level changed` at any level. Every other setting takes a restart: the reloader's `config reloaded` entry (at `info`) lists a change to one under `pending_restart`, and a reload that fails validation logs `config reload failed` at ERROR and keeps the settings it had. A flag or environment variable that sets `log_level` pins it, since those sources do not change.

SIGINT or SIGTERM stops new requests and drains active requests for the configured grace period; a second signal during the drain ends the process at once. Connections close if that deadline expires; the database then closes (a PostgreSQL pool waits for its connections to be released).

## Configuration

`pkg/config` owns defaults, the settings file, environment decoding, validation and reloads; Kong supplies explicit command-line overrides. Each source sits above the one before: defaults, the `--config` file, the environment, then the flags given. An omitted flag preserves the environment value. Environment variables use the `PARSE_SERVER_` prefix. The file's keys are the settings' names below the prefix, lowercased, the `db` settings nested under `db`: `log_level: debug`, `db: {provider: postgres}`. With no settings at all the server listens on `127.0.0.1:8080` over an in-memory SQLite database.

| Flag | Environment | Type | Default | Purpose |
|---|---|---|---|---|
| `--config` | none | path | none | Settings file, YAML (`.yaml`, `.yml`) or JSON (`.json`); it must exist when named. A change to it, or SIGHUP, reloads the settings, and `log_level` applies live |
| `--log-level` | `PARSE_SERVER_LOG_LEVEL` | string | `warn` | Minimum log level: `trace`, `debug`, `info`, `warn` or `error`; the `starting` and `log level changed` entries are written at any level |
| `--addr` | `PARSE_SERVER_ADDR` | string | `127.0.0.1:8080` | HTTP listen address |
| `--request-timeout` | `PARSE_SERVER_REQUEST_TIMEOUT` | duration, positive | `5s` | Deadline on each request's context, which its database work runs under; a request that outlives it is 504 `E5004` |
| `--shutdown-grace` | `PARSE_SERVER_SHUTDOWN_GRACE` | duration, positive | `10s` | Drain active requests before forced connection closure |
| `--read-header-timeout` | `PARSE_SERVER_READ_HEADER_TIMEOUT` | duration, positive | `5s` | Bound header reads |
| `--read-timeout` | `PARSE_SERVER_READ_TIMEOUT` | duration, positive | `10s` | Bound request reads |
| `--write-timeout` | `PARSE_SERVER_WRITE_TIMEOUT` | duration, positive | `10s` | Bound response writes |
| `--idle-timeout` | `PARSE_SERVER_IDLE_TIMEOUT` | duration, positive | `60s` | Bound idle keep-alive connections |

The database settings are `parseprovider.Config` under `db`. They have no flags: set them in the environment or the settings file. `parseprovider.Open` checks them again at startup and names the setting it refuses. Each takes a restart.

| Setting | Environment | Type | Default | Purpose |
|---|---|---|---|---|
| `db.provider` | `PARSE_SERVER_DB_PROVIDER` | string | `sqlite` | The database parse runs on: `sqlite` or `postgres` |
| `db.dsn` | `PARSE_SERVER_DB_DSN` | string, secret | `:memory:` | For `sqlite` a file path (absolute, `./` or `../`, ending `.db`, `.sqlite` or `.sqlite3`), a `file:` URI or `:memory:`, without `_txlock` or `_busy_timeout`; for `postgres` a `postgres://` URL or a keyword/value string, without `pool_*` parameters. Redacted in the startup entry |
| `db.settle_timeout` | `PARSE_SERVER_DB_SETTLE_TIMEOUT` | duration, positive | `10s` | Bound on each statement that settles an update's transaction: commit, rollback, savepoint release |
| `db.sqlite.max_open_conns` | `PARSE_SERVER_DB_SQLITE_MAX_OPEN_CONNS` | int, at least 1 | `1` | Connections the SQLite database holds open, never recycled; an in-memory database without `cache=shared` needs 1 |
| `db.sqlite.busy_timeout` | `PARSE_SERVER_DB_SQLITE_BUSY_TIMEOUT` | duration, not negative | `5s` | How long a SQLite statement waits for another connection's write lock before `SQLITE_BUSY` |
| `db.postgres.collation` | `PARSE_SERVER_DB_POSTGRES_COLLATION` | string | `pg_c_utf8` | Code-point collation strings sort and compare under: `pg_c_utf8` (PostgreSQL 17+) or `C` (14 to 16) |
| `db.postgres.containment_max_values` | `PARSE_SERVER_DB_POSTGRES_CONTAINMENT_MAX_VALUES` | int | `0` | Largest `$in` set a jsonb path compares by containment; 0 is parse's default, negative compares none |
| `db.postgres.max_conns` | `PARSE_SERVER_DB_POSTGRES_MAX_CONNS` | int, at least 1 | `10` | Most connections the pool opens |
| `db.postgres.min_conns` | `PARSE_SERVER_DB_POSTGRES_MIN_CONNS` | int, 0 to `max_conns` | `0` | Connections the pool keeps open while idle |
| `db.postgres.max_conn_lifetime` | `PARSE_SERVER_DB_POSTGRES_MAX_CONN_LIFETIME` | duration, positive | `1h` | Age at which the pool closes a connection once it is released |
| `db.postgres.max_conn_idle_time` | `PARSE_SERVER_DB_POSTGRES_MAX_CONN_IDLE_TIME` | duration, positive | `30m` | Idle time after which the pool closes a connection |
| `db.postgres.health_check_period` | `PARSE_SERVER_DB_POSTGRES_HEALTH_CHECK_PERIOD` | duration, positive | `1m` | How often the pool checks idle connections |
| `db.postgres.connect_timeout` | `PARSE_SERVER_DB_POSTGRES_CONNECT_TIMEOUT` | duration, positive | `5s` | Bound on opening one connection, TLS and authentication included |

## Request flow

```mermaid
sequenceDiagram
    participant C as HTTP client
    participant H as examples/server
    participant R as parse.Resource
    participant DB as SQLite or PostgreSQL
    C->>H: GET /api/products?… or POST /api/products with a JSON body
    H->>R: parsehttp.ParseList(request)
    H->>R: List(ctx, request, scope)
    R->>DB: page, count and facet statements, then include statements
    DB-->>R: Rows, scanned through the resource's row contract
    R-->>H: Page
    H-->>C: JSON page
```

**Entry points:** An HTTP list request supplies its parameters in a GET's query string or a POST's `application/json` body; server-owned scope accompanies compilation.

**Exit points:** Database rows become a typed page and cursor metadata.

**Failure modes:** Query validation returns the native Parse error. A POST with list parameters in its query string, or a body that is not `application/json`, is E2067; a body over the resource's list body limit (80 KiB at the default limits) is E2063, read no further. Database execution and page construction errors reach the HTTP error boundary.

**Dependencies:** The request's context, carrying `--request-timeout`, flows to database execution; the Resource compiles the request for its provider's dialect, runs the statements on the provider's database (an update in one transaction, on SQLite begun IMMEDIATE, as `parseprovider` opens it), and owns grammar and page construction. A patch answers every target row it matched: those it wrote and those it left as they were.

## Security and operation

The default listener is loopback. The seeded demonstration exposes writes without authentication, tenant isolation or authorization; supply those application policies before exposing it to untrusted clients. With the default DSN the data is in memory and disappears when the process exits. The DSN is the one secret: set it in the environment or a settings file readable only by the service, never on the command line (it has no flag), and it is redacted from the startup entry; `parseprovider` errors name the setting without quoting it.

JSON logs report startup at any level, request and response failures at WARN and ERROR, and, at `info`, the database opened, every request and the shutdown. To follow one request, filter the entries by its `request_id`, the `X-Request-ID` of its answer. `request deadline exceeded` (WARN) means a request's queries outlived `--request-timeout`: when ordinary requests log it, check the filter and sort in the entry's path against the indexed fields, then the timeout; on PostgreSQL also check `db.postgres.max_conns` against the requests in flight, since a request waiting for a connection spends its deadline. `request cancelled` (DEBUG) is a client that left before its answer. `request failed` (ERROR) is a server fault: its `error` names the table and the statement (`parse: products: page: …`). `database not ready` (WARN) is `GET /readyz` failing its ping: check that the database is up and reachable at `db.dsn`, then the pool's connections. HTTP and database deadlines bound active work; the example has a readiness probe and no metrics endpoint.

## Verification

From this directory:

```bash
GOWORK=off go test -tags=unit ./...
GOWORK=off go test -tags=integration -race ./...
PARSE_POSTGRES_DSN='postgres://postgres:example@127.0.0.1:5432/postgres?sslmode=disable' \
  GOWORK=off go test -tags=integration -race ./...
```

`make parse-example-check` from the repository root runs both tags, after `go mod tidy -diff`, `go vet` and staticcheck; CI runs it whenever Parse or the example changes. The integration checks run every case on in-memory SQLite and, when `PARSE_POSTGRES_DSN` names a PostgreSQL 17+ server, again on PostgreSQL, each test on a schema of its own that it drops after; without the variable the PostgreSQL subtests skip, and `PARSE_REQUIRE_PG=1` fails them instead.

The unit checks cover explicit flags over environment values, omitted-flag precedence, the settings file beneath both, rejection of an unbounded shutdown and of an unknown log level, a reload changing the log level live, the startup entry at any level, the TRACE level's name, the request entry at each level with its request ID, schema discovery at trace, the bound address logged after binding, a port in use failing before `listening`, the shutdown entry, and the request timeout on every request's context. The unit checks also cover the database settings' defaults (in-memory SQLite), the environment selecting PostgreSQL, the refusal of an unknown provider, the database having no flags, and the DSN redacted from the startup entry. The integration checks request every route of the Routes table, on each database. `run` serves on the database the settings select (with none set, in-memory SQLite) and closes it after the drain; the seed keeps a persisted database's changes and holds each product once; the readiness probe follows the database's ping. A list under a field mask, over GET and POST, answers only the masked members of each product, with the unmasked list's cursors, total and facets, a cursor that continues the walk, `[]` for no match and 422 `E2071` for a name that is no field. The integration checks include an expired deadline (504 `E5004`, no ERROR entry) and a cancelled request (nothing written, a DEBUG entry) on every route that reaches the database. See [the module overview](../../README.md), [query guide](../../docs/queries.md) and [error contract](../../docs/http-errors.md).

## Key Files

| File | Responsibility |
|---|---|
| `go.mod` | The example's own module: Kong, the released config modules and pgx, Parse replaced by this working tree |
| `main.go` | Configuration and its reloads (the `db` settings are `parseprovider.Config`), the live log level, HTTP lifecycle, and the database: `openDB` opens it through `parseprovider.Open`, and the seed creates and fills the products table in each provider's SQL |
| `log.go` | The JSON logger: the TRACE level, the request ID on every entry, entries written past the level |
| `request_log.go` | The request ID and the one entry per request, with its detail by level |
| `server.go` | Resource declaration on the database's provider, routes (the item route from the key, `parsehttp.ItemPattern`), list (GET and POST, written under its field mask by `parsehttp.WriteList`), facet, schema, single (key read by `parsehttp.ItemKey`) and batch patch handlers, the readiness probe, the example's error codes |
| `settings_test.go` | Configuration source precedence, bounded shutdown, the log level, its live change, the database settings and the startup entry |
| `server_test.go` | The request timeout on every request's context, the request log, listening and shutdown |
| `server_integration_test.go` | Requests against the HTTP server, one per row of the Routes table, on each database |
| `backend_integration_test.go` | The databases the integration checks run on (in-memory SQLite; PostgreSQL on a fresh schema when `PARSE_POSTGRES_DSN` is set), `run` on each, the seed on a persisted database, the readiness probe |
