# httpx

HTTP caching, compression and retry/backoff as composable `http.RoundTripper`
layers for Go's standard `http.Client`.

Keep your client, requests and responses. Wrap its transport to add ETag /
Last-Modified revalidation, cache freshness, compression negotiation or safe
retries with `Retry-After`. Use each layer independently or combine them;
your base transport keeps control of connections, proxies and TLS.

Requires **Go 1.26 or later**. APIs may change between minor releases while at `v0`.

```sh
go get github.com/ashtonian/pkg/httpx
```

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

Already have a client? [Wrap its transport](docs/composition.md) while keeping
its cookie jar, redirect policy and timeout. Each layer also has a direct
constructor, such as `NewRetryTransport` or `NewCacheTransport`. Go's default
transport already handles basic gzip; httpx adds explicit compression policy,
deflate and optional Brotli.

## Documentation

| Need | Read |
|---|---|
| Choose which behaviors to add and when | [Use cases](docs/use-cases.md) |
| Wrap an existing client, order layers, or use a custom transport | [Composition](docs/composition.md) |
| Use ETags, freshness, stale responses and identity-safe caching | [Caching](docs/caching.md) |
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
