package httpx

import (
	"net/http"
	"time"
)

// NewStandard returns an *http.Client with compression, safe retries and
// a bounded timeout for ordinary outbound API calls. Pass
// additional [Option]s to override or extend the defaults for a
// specific call site (e.g. circuit breaker around a flaky upstream,
// hedging for a latency-sensitive endpoint).
//
// Default configuration:
//
//   - Base transport with HTTP/2 + tuned pool defaults
//     (MaxIdleConns 100 / MaxIdleConnsPerHost 10 / IdleConnTimeout 90s
//     / TLSHandshakeTimeout 10s / ResponseHeaderTimeout 30s)
//   - Compression (gzip and zlib deflate; optional registered encodings)
//   - Retry with exponential backoff (3 attempts; transient failures) for
//     idempotent requests and requests carrying Idempotency-Key only — a
//     POST or PATCH is sent once unless the caller opts in
//   - 30s overall request timeout
//
// No HTTP cache is wired by default. Callers that want one opt in with
// WithCache, which partitions stored responses by Authorization, Cookie and
// any WithCacheIdentityHeaders so a client shared across tenants never
// serves one identity's response to another.
//
// Callers that need cancellation pass a [context.Context] with a
// deadline; the 30s ceiling is the upper bound, not the floor.
//
// The injection seam: every type that holds an `*http.Client` should
// take it via a `WithHTTPClient(*http.Client)` option and default to
// `NewStandard()` so zero-config wiring still works.
//
// Codec note: httpx ships JSON + XML codecs natively. MsgPack is opt-in
// via the separate codec/msgpack module; protobuf uses codec/proto.
// Import only the formats your application needs.
//
// NewStandard returns an error wrapping [ErrInvalidConfig] when an override
// is invalid; the defaults alone always validate.
func NewStandard(opts ...Option) (*http.Client, error) {
	defaults := []Option{
		WithBaseTransport(WithHTTP2()),
		WithCompression(),
		WithRetry(WithMaxAttempts(3)),
		WithTimeout(30 * time.Second),
	}
	all := make([]Option, 0, len(defaults)+len(opts))
	all = append(all, defaults...)
	all = append(all, opts...)
	return NewClient(all...)
}

// MustNewStandard is [NewStandard] for statically known options: it panics
// when they are invalid, like regexp.MustCompile. Use it only for zero-config
// defaults built from literal options; call NewStandard and handle the error
// whenever an option value comes from runtime configuration.
func MustNewStandard(opts ...Option) *http.Client {
	c, err := NewStandard(opts...)
	if err != nil {
		panic(err)
	}
	return c
}
