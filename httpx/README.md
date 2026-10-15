# httpx

HTTP standards and resilience patterns as composable `http.RoundTripper`
layers for Go's standard `http.Client`.

Keep your client, requests and responses. Add one transport or combine layers
around your existing transport; it keeps control of connections, proxies and
TLS. [Wrapping an existing client](docs/composition.md) also preserves its
cookie jar, redirect policy and timeout.

Requires **Go 1.26 or later**. APIs may change between minor releases while at `v0`.

```sh
go get github.com/ashtonian/pkg/httpx
```

## Common HTTP patterns

Choose the behaviors your client needs and enable their layers individually.

| Pattern | What httpx handles |
|---|---|
| [Retries and backoff](docs/resilience.md#retries) | Transient transport errors and HTTP 429/502/503/504; bounded exponential backoff, jitter and `Retry-After`. |
| [Request replay rules](docs/resilience.md#retries) | Idempotent methods, an idempotency key or explicit opt-in, plus a replayable body. Write safety depends on the server's contract; an opt-in does not deduplicate requests. |
| [Circuit breaking](docs/resilience.md#circuit-breakers) | Fail fast for a repeatedly failing origin, then probe for recovery; independent state per scheme/host/port. |
| [Slow-request hedging](docs/resilience.md#hedging) | Start an overlapping attempt after a delay and cancel losers. GET by default; the first HTTP response wins, including an error status. |
| [Rate and concurrency limits](docs/resilience.md#rate-versus-concurrency) | Per-origin rate/burst budgets and optional waiting; cap in-flight requests through the response body's lifetime. |
| [Timeouts and cancellation](docs/requests.md#timeouts-and-cancellation) | Preserve client/context deadlines through retry and admission waits; optional base transport with dial/TLS/header timeouts. |
| [Conditional and fresh reads](docs/caching.md) | ETag / Last-Modified revalidation, 304 reuse, `Cache-Control`, `Vary`, corrected age and bounded storage. |
| [Cache partitioning](docs/caching.md#identity-and-authority) | Keys include the effective URL, `Authorization` and `Cookie`; configure additional headers explicitly, including API-key headers. |
| [Stale fallback and background refresh](docs/caching.md#when-to-enable-it) | `stale-if-error` and `stale-while-revalidate` when permitted; keep useful cached reads available during upstream failures or refreshes. |
| [Compression negotiation](docs/codecs-compression.md) | Lazy gzip/deflate decoding, optional Brotli and configurable accepted encodings; preserve encoded partial responses. Go already handles basic gzip. |
| [Origin-bound credentials](docs/security-observability.md#origin-bound-credentials) | Add bearer, basic or API-key credentials only when the request URL and effective host match the provider's bound origin. |
| [Streaming and response bounds](docs/requests.md) | Ordinary `client.Do` bodies for streams/downloads; opt-in bounded reads and JSON/XML decoding, with optional MessagePack/protobuf. |
| [Operational visibility](docs/security-observability.md) | Redacted request/response logging, retry/cache events and metrics hooks. |

## Failure handling and failover

After retries are exhausted, callers receive the final HTTP response or Go
error. Check `StatusCode`: a 4xx/5xx response is not automatically a Go error.
Use `errors.Is` / `errors.As` for underlying errors and local refusals such as
`ErrCircuitOpen`, `ErrRateLimited` and `ErrLimiterTimeout`. Close response bodies.

**Failover to another host or region is application-owned.** Retries and
hedges repeat the same target through your transport; httpx does not select a
backup endpoint. Circuit breaking limits calls to a failing origin; stale-cache
fallback can keep permitted reads available. See [failure behavior and failover
boundaries](docs/resilience.md#failure-handling-and-failover).

## Quick Start

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/ashtonian/pkg/httpx"
)

func main() {
	if err := run(context.Background(), os.Getenv("API_URL"), os.Stdout); err != nil {
		slog.Error("request", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, url string, out io.Writer) (err error) {
	if url == "" {
		url = "https://httpbin.org/etag/httpx-demo"
	}
	// Layers are listed from innermost to outermost.
	transport, err := httpx.NewChain(
		httpx.WithCompressionT(),
		httpx.WithRetryT(httpx.WithMaxAttempts(3),
			httpx.WithBackoff(100*time.Millisecond, time.Second, 2)),
	).Build(http.DefaultTransport.(*http.Transport).Clone())
	if err != nil {
		return err
	}
	cache, err := httpx.NewCacheTransport(transport, nil) // Bounded private cache.
	if err != nil {
		return err
	}
	client := &http.Client{Transport: cache, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	defer func() { err = errors.Join(err, cache.Close()) }()

	for range 2 { // The server's cache headers decide: fresh hit or revalidation.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		body, readErr := httpx.ReadAtMost(resp.Body, 1<<20)
		if err := errors.Join(readErr, resp.Body.Close()); err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if _, err := fmt.Fprintf(out, "HTTP %d; %s\n%s\n", resp.StatusCode, resp.Header.Get("Cache-Status"), body); err != nil {
			return err
		}
	}
	return nil
}
```

Run `go run ./examples/quickstart` from this module. It fetches httpbin's ETag
endpoint twice; set `API_URL` to use another complete URL. Cache headers decide
whether the second GET is a fresh hit, a conditional revalidation or a new
fetch. The [integration tests](examples/quickstart/main_integration_test.go)
exercise retries, gzip, ETags and 304 reuse against a local HTTP server.

## Documentation

| Need | Read |
|---|---|
| Choose which behaviors to add and when | [Use cases](docs/use-cases.md) |
| Wrap an existing client, order layers, or use a custom transport | [Composition](docs/composition.md) |
| Use ETags, freshness, stale responses and cache partition keys | [Caching](docs/caching.md) |
| Negotiate compression, preserve ranges, or add a codec | [Compression and codecs](docs/codecs-compression.md) |
| Tune retry/backoff, circuit breakers and request budgets | [Resilience](docs/resilience.md) |
| Handle statuses, stream bodies, or use optional typed helpers | [Requests and responses](docs/requests.md) |
| Bind credentials, redact logs and export metrics | [Security and observability](docs/security-observability.md) |
| Look up every constructor, option, default and error | [Reference](docs/reference.md) |
| Run tests, benchmarks or release checks | [Development](docs/development.md) |

`NewClient` / `NewStandard` and typed `Get[T]` / `Do[T]` helpers are optional.
Optional modules: [Brotli](compress/brotli/README.md),
[MessagePack](codec/msgpack/README.md), [protobuf](codec/proto/README.md).
The [Gin example](examples/gin/README.md) demonstrates a complete API and client.

## License

[MIT](LICENSE).
