# httpx

Composable outbound HTTP for Go, with typed responses and ordinary
`*http.Client` / `http.RoundTripper` interfaces.

- Retry safe requests with bounded backoff and `Retry-After` handling.
- Protect upstreams with per-origin circuit breakers, rate limits and an in-flight limit.
- Opt into an identity-partitioned HTTP cache or hedging for slow reads.
- Bind credentials to an origin; inspect requests through redacted logging hooks.
- Decode bounded JSON/XML responses; add MessagePack, protobuf or Brotli separately.

Requires **Go 1.26 or later**. APIs may change between minor releases while at `v0`.

```sh
go get github.com/ashtonian/pkg/httpx
```

Start with `NewStandard`: compression, safe retries and a 30-second timeout.
Use `NewClient` to choose each layer or `NewChain` to control their order.
See [when to use each feature](docs/use-cases.md).

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
	"strings"
	"time"

	"github.com/ashtonian/pkg/httpx"
)

type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func main() {
	if err := run(context.Background(), os.Getenv("API_URL"), os.Getenv("API_TOKEN"), os.Stdout); err != nil {
		slog.Error("fetch user", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, baseURL, token string, out io.Writer) (err error) {
	if baseURL == "" {
		baseURL = "https://api.example.com"
	}
	client, err := httpx.NewStandard(
		httpx.WithAuth(baseURL, httpx.NewBearerTokenProvider(token)),
		httpx.WithCircuitBreaker(),
		httpx.WithLimiter(16),
		httpx.WithTimeout(10*time.Second),
	)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()

	user, resp, err := httpx.Get[User](ctx, client,
		strings.TrimRight(baseURL, "/")+"/users/1",
		httpx.WithMaxResponseBytes(1<<20),
	)
	if resp != nil {
		defer func() { err = errors.Join(err, resp.Body.Close()) }()
	}
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get user: HTTP %d", resp.StatusCode)
	}
	_, err = fmt.Fprintf(out, "%s (HTTP %d)\n", user.Name, resp.StatusCode)
	return err
}
```

Set `API_URL` to an API serving `GET /users/1` as JSON and `API_TOKEN` to its
bearer token, then run `go run ./examples/quickstart` from this module.
The [program](examples/quickstart/main.go) is compiled verbatim, with
[integration tests](examples/quickstart/main_integration_test.go) for retries,
HTTP failures, invalid JSON, size limits and cancellation.

A nil error does **not** mean HTTP success: check the status. Close every
non-nil response body, including one returned with a decoding error.

## Documentation

| Need | Read |
|---|---|
| Choose a client, retry, cache, hedge or limit | [Use cases](docs/use-cases.md) |
| Make typed calls, stream a body, or handle errors | [Requests and responses](docs/requests.md) |
| Tune retries, breakers, concurrency and rate budgets | [Resilience](docs/resilience.md) |
| Cache safely across credentials and tenants | [Caching](docs/caching.md) |
| Understand layer ordering and custom transports | [Composition](docs/composition.md) |
| Bind credentials, redact logs and export metrics | [Security and observability](docs/security-observability.md) |
| Use codecs, compression or ranged downloads | [Codecs and compression](docs/codecs-compression.md) |
| Look up every option, default, interface and error | [Reference](docs/reference.md) |
| Run tests, benchmarks or release checks | [Development](docs/development.md) |

Optional modules: [MessagePack](codec/msgpack/README.md),
[protobuf](codec/proto/README.md), [Brotli](compress/brotli/README.md).
The [Gin example](examples/gin/README.md) demonstrates a complete API and client.

## License

[MIT](LICENSE).
