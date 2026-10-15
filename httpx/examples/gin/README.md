# Gin API example

Run a local Gin API and an `httpx` client together to see typed JSON requests, caching, compression, retries, and response ownership over real HTTP.

## Run

These commands use the parent version declared in this example's `go.mod`
and disable the repository workspace, which excludes example modules.
To test unreleased changes, run `make httpx-release-check` from the repository
root; it runs this server/client against the candidate module archives.

From this directory, start the server:

```sh
GOWORK=off go run ./server
```

In another terminal, run the client:

```sh
GOWORK=off go run ./client
```

The server listens on `127.0.0.1:8080`. Both programs use the same local demonstration token; set `HTTPX_EXAMPLE_TOKEN` in both shells to override it. The client exits with a nonzero status if a request fails or returns an unexpected status. Stop the server with Ctrl+C for graceful shutdown.

## What it demonstrates

| Use case | Code and behavior |
|---|---|
| Several response types | `Get[User]`, `Get[Device]`, and slice responses share one `*http.Client`. |
| Fresh cache hits | Repeated GETs use a bounded private HTTP cache and report `Cache-Status`. |
| Conditional revalidation | A request with `Cache-Control: no-cache` revalidates its ETag; the cache turns the server's bodyless 304 into the stored 200 representation. |
| Authenticated caching | The server sends `Cache-Control: private`; `httpx` partitions entries by the credentials actually sent. Do not mark user-specific responses `public`. |
| Compression | The server honors gzip quality values and varies by `Accept-Encoding`. Weak ETags identify the same representation across gzip and identity encoding. |
| CRUD and 204 | POST and PUT decode only after checking status; DELETE checks 204 and closes the raw response. |
| Structured errors | The client checks status separately from `err`, bounds error bodies, and decodes 401/404 responses into `APIError`. |
| Safe retries | The flaky GET fails every third attempt. Retry recovers it; POST is not automatically replayed without a server-supported idempotency contract. |
| Concurrent work | Twenty requests share a limiter, per-origin rate limit, and circuit breaker. All errors are collected and reported. |
| Hooks | Logging, cache, retry, and metrics hooks show the request path. Logging hooks receive redacted snapshots; other hooks receive live requests. |

The client closes every returned body, including a response returned together with a decode error, and closes idle connections on completion. The server stores copied snapshots so JSON rendering can run concurrently with updates without races. Collection ordering and ETags stay stable until the data changes.

## Configuration

| Program | Flag or environment variable | Default |
|---|---|---|
| Both | `HTTPX_EXAMPLE_TOKEN` | Local demonstration token |
| Server | `-addr` | `127.0.0.1:8080` |
| Server | `-timeout` | `30s` read/write deadline; headers capped at `5s` |
| Server | `-idle-timeout` | `1m` |
| Server | `-shutdown-timeout` | `5s` |
| Client | `-base-url` | `http://127.0.0.1:8080` |
| Client | `-deadline` | `2m` for the entire demonstration |

For a different port:

```sh
GOWORK=off go run ./server -addr 127.0.0.1:9090
GOWORK=off go run ./client -base-url http://127.0.0.1:9090
```

Run those commands in separate terminals. Client transport settings are grouped in `run` in [client/main.go](client/main.go), including a 30-second per-request timeout. Write bodies are capped at 1 MiB; error reads at 16 KiB.

This is a local teaching server with in-memory state, a shared demo token, and buffered JSON routes. For a deployed API, use HTTPS, your authentication and authorization system, durable storage, and middleware appropriate for streaming or upgraded connections. Cache invalidation for a resource URL does not automatically invalidate collection URLs; choose freshness or explicit invalidation policies for your API.

## Checks

```sh
GOWORK=off go test -tags=unit,integration -race ./...
```

The integration lane runs the real Gin handler, checks gzip negotiation and bodyless 204/304 responses, verifies cache/retry behavior, races concurrent reads and writes, and builds and runs the client against the API. It also verifies that invalid credentials make the client command fail.

See the [httpx README](../../README.md) for the library quick start and guides.
