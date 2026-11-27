# parse/examples/server

Serve a seeded product API from an in-memory SQLite database. The example exercises Resource lists over both transports (a GET query string, or the same request as a POST body for a filter too large for a URL), keyset pages, schema discovery, facets and Merge Patch of one product or a batch, through the same compiler as the PostgreSQL adapter. It serves every route the TypeScript SDK calls ([server contract](../../sdk/ts/README.md#server-contract)).

## Architecture

```mermaid
flowchart LR
    client["HTTP client"] -->|"Send list, schema or patch request"| server["examples/server [Service]<br/>Resource API + embedded SQLite"]
    server -->|"Return JSON or a native error envelope"| client
```

**Entry points:** `main` loads settings and runs the HTTP server; `newServer` wires the seeded products resource.

**Exit points:** Parameterized SQL goes to SQLite; JSON pages, schema documents, facets and native error envelopes go to the HTTP client.

**Failure modes:** Invalid settings fail startup; invalid requests return Parse E-codes; a patch of a missing product, or of a key its columns cannot hold, is 404 `E4004`, and a batch's missing keys, and those its column cannot hold, are its `failed` items; a request body the client did not finish sending is 400 `E4001`, logged at WARN; a request that outlives `--request-timeout` is 504 `E5004`, logged at WARN, and a request whose client left is not answered and is logged at DEBUG; any other SQL failure is 500 `E5001`, logged at ERROR; response failures are observed by the handler; an expired shutdown grace forces connections closed.

**Dependencies:** The example is a module of its own. It imports Parse, replaced by the working tree it sits in, the released `pkg/config` and Kong provider, Kong, and the pure-Go SQLite driver; Parse itself requires none of the CLI and configuration modules. Applications supply their own resource, storage, authenticated scope and authorization policy.

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

## Routes

| Route | SDK call | Served by |
|---|---|---|
| `GET /api/products` | `list` | `parsehttp.ParseList`, `BuildList`, `parsesql.QueryPage`, `parsehttp.WriteList`: the page, with the count and facets when asked, each product narrowed to the members `fields` names ([field mask](../../docs/queries.md#field-mask)) |
| `POST /api/products` | `list` over the body transport | The same handler |
| `OPTIONS /api/products?facets=…` | `facets` | `parsehttp.ParseFacets`, `BuildFacets`: `{"facets": {…}}`; a page parameter (`cursor`, `sort`, `page_size`, …) is 422 `E2067` |
| `PATCH /api/products/{id}` | `patch` | The pattern is the collection's path, a slash and `parsehttp.ItemPattern(products)`, a wildcard per key column; `parsehttp.ItemKey` reads the key, `parsehttp.ParsePatch` the patch, then `BuildUpdate(…, parse.ByKey(key), …)`, `parsesql.Update`: a `parsehttp.PatchAnswer`, `{"updated": [product]}`, or 404 `E4004` for a key that names no product, one its columns cannot hold included |
| `PATCH /api/products?id[]=…` | `patchMany` | `parsehttp.Query`, `Resource.ParseKey` per `id[]`, `parsehttp.ParsePatch`, `BuildUpdate(…, parse.ByKeys(…), …)`, `parsesql.Update` for 1 to 100 keys: `{"updated": [...], "failed": [{"id", "code": "E4004", "error"}]}`; no key or more than 100 is 400 `E4001` |
| `GET /api/{resource}/schema` | `schema` | `parsehttp.SchemaHandler`: the resource's document, or 404 `E2078` |
| `GET /api/schemas` | `schemas` | `parsehttp.SchemaListHandler`: `{"schemas": [...]}`, every registered resource's document |

The patch routes read no query parameter but a batch's `id[]`, and answer any other with 400 `E4001`: a parameter a patch ignored, a filter meant to narrow it, would let it write rows the client did not mean to.

The item route derives from the resource's key: products, keyed by `id`, are `/api/products/{id}`, and a resource keyed by several columns, `parse.PrimaryKey("warehouse_id", "sku")`, would be `/api/stock/{warehouse_id}/{sku}` from the same code. Each key value is a path segment escaped on its own, so the SKU `a/b` travels as `a%2Fb` and stays in its segment: `PATCH /api/stock/w1/a%2Fb`, as the TypeScript SDK's `patch(["w1", "a/b"], …)` sends it. `ItemKey` reads each segment as its column's text (`Resource.ParseKey`, an integer column's as decimal digits). A batch's `id[]` holds one value per row, so `patchMany` addresses only a key of one column.

`E4001`, `E4004`, `E5001` (internal error) and `E5004` (the request timeout expired) are the example's own codes: parse's catalog holds request errors, and whether a row exists or the server failed is the application's to say. Every other code is parse's ([HTTP contract](../../docs/http-errors.md)).

## Logging and shutdown

The process writes JSON logs to stderr. Its first entry, `starting`, is written at any level: the service, its version, the effective log level and the settings, with secrets redacted (the example has none). At the default level, `warn`, a healthy process writes nothing else. Each level below adds detail:

| Level | Adds |
|---|---|
| `info` | `listening` with the address bound (after binding it, so a port in use fails startup without it), one `request` entry per answered request (`method`, `route` template, `status`, `duration_seconds`, `request_id`), `shutting down` and `stopped` |
| `debug` | On each `request` entry: `request_bytes`, `response_bytes`, `client_addr` and the `query_params` names; a cancelled request's `request cancelled` |
| `trace` | On each `request` entry: the request and response bodies, each capped at 4 KiB with a `…_truncated` flag; the `request` entries of schema discovery (`GET /api/{resource}/schema`, `GET /api/schemas`), which clients poll |

Every entry logged while serving a request carries its `request_id`: the `X-Request-ID` header a proxy sent, when it is 1 to 128 letters, digits, `-`, `_` or `.`, or one the server mints; the answer carries it in `X-Request-ID` too. The example's bodies hold no credentials or personal data; a service whose bodies do redacts them before they reach the trace entry.

With `--config`, the settings file is watched, and SIGHUP reloads the settings too: a new `log_level` applies at once and logs `log level changed` at any level. Every other setting takes a restart: the reloader's `config reloaded` entry (at `info`) lists a change to one under `pending_restart`, and a reload that fails validation logs `config reload failed` at ERROR and keeps the settings it had. A flag or environment variable that sets `log_level` pins it, since those sources do not change.

SIGINT or SIGTERM stops new requests and drains active requests for the configured grace period; a second signal during the drain ends the process at once. Connections close if that deadline expires; the database then closes.

## Configuration

`pkg/config` owns defaults, the settings file, environment decoding, validation and reloads; Kong supplies explicit command-line overrides. Each source sits above the one before: defaults, the `--config` file, the environment, then the flags given. An omitted flag preserves the environment value. Environment variables use the `PARSE_SERVER_` prefix. All durations must be positive. The file's keys are the settings' names below the prefix, lowercased: `log_level: debug`.

| Flag | Environment | Default | Purpose |
|---|---|---|---|
| `--config` | none | none | Settings file, YAML (`.yaml`, `.yml`) or JSON (`.json`); it must exist when named. A change to it, or SIGHUP, reloads the settings, and `log_level` applies live |
| `--log-level` | `PARSE_SERVER_LOG_LEVEL` | `warn` | Minimum log level: `trace`, `debug`, `info`, `warn` or `error`; the `starting` and `log level changed` entries are written at any level |
| `--addr` | `PARSE_SERVER_ADDR` | `127.0.0.1:8080` | HTTP listen address |
| `--request-timeout` | `PARSE_SERVER_REQUEST_TIMEOUT` | `5s` | Deadline on each request's context, which its database work runs under; a request that outlives it is 504 `E5004` |
| `--shutdown-grace` | `PARSE_SERVER_SHUTDOWN_GRACE` | `10s` | Drain active requests before forced connection closure |
| `--read-header-timeout` | `PARSE_SERVER_READ_HEADER_TIMEOUT` | `5s` | Bound header reads |
| `--read-timeout` | `PARSE_SERVER_READ_TIMEOUT` | `10s` | Bound request reads |
| `--write-timeout` | `PARSE_SERVER_WRITE_TIMEOUT` | `10s` | Bound response writes |
| `--idle-timeout` | `PARSE_SERVER_IDLE_TIMEOUT` | `60s` | Bound idle keep-alive connections |

## Request flow

```mermaid
sequenceDiagram
    participant C as HTTP client
    participant H as examples/server
    participant R as parse.Resource
    participant DB as SQLite (embedded)
    C->>H: GET /api/products?… or POST /api/products with a JSON body
    H->>R: parsehttp.ParseList(request), BuildList(scope)
    R-->>H: ListPlan
    H->>DB: parsesql.QueryPage: page, count, facet and include statements
    DB-->>H: Rows, scanned through the resource's row contract
    H-->>C: JSON page
```

**Entry points:** An HTTP list request supplies its parameters in a GET's query string or a POST's `application/json` body; server-owned scope accompanies compilation.

**Exit points:** Database rows become a typed page and cursor metadata.

**Failure modes:** Query validation returns the native Parse error. A POST with list parameters in its query string, or a body that is not `application/json`, is E2067; a body over the resource's list body limit (80 KiB at the default limits) is E2063, read no further. Database execution and page construction errors reach the HTTP error boundary.

**Dependencies:** The request's context, carrying `--request-timeout`, flows to database execution; `parsesql` runs the plans (an update with relation changes or `SkipUnchanged` in one transaction, begun IMMEDIATE by the connection's `_txlock=immediate`), and the Resource owns grammar and page construction. A patch answers every target row it matched: those it wrote and those it left as they were.

## Security and operation

The default listener is loopback. The seeded demonstration exposes writes without authentication, tenant isolation or authorization; supply those application policies before exposing it to untrusted clients. Data is in memory and disappears when the process exits.

JSON logs report startup at any level, request and response failures at WARN and ERROR, and, at `info`, every request and the shutdown. To follow one request, filter the entries by its `request_id`, the `X-Request-ID` of its answer. `request deadline exceeded` (WARN) means a request's queries outlived `--request-timeout`: when ordinary requests log it, check the filter and sort in the entry's path against the indexed fields, then the timeout. `request cancelled` (DEBUG) is a client that left before its answer. `request failed` (ERROR) is a server fault. HTTP and database deadlines bound active work; this example has no dedicated health or metrics endpoint. Use the integration checks below to verify the seeded API.

## Verification

From this directory:

```bash
GOWORK=off go test -tags=unit ./...
GOWORK=off go test -tags=integration -race ./...
```

`make parse-example-check` from the repository root runs both, after `go mod tidy -diff`, `go vet` and staticcheck; CI runs it whenever Parse or the example changes.

The unit checks cover explicit flags over environment values, omitted-flag precedence, the settings file beneath both, rejection of an unbounded shutdown and of an unknown log level, a reload changing the log level live, the startup entry at any level, the TRACE level's name, the request entry at each level with its request ID, schema discovery at trace, the bound address logged after binding, a port in use failing before `listening`, the shutdown entry, and the request timeout on every request's context. The integration checks request every route of the Routes table. A list under a field mask, over GET and POST, answers only the masked members of each product, with the unmasked list's cursors, total and facets, a cursor that continues the walk, `[]` for no match and 422 `E2071` for a name that is no field. The integration checks include an expired deadline (504 `E5004`, no ERROR entry) and a cancelled request (nothing written, a DEBUG entry) on every route that reaches the database. Integration checks exercise the HTTP resource against SQLite. See [the module overview](../../README.md), [query guide](../../docs/queries.md) and [error contract](../../docs/http-errors.md).

## Key Files

| File | Responsibility |
|---|---|
| `go.mod` | The example's own module: Kong and the released config modules, Parse replaced by this working tree |
| `main.go` | Configuration and its reloads, the live log level, HTTP lifecycle and seeded SQLite storage |
| `log.go` | The JSON logger: the TRACE level, the request ID on every entry, entries written past the level |
| `request_log.go` | The request ID and the one entry per request, with its detail by level |
| `server.go` | Resource declaration, routes (the item route from the key, `parsehttp.ItemPattern`), list (GET and POST, written under its field mask by `parsehttp.WriteList`), facet, schema, single (key read by `parsehttp.ItemKey`) and batch patch handlers, the example's error codes |
| `settings_test.go` | Configuration source precedence, bounded shutdown, the log level, its live change and the startup entry |
| `server_test.go` | The request timeout on every request's context, the request log, listening and shutdown |
| `server_integration_test.go` | Requests against the SQLite HTTP server, one per row of the Routes table |
