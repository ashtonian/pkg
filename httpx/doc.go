// Package httpx composes outbound HTTP policies around [http.RoundTripper]
// and supplies typed request/response helpers that work with ordinary
// [http.Client] values.
//
// # Choosing a client
//
// [NewStandard] starts with a tuned HTTP/2-capable transport, gzip/deflate
// decoding, three safe retry attempts and a 30-second overall timeout.
// [NewClient] starts with the transport and timeout; every policy is opt-in.
// Neither constructor enables caching, credentials, hedging, circuit breaking,
// rate limiting or concurrency limiting unless requested.
//
// Reuse a client to retain its pools and per-origin state. Use [NewChain] or
// the individual transport constructors when an explicit layer order is
// necessary. The first Chain layer is the innermost.
//
// # Requests and responses
//
// [Get], [Post], [Put], [Patch] and [Do] decode bounded 2xx representations
// according to Content-Type. JSON and XML are built in; optional modules add
// MessagePack and protobuf. Non-2xx, HEAD, 204 and 205 return zero T with a
// response and nil error. Always check the HTTP status and close every non-nil
// response body, including one returned with a decode error.
//
// [WithMaxResponseBytes] defaults to 10 MiB after decompression. Larger bodies
// return [ErrResponseTooLarge], leaving the full response body available.
// [ReadAtMost] applies the same whole-value-or-error rule to an io.Reader.
// Use the client's Do method for streaming instead of a typed helper.
//
// [EncodeRequest] sets Body, GetBody and ContentLength from the same encoded
// bytes so retries and redirects replay the current payload.
//
// # Retry and admission
//
// Retry requires an idempotent method, an Idempotency-Key, or explicit
// [WithRetryNonIdempotent] permission, plus a replayable body. The application
// owns server-side idempotency. Unknown streams are recorded as sent, within
// a bound, rather than buffered before the transport can honor cancellation.
// Hedging is opt-in, defaults to GET, and requires GetBody for a non-empty body.
//
// NewClient request order, outermost to innermost, is logging, metrics, auth,
// cache, hedging, retry, rate limit, concurrency limiter, circuit breaker,
// compression, base transport. Each retry or hedge spends its own admission
// budget. Cache hits bypass origin admission; refreshes use the protected
// path. Auth runs before the cache captures identity. Local admission errors
// do not count as upstream circuit failures.
//
// # Caching and credentials
//
// [CacheTransport] is an opt-in private HTTP cache over a [CacheStore].
// [MemoryCache] bounds retained entries and bytes. Identity includes
// Authorization, Cookie, effective authority and configured identity headers.
// Configure every header that changes the caller's identity; separate stores
// for identity hidden in a transport, such as mTLS certificates.
//
// [WithAuth] binds a provider to a scheme, host and port. Providers do not
// add credentials on cross-origin redirects or to an unbound Host override.
// Destination allowlists and SSRF protection remain application policy.
//
// # Observability and lifetime
//
// [LoggingHook] receives detached, redacted snapshots; metrics/retry/cache
// hooks receive live objects and must not dump secrets. [CountingHook],
// [LatencyHook] and [MultiHook] provide bounded, concurrency-safe helpers.
// Round-trip metrics measure through response headers, not body consumption.
//
// Response-body EOF, read error or Close releases limiter slots and hedge
// winner contexts. Call [CacheTransport.Close] to stop background refreshes,
// then http.Client.CloseIdleConnections for pooled connections.
//
// # Optional modules
//
// Blank-import codec/msgpack, codec/proto or compress/brotli to register
// MessagePack, protobuf or Brotli. Named imports expose their codec/compressor
// implementations. Each optional dependency is a separate Go module; the
// core depends only on golang.org/x/time and github.com/sony/gobreaker/v2.
package httpx
