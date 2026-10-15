# Options and API reference

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

## Configuration

Constructors validate durations, counts, levels, thresholds and required providers, returning errors that wrap `ErrInvalidConfig`. Individual transport constructors reject nil bases. `Chain.Build(nil)` and `Chain.Client(nil, timeout)` instead construct the default base transport. Logging and metrics hooks may be nil to disable their output. `MustNewStandard` panics instead and is meant only for literal options.

### Client

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithTimeout(d)` | `time.Duration` | `DefaultTimeout` (30 s) | `http.Client.Timeout`; 0 disables it for long-poll / SSE |
| `WithBaseTransport(opts...)` | `...BaseOption` | `NewBaseTransport()` defaults | Tunes the base `*http.Transport`; the last call wins |
| `WithTransport(rt)` | `http.RoundTripper` | none | Replaces the base transport; `WithBaseTransport` then does not apply |
| `WithJar(jar)` | `http.CookieJar` | nil | Cookie jar of the client |
| `WithCompression(opts...)` | `...CompressionOption` | off | Adds `CompressionTransport` |
| `WithCache(store, opts...)` | `CacheStore`, `...CacheOption` | off | Adds `CacheTransport`; a nil store uses `NewMemoryCache()` |
| `WithCircuitBreaker(opts...)` | `...CircuitOption` | off | Adds `CircuitTransport` |
| `WithRetry(opts...)` | `...RetryOption` | off (`NewStandard`: 3 attempts) | Adds `RetryTransport` |
| `WithHedging(delay, opts...)` | `time.Duration`, `...HedgingOption` | off | Adds `HedgingTransport`; `delay > 0` |
| `WithLimiter(n, opts...)` | `int`, `...LimiterOption` | off | Adds `LimiterTransport`; `n >= 1` in-flight requests |
| `WithRateLimit(rps, burst, opts...)` | `float64`, `int`, `...RateLimitOption` | off | Adds `RateLimitTransport`; `rps > 0`, `burst >= 1` per origin |
| `WithAuth(originURL, provider)` | `string`, `AuthProvider` | none | Binds credentials to one origin; repeat for more origins |
| `WithMetrics(hook)` | `MetricsHook` | off | Adds `MetricsTransport` and feeds the retry and cache layers |
| `WithLogging(hook, opts...)` | `LoggingHook`, `...LoggingOption` | off | Adds `LoggingTransport` |

### Base transport

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithMaxIdleConns(n)` | `int` | 100 | Idle connections across all hosts |
| `WithMaxIdleConnsPerHost(n)` | `int` | 10 | Idle connections per host |
| `WithMaxConnsPerHost(n)` | `int` | 0 (unlimited) | Total connections per host |
| `WithIdleConnTimeout(d)` | `time.Duration` | 90 s | How long an idle connection stays open |
| `WithTLSHandshakeTimeout(d)` | `time.Duration` | 10 s | TLS handshake deadline |
| `WithResponseHeaderTimeout(d)` | `time.Duration` | 30 s | Wait for response headers after the request is written |
| `WithExpectContinueTimeout(d)` | `time.Duration` | 1 s | Wait for `100 Continue` |
| `WithDialer(d)` | `*net.Dialer` | 30 s dial timeout, 30 s keep-alive | Connection dialer; nil is rejected |
| `WithTLSConfig(cfg)` | `*tls.Config` | nil (Go defaults) | Client TLS settings |
| `WithHTTP2()` / `WithDisableHTTP2()` | — | HTTP/2 attempted | Enable or disable HTTP/2 |
| `WithDisableKeepAlives()` | — | keep-alives on | One request per connection |
| `WithWriteBufferSize(n)` / `WithReadBufferSize(n)` | `int` | 0 (4 KiB) | Connection buffer sizes |

The base transport honours `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` and leaves response decoding to `CompressionTransport`.

### Compression

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithEncodings(names...)` | `...string` | registered, in order `br`, `gzip`, `deflate` | Accepted encodings in priority order; an unregistered name is rejected |
| `WithGzipLevel(level)` | `int` | `gzip.DefaultCompression` | Level for `Compress`, within [-2, 9] |
| `WithDeflateLevel(level)` | `int` | `flate.DefaultCompression` | Level for `deflate` (zlib), within [-2, 9] |
| `WithBrotliLevel(level)` | `int` | brotli default | Within [0, 11]; requires the `compress/brotli` module |
| `WithCompressor(name, c)` | `string`, `Compressor` | none | Adds or overrides an encoding for this transport; nil is rejected |
| `WithCompressionPool(pool)` | `*BufferPool` | `DefaultBufferPool()` (32 KiB) | Buffers for `CompressionTransport.Compress` |

`deflate` is the zlib format (RFC 9110 §8.4.1.2); a body without a zlib header decodes as raw DEFLATE. Decoding starts on the first body read; HEAD, 1xx, 204, 205 and 304 responses pass through untouched. Range requests and partial responses preserve their encoded bytes and headers.

### Cache

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithCacheIdentityHeaders(names...)` | `...string` | none (`Authorization` and `Cookie` always partition) | Extra request headers that identify the caller, such as `X-Tenant-ID` |
| `WithStaleIfError(d)` | `time.Duration` | 0 (off) | Serve stale for up to `d` when revalidation fails with a network error or 5xx |
| `WithStaleWhileRevalidate(d)` | `time.Duration` | 0 (off) | Serve stale for up to `d` while revalidating in the background |
| `WithMaxCacheBodyBytes(n)` | `int64` | `DefaultMaxCacheBodyBytes` (1 MiB) | Largest stored body; larger responses stream through unstored |
| `WithMaxRetention(d)` | `time.Duration` | `DefaultMaxRetention` (24 h) | Longest an entry is kept, including entries kept only to revalidate |
| `WithRevalidation(timeout, n)` | `time.Duration`, `int` | `DefaultRevalidateTimeout` (30 s), `DefaultMaxConcurrentRevalidates` (8) | Timeout and concurrency of background revalidations; `Close` stops them |
| `WithCacheHook(h)` | `MetricsHook` | the `WithMetrics` hook, else none | Receives `OnCacheHit` / `OnCacheMiss` |
| `WithMaxEntries(n)` (`NewMemoryCache`) | `int` | `DefaultMemoryCacheMaxEntries` (1024) | Entry cap; least recently used evicted first |
| `WithMaxBytes(n)` (`NewMemoryCache`) | `int64` | `DefaultMemoryCacheMaxBytes` (64 MiB) | Byte cap over bodies, headers and keys |

The private cache implements the following RFC 9111 policy within each identity partition. It keys on the effective target — the URL with the request's `Host` as its authority, so virtual hosts behind one URL never share an entry (`Vary: Host` reads it too): `Vary` (any `*` in it forbids storing), corrected `Age` (saturating), request `no-store` / `no-cache` / `max-age` / `min-fresh` / `max-stale` / `only-if-cached`, response `no-store` / `no-cache` / `must-revalidate`, conditional revalidation with 304 header merge — and the merged header's storage policy reapplied, so a 304 carrying `no-store` or `Vary: *` answers its request and removes the entry — and RFC 5861 stale permissions from the origin or the options (never under `must-revalidate` / `no-cache`). Responses without explicit freshness are stored only with a validator and revalidated on every use. Range, HEAD, caller-conditional and body-bearing GET requests bypass it; a successful unsafe request invalidates its target and same-origin `Location` / `Content-Location`, and a fetch or revalidation in flight when it does never publishes what it read. A successful full validation that forbids storage or exceeds the body cap retires the previous representation. Identity, directive and request variation values include noncanonical header-map spellings.

### Retry

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithMaxAttempts(n)` | `int` | 3 | Attempts including the first |
| `WithBackoff(initial, max, mult)` | `time.Duration`, `time.Duration`, `float64` | 100 ms, 10 s, 2.0 | Exponential backoff; `max` also bounds `Retry-After` |
| `WithJitter(j)` | `float64` | 0.1 | Random fraction added to each wait, within [0, 1] |
| `WithRetryOn(codes...)` | `...int` | 429, 502, 503, 504 | Statuses that retry; replaces the default set |
| `WithRetryIf(fn)` | `func(*http.Response, error) bool` | the status set, plus connection reset / refused / aborted, broken pipe, EOF and network timeouts | Full retry predicate |
| `WithRetryNonIdempotent()` | — | off | Also replay POST / PATCH without `Idempotency-Key` |
| `WithMaxReplayBytes(n)` | `int64` | `DefaultMaxReplayBytes` (1 MiB) | Most of a body without `GetBody` recorded, as the first attempt sends it, for replay; a larger body is sent once |
| `WithRetryHook(h)` | `MetricsHook` | the `WithMetrics` hook, else none | Receives `OnRetry` (and `OnRetryCause`) |

A request is replayed only when that is safe: an idempotent method (GET, HEAD, OPTIONS, TRACE, PUT, DELETE), an `Idempotency-Key` header, or `WithRetryNonIdempotent`. Each attempt is a clone; the caller's request is never modified. A body with `GetBody` (`http.NewRequest` sets it for in-memory readers; `EncodeRequest` always does) is re-read from it; any other body is recorded as the first attempt streams it, never read ahead of the transport, and replayed only if that attempt read it to its end within `WithMaxReplayBytes` — a stalled stream is cancelled by the request's own deadline. Under `WithRateLimit` / `WithLimiter`, a retry beyond the budget fails with `ErrRateLimited` / `ErrLimiterTimeout` (or waits, under `WithWait`). A `Retry-After` longer than the max wait, or a wait past the request deadline, returns the response unretried. Caller cancellation and deadlines are not retried.

### Circuit breaker

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithFailureThreshold(n)` | `int` | `DefaultFailureThreshold` (5) | Consecutive failures that open an origin's circuit; 1 through `math.MaxUint32` |
| `WithSuccessThreshold(n)` | `int` | `DefaultSuccessThreshold` (2) | Probes a half-open circuit admits, and consecutive successes that close it; 1 through `math.MaxUint32` |
| `WithOpenTimeout(d)` | `time.Duration` | `DefaultOpenTimeout` (30 s) | Time open before admitting probes |
| `WithIsFailure(fn)` | `func(*http.Response, error) bool` | network error or 5xx | Classifies an outcome; caller cancellation is excluded first |
| `WithMaxBreakers(n)` | `int` | `DefaultMaxOrigins` (1024) | Origins that keep a breaker; the least recently used is dropped and restarts closed. A request the dropped breaker admitted still completes but reports no transitions, so the cap bounds origins retained, not requests in flight |
| `WithOnStateChange(fn)` | `func(Origin, from, to State)` | nil | Synchronous transition callback, also for `Reset` and dropping a non-closed breaker; must be quick and must not call back into the transport |

One `gobreaker` two-step breaker per origin (scheme, host, port): an outcome admitted under an earlier state is ignored, a caller deadline expiring counts as a failure, and `State(origin)` / `Reset(origin)` inspect and clear it.

### Hedging

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `delay` (argument) | `time.Duration` | — (required, > 0) | Wait before each further attempt |
| `WithMaxHedgedRequests(n)` | `int` | 2 | Concurrent attempts including the first, >= 2 |
| `WithHedgeIf(fn)` | `func(*http.Request) bool` | GET only | Which requests are hedged |
| `percentile` (adaptive argument) | `float64` | — (required, in (0, 1]) | Adaptive delay tracks this latency percentile over the last 1000 responses, starting at 100 ms |

The first response wins and keeps its context until its body is done; the other attempts are cancelled and their bodies drained and closed. A hedge runs beside the first attempt, so it needs its own copy of the body: a request body without `GetBody` is sent once, unhedged.

### Concurrency limiter

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `maxConcurrent` (argument) | `int` | — (required, >= 1) | In-flight requests across the client |
| `WithAcquireTimeout(d)` | `time.Duration` | 0 (wait for the request context) | Fail with `ErrLimiterTimeout` after `d` without a slot |
| `WithOnBlocked(fn)` | `func()` | nil | Called when a request has to wait |

A slot is held until the response body reaches EOF or is closed, so the cap bounds concurrent downloads — close bodies to free slots.

### Rate limit

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `rps`, `burst` (arguments) | `float64`, `int` | — (required, `rps > 0`, `burst >= 1`) | Token bucket per origin |
| `WithWait()` | — | off (fail with `ErrRateLimited`) | Wait for a token under the request context; a wait past the deadline fails at once |
| `WithMaxBuckets(n)` | `int` | `DefaultMaxOrigins` (1024) | Origins that keep a bucket; the least recently used is dropped and restarts full |

### Auth

| Provider | Sends |
|----------|-------|
| `NewBearerTokenProvider(token)` | `Authorization: Bearer <token>` |
| `NewRefreshableBearerTokenProvider(token, refresh)` | Bearer token; `Refresh()` / `SetToken()` rotate it |
| `NewBasicAuthProvider(user, password)` | `Authorization: Basic …` |
| `NewAPIKeyProvider(key)` / `NewAPIKeyProviderWithHeader(key, header)` | `X-API-Key: <key>` or a custom header |
| `NewQueryParamAuthProvider(name, value)` | A query parameter |
| `NewChainAuthProvider(providers...)` | Every provider in order |
| `FuncAuthProvider(fn)` | Whatever `fn` sets |

A provider runs only for requests to the origin it is bound to (scheme, host and port — RFC 6454), so redirects to another host, https→http downgrades and caller-supplied URLs never receive it. `ParseOrigin` rejects URLs with userinfo, non-HTTP schemes and relative URLs.

### Metrics and logging

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithMetrics(hook)` | `MetricsHook` | off | Request and response events; also the retry and cache events unless `WithRetryHook` / `WithCacheHook` set theirs |
| `NewLatencyHook(window)` | `int` | `DefaultLatencyWindow` (1024) suggested | Running count / sum / avg / min / max plus the last `window` samples (`Recent()`) |
| `NewMultiHook(hooks...)` | `...MetricsHook` | — | Fans every event out, including `OnRetryCause`; nil hooks are skipped |
| `WithLogging(hook, opts...)` | `LoggingHook` | off | Detached, allowlisted request / response snapshots — no body, redirect response or TLS state — with sensitive headers, credential query parameters, URL userinfo and fragments redacted, in the request URL and in `Referer` / `Location` / `Content-Location`; `LogResponse` receives the transport error with every URL in its text redacted (the caller receives the original). Other layers' hooks (`MetricsHook`, `RetryHook`, the cache hook) get the live request: do not log from them |
| `WithSensitiveHeaders(names...)` | `...string` | `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `X-Api-Key`, `X-Auth-Token`, `X-Access-Token`, `X-Refresh-Token`, `X-Csrf-Token`, `X-Amz-Security-Token` | Headers redacted to `Redacted`, in addition to the defaults |
| `WithSensitiveQueryParams(names...)` | `...string` | OAuth tokens and codes, API keys, passwords and secrets, `sig` / `signature`, AWS and GCS presigned-URL credentials | Query parameters redacted, in addition to the defaults; an unparseable query is redacted whole |

With the `Chain` API, pass `WithRetryHook` / `WithCacheHook` explicitly: only `NewClient` threads the client hook into the layers.

### Decoding helpers

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithMaxResponseBytes(n)` | `int64` | `DefaultMaxResponseBytes` (10 MiB) | Largest body `Get` / `Post` / `Put` / `Patch` / `Do` / `DecodeResponse` read; must be >= 1 |

Only 2xx representations are decoded; non-2xx responses, HEAD, 204 and 205 return zero `T`, the live response and a nil error. A larger body returns `ErrResponseTooLarge` and the response still reads the whole body (close it). After decoding, `resp.Body` holds the bytes read. When `T` is a pointer (`Do[*pb.Msg]`), the codec decodes into a newly allocated pointee. `Post` / `Put` / `Patch` encode a non-`io.Reader` body as JSON.

## Errors

httpx defines sentinels, not E-codes; match them with `errors.Is`.

| Sentinel | Returned by | Retryable |
|----------|-------------|-----------|
| `ErrInvalidConfig` | every constructor, the decoding helpers | no — fix the option |
| `ErrCircuitOpen` | `CircuitTransport` | after the open timeout |
| `ErrRateLimited` | `RateLimitTransport` | yes, after the bucket refills |
| `ErrLimiterTimeout` | `LimiterTransport` | yes |
| `ErrResponseTooLarge` | `Get`, `Post`, `Put`, `Patch`, `Do`, `DecodeResponse`, `ReadAtMost` | no — raise the limit or stream the body |
| `ErrNoCodec` | the decoding helpers | no — register a codec |

## API Reference

### Constructors

- `NewClient(opts...) (*http.Client, error)` — the fixed decorator stack
- `NewStandard(opts...) (*http.Client, error)` / `MustNewStandard(opts...)` — defaults plus overrides
- `NewChain(fns...) Chain`, `Chain.Build(base)`, `Chain.Client(base, timeout)` — explicit order, first innermost; presets `DefaultChain()`, `ResilientChain()`, `APIClientChain()`
- `NewBaseTransport(opts...) (*http.Transport, error)`
- `NewCompressionTransport`, `NewCacheTransport`, `NewCircuitTransport`, `NewRetryTransport`, `NewHedgingTransport`, `NewAdaptiveHedgingTransport`, `NewLimiterTransport`, `NewRateLimitTransport`, `NewAuthTransport`, `NewMetricsTransport`, `NewLoggingTransport` — one decorator each, `(base, ...) (*T, error)`
- `NewMemoryCache(opts...) (*MemoryCache, error)` — bounded `CacheStore`
- `NewLatencyHook(window) (*LatencyHook, error)`, `NewMultiHook(hooks...)`

### Helpers

- `Get[T]`, `Post[T]`, `Put[T]`, `Patch[T]`, `Do[T]` — request and decode; `Delete` — request only
- `DecodeResponse(resp, codec, v, opts...) error`, `EncodeRequest(req, codec, v) error` — sets `Body`, `GetBody` and `ContentLength` together and closes the body it replaces
- `ParseOrigin(url) (Origin, error)`, `OriginOf(*url.URL) Origin`
- `TransportStack(client) []http.RoundTripper` — the layers, outer to inner
- `RegisterCodec`, `GetCodec`, `HasCodec`, `RegisteredCodecs`; `RegisterCompressor`, `GetCompressor`, `HasCompressor`, `RegisteredEncodings`
- `NewBufferPool(size) (*BufferPool, error)`, `DefaultBufferPool() *BufferPool`

### Interfaces

| Interface | Methods | Implement to |
|-----------|---------|--------------|
| `CacheStore` | `Get`, `Set(key, *CachedResponse, ttl)`, `Delete` | Back the cache with Redis or another shared store; stored values are immutable |
| `Compressor` | `Compress`, `Decompress` | Add an encoding (zstd, …) |
| `Codec` | `ContentType`, `Encode`, `Decode` | Add a content type |
| `AuthProvider` | `AddAuth(*http.Request) error` | Add a credential scheme |
| `MetricsHook` | `OnRequest`, `OnResponse`, `OnRetry`, `OnCacheHit`, `OnCacheMiss` | Export metrics |
| `RetryCauseHook` | `OnRetryCause(req, attempt, cause, err)` | Receive retry causes |
| `LoggingHook` | `LogRequest(req)`, `LogResponse(req, resp, duration, err)` | Log redacted snapshots; `StructuredLoggingHook` adapts to a `func(LogEntry)` |
