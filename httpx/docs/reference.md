# Options and API reference

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

## Transport constructors

All decorators accept a base `http.RoundTripper`, return a transport plus an
error, and work as `http.Client.Transport`. They do not require a new request
or response type. Use them individually or combine their `With*T`
functions (or a custom `TransportFunc`) with `NewChain(...).Build(base)`; the first layer is innermost.

| Constructor | Behavior | Options |
|---|---|---|
| `NewCacheTransport(base, store, opts...)` | HTTP freshness and conditional revalidation; nil store uses bounded memory | `CacheOption` |
| `NewCompressionTransport(base, opts...)` | Content-encoding negotiation and lazy decoding | `CompressionOption` |
| `NewRetryTransport(base, opts...)` | Safe replay with backoff and `Retry-After` | `RetryOption` |
| `NewCircuitTransport(base, opts...)` | Per-origin circuit breaker | `CircuitOption` |
| `NewRateLimitTransport(base, rps, burst, opts...)` | Per-origin attempt rate | `RateLimitOption` |
| `NewLimiterTransport(base, n, opts...)` | In-flight requests through response-body lifetime | `LimiterOption` |
| `NewHedgingTransport(base, delay, opts...)` | Overlapping attempts for slow requests | `HedgingOption` |
| `NewAdaptiveHedgingTransport(base, percentile, opts...)` | Hedge delay from observed latency | `HedgingOption` |
| `NewAuthTransport(base, bindings...)` | Origin-bound credentials | `AuthBinding` values |
| `NewLoggingTransport(base, hook, opts...)` | Redacted request/response snapshots | `LoggingOption` |
| `NewMetricsTransport(base, hook)` | Round-trip metrics | `MetricsHook` |

`NewBaseTransport(opts...)` creates a tuned `*http.Transport`; pass your own
base to retain its connection settings. `NewClient(opts...)` and
`NewStandard(opts...)` optionally build a new `*http.Client` with fixed layer
order. Their `WithCache`, `WithRetry` and other options delegate to the same
transports. See [composition](composition.md) for ordering and shutdown.

## Configuration

Constructors validate durations, counts, levels, thresholds and required providers, returning errors that wrap `ErrInvalidConfig`. Individual transport constructors reject nil bases. `Chain.Build(nil)` and `Chain.Client(nil, timeout)` instead construct the default base transport. Logging and metrics hooks may be nil to disable their output. `MustNewStandard` panics instead and is meant only for literal options.

### Client

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithTimeout(d)` | `time.Duration` | `DefaultTimeout` (30 s) | `http.Client.Timeout`; 0 disables it for long-poll / SSE |
| `WithBaseTransport(opts...)` | `...BaseOption` | `NewBaseTransport()` defaults | Tunes the base `*http.Transport`; the last call wins |
| `WithTransport(rt)` | `http.RoundTripper` | none | Replaces the base transport; `WithBaseTransport` then does not apply |
| `WithJar(jar)` | `http.CookieJar` | nil | Cookie jar of the client |
| `WithCompression(opts...)` | `...CompressionOption` | off (`NewStandard`: on) | Adds response compression negotiation and decoding |
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
| `WithEncodings(names...)` | `...string` | registered members of `br`, `gzip`, `deflate`, in that order | Advertised encoding list, without quality weights; an unavailable name is rejected |
| `WithGzipLevel(level)` | `int` | `gzip.DefaultCompression` | Level for `Compress`, within [-2, 9] |
| `WithDeflateLevel(level)` | `int` | `flate.DefaultCompression` | Level for `deflate` (zlib), within [-2, 9] |
| `WithBrotliLevel(level)` | `int` | brotli default | Within [0, 11]; requires the `compress/brotli` module |
| `WithCompressor(name, c)` | `string`, `Compressor` | none | Adds or overrides an encoding for this transport; nil is rejected |
| `WithCompressionPool(pool)` | `*BufferPool` | `DefaultBufferPool()` (32 KiB) | Buffers for `CompressionTransport.Compress` |

`deflate` is the zlib format (RFC 9110 §8.4.1.2); a body without a zlib header decodes as raw DEFLATE. Decoding starts on the first body read; HEAD, 1xx, 204, 205 and 304 responses pass through untouched. Range requests receive no automatic `Accept-Encoding`; 206 or `Content-Range` responses preserve encoded bytes and headers. A complete 200 response to a range request is decoded normally. Request bodies are compressed only when the caller explicitly uses `Compress` and sets the corresponding request headers.

### Cache

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithCacheIdentityHeaders(names...)` | `...string` | none (`Authorization` and `Cookie` always partition) | Extra headers included in the cache key, such as `X-API-Key`; the application must configure all identity headers |
| `WithStaleIfError(d)` | `time.Duration` | 0 (off) | Serve stale after an inner transport error (including local refusal) or 5xx; caller cancellation/deadlines remain errors |
| `WithStaleWhileRevalidate(d)` | `time.Duration` | 0 (off) | Serve stale for up to `d` while revalidating in the background |
| `WithMaxCacheBodyBytes(n)` | `int64` | `DefaultMaxCacheBodyBytes` (1 MiB) | Largest stored body; larger responses stream through unstored |
| `WithMaxRetention(d)` | `time.Duration` | `DefaultMaxRetention` (24 h) | Longest an entry is kept, including entries kept only to revalidate |
| `WithRevalidation(timeout, n)` | `time.Duration`, `int` | `DefaultRevalidateTimeout` (30 s), `DefaultMaxConcurrentRevalidates` (8) | Timeout and concurrency of background revalidations; `Close` stops them |
| `WithCacheHook(h)` | `MetricsHook` | the `WithMetrics` hook, else none | Receives `OnCacheHit` / `OnCacheMiss` |
| `WithMaxEntries(n)` (`NewMemoryCache`) | `int` | `DefaultMemoryCacheMaxEntries` (1024) | Entry cap; least recently used evicted first |
| `WithMaxBytes(n)` (`NewMemoryCache`) | `int64` | `DefaultMemoryCacheMaxBytes` (64 MiB) | Byte cap over bodies, headers and keys |

Cache keys use the effective URL (including `Request.Host`), plus hashed
`Authorization`, `Cookie` and configured identity headers. Other credentials
are not detected automatically. Invalidation applies within the request's
partition; coalescing and invalidation coordination are local to one
`CacheTransport` instance.

The cache supports explicit freshness, validators, `Vary`, corrected `Age`,
request cache directives and permitted stale reuse. Storage is limited to
eligible GETs and the documented status codes, with one `Vary` variant per
primary key. Caller-conditional, range, HEAD and body-bearing GET requests
bypass caching. See [supported HTTP policy](caching.md#supported-http-policy)
for storage, revalidation, stale fallback and invalidation boundaries.

### Retry

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithMaxAttempts(n)` | `int` | 3 | Attempts including the first |
| `WithBackoff(initial, max, mult)` | `time.Duration`, `time.Duration`, `float64` | 100 ms, 10 s, 2.0 | Exponential backoff; `max` also bounds `Retry-After` |
| `WithJitter(j)` | `float64` | 0.1 | Fractional variation above/below exponential backoff, within [0, 1], capped by max wait; not applied to `Retry-After` |
| `WithRetryOn(codes...)` | `...int` | 429, 502, 503, 504 | Statuses that retry; replaces the default set |
| `WithRetryIf(fn)` | `func(*http.Response, error) bool` | the status set, plus connection reset / refused / aborted, broken pipe, EOF and network timeouts | Full retry predicate |
| `WithRetryNonIdempotent()` | — | off | Also replay POST / PATCH without `Idempotency-Key` |
| `WithMaxReplayBytes(n)` | `int64` | `DefaultMaxReplayBytes` (1 MiB) | Most of a body without `GetBody` recorded, as the first attempt sends it, for replay; a larger body is sent once |
| `WithRetryHook(h)` | `MetricsHook` | the `WithMetrics` hook, else none | Receives `OnRetry` (and `OnRetryCause`) |

Replay requires an idempotent method (GET, HEAD, OPTIONS, TRACE, PUT, DELETE), an `Idempotency-Key` header, or `WithRetryNonIdempotent`. The server must make the replay safe; a key or opt-in does not deduplicate requests. Retried attempts clone the request metadata; bodies are still consumed and closed. `GetBody` supplies fresh bodies (`http.NewRequest` sets it for `*bytes.Buffer`, `*bytes.Reader` and `*strings.Reader`; `EncodeRequest` installs it too). Other bodies are recorded during the first attempt and replayed only if fully consumed within `WithMaxReplayBytes`. That bound limits recording, not upload size or `GetBody` bodies. Cancellation of a blocked upload depends on the wrapped transport honoring its context.

With rate/concurrency layers inside retry, an attempt can fail admission with `ErrRateLimited` / `ErrLimiterTimeout`; neither is retried by the default predicate. `WithWait` permits rate-token waiting; the concurrency limiter already waits, bounded by context and optional `WithAcquireTimeout`. A `Retry-After` longer than the max wait, or a wait past the request deadline, returns the last result unretried. Caller cancellation and deadlines are not retried.

### Circuit breaker

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithFailureThreshold(n)` | `int` | `DefaultFailureThreshold` (5) | Consecutive failures that open an origin's circuit; 1 through `math.MaxUint32` |
| `WithSuccessThreshold(n)` | `int` | `DefaultSuccessThreshold` (2) | Probes a half-open circuit admits, and consecutive successes that close it; 1 through `math.MaxUint32` |
| `WithOpenTimeout(d)` | `time.Duration` | `DefaultOpenTimeout` (30 s) | Time open before admitting probes |
| `WithIsFailure(fn)` | `func(*http.Response, error) bool` | any inner Go error, nil response or 5xx | Classifies an outcome; caller cancellation is excluded first; deadline errors count |
| `WithMaxBreakers(n)` | `int` | `DefaultMaxOrigins` (1024) | Origins that keep a breaker; the least recently used is dropped and restarts closed. A request the dropped breaker admitted still completes but reports no transitions, so the cap bounds origins retained, not requests in flight |
| `WithOnStateChange(fn)` | `func(Origin, from, to State)` | nil | Synchronous transition callback, also for `Reset` and dropping a non-closed breaker; must be quick and must not call back into the transport |

One `gobreaker` two-step breaker per origin (scheme, host, port): an outcome admitted under an earlier state is ignored, a caller deadline expiring counts as a failure, and `State(origin)` / `Reset(origin)` inspect and clear it.

### Hedging

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `delay` (argument) | `time.Duration` | — (required, > 0) | Wait before each further attempt |
| `WithMaxHedgedRequests(n)` | `int` | 2 | Total attempts this hedge call may launch, including the first, >= 2 |
| `WithHedgeIf(fn)` | `func(*http.Request) bool` | GET only | Which requests are hedged; a custom predicate owns replay safety |
| `percentile` (adaptive argument) | `float64` | — (required, in (0, 1]) | Percentile of the last 1000 completed calls, including errors; starts at 100 ms and adapts after 10 samples |

The first response wins and keeps its context until its body is done; the other attempts are cancelled and their bodies drained and closed. A hedge runs beside the first attempt, so it needs its own copy of the body: a request body without `GetBody` is sent once, unhedged.

### Concurrency limiter

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `maxConcurrent` (argument) | `int` | — (required, >= 1) | In-flight requests across the client |
| `WithAcquireTimeout(d)` | `time.Duration` | 0 (wait for the request context) | Fail with `ErrLimiterTimeout` after `d` without a slot |
| `WithOnBlocked(fn)` | `func()` | nil | Called when a request has to wait |

A slot is held until the response body reaches EOF, returns a read error or is closed. The cap applies to this transport instance; close bodies to free slots.

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
| `NewRefreshableBearerTokenProvider(token, refresh)` | Bearer token; the application invokes `Refresh()` or `SetToken()` to rotate it |
| `NewBasicAuthProvider(user, password)` | `Authorization: Basic …` |
| `NewAPIKeyProvider(key)` / `NewAPIKeyProviderWithHeader(key, header)` | `X-API-Key: <key>` or a custom header |
| `NewQueryParamAuthProvider(name, value)` | A query parameter |
| `NewChainAuthProvider(providers...)` | Every provider in order |
| `FuncAuthProvider(fn)` | Whatever `fn` sets |

A provider runs only when both the request URL's origin and its effective `Host` match the binding (scheme, host and port). Caller-supplied URLs at that origin receive credentials; other origins, including redirect destinations, do not receive that provider's additions. Existing caller headers are passed through, not scrubbed. Bindings do not authorize destinations or implement token refresh on 401. `ParseOrigin` rejects URLs with userinfo, non-HTTP schemes and relative URLs. See [credential boundaries](security-observability.md#origin-bound-credentials).

### Metrics and logging

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithMetrics(hook)` | `MetricsHook` | off | Request and response events; also the retry and cache events unless `WithRetryHook` / `WithCacheHook` set theirs |
| `NewLatencyHook(window)` | `int` | `DefaultLatencyWindow` (1024) suggested | Running count / sum / avg / min / max plus the last `window` samples (`Recent()`) |
| `NewMultiHook(hooks...)` | `...MetricsHook` | — | Fans every event out, including `OnRetryCause`; nil hooks are skipped |
| `WithLogging(hook, opts...)` | `LoggingHook` | off | Detached, allowlisted request / response snapshots — no body, redirect response or TLS state — with sensitive headers, credential query parameters, URL userinfo and fragments redacted, in the request URL and in `Referer` / `Location` / `Content-Location`; `LogResponse` receives detached error text with request URLs, `url.Error` URLs and HTTP(S) URLs redacted (the caller receives the original). Other layers' hooks (`MetricsHook`, `RetryHook`, the cache hook) get the live request: do not log from them |
| `WithSensitiveHeaders(names...)` | `...string` | `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `X-Api-Key`, `X-Auth-Token`, `X-Access-Token`, `X-Refresh-Token`, `X-Csrf-Token`, `X-Amz-Security-Token` | Headers redacted to `Redacted`, in addition to the defaults |
| `WithSensitiveQueryParams(names...)` | `...string` | OAuth tokens and codes, API keys, passwords and secrets, `sig` / `signature`, AWS and GCS presigned-URL credentials | Query parameters redacted, in addition to the defaults; an unparseable query is redacted whole |

With the `Chain` API, pass `WithRetryHook` / `WithCacheHook` explicitly: only `NewClient` threads the client hook into the layers.

### Decoding helpers

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithMaxResponseBytes(n)` | `int64` | `DefaultMaxResponseBytes` (10 MiB) | Largest body `Get` / `Post` / `Put` / `Patch` / `Do` / `DecodeResponse` read; must be >= 1 |

The typed request helpers decode only 2xx representations; non-2xx responses, HEAD, 204 and 205 return zero `T`, the live response and a nil error. `DecodeResponse` instead decodes the supplied body with the supplied codec regardless of status or content type; the caller chooses when to call it. A larger body returns `ErrResponseTooLarge` and the response still reads the whole body (close it). After decoding, `resp.Body` holds the bytes read. When `T` is a pointer (`Do[*pb.Msg]`), the codec decodes into a newly allocated pointee. `Post` / `Put` / `Patch` encode a non-`io.Reader` body as JSON.

### Response cleanup

`DrainAndClose(ctx context.Context, body io.ReadCloser, maxDrainBytes int64) CleanupResult`
explicitly transfers one body's cleanup ownership. The byte budget has no
default: zero closes without reading; negative values record `ErrInvalidConfig`
and still close. The budget applies only to bytes currently exposed by the
body. `CleanupResult` contains `BytesDrained int64`, `EOF bool`, `DrainErr error`
and `CloseErr error`; reaching the budget without an `io.EOF` read does not prove
completion. Nil/typed-nil bodies and `http.NoBody` are known empty. The result
does not implement `error` or decide protocol acceptance. See
[bounded response cleanup](requests.md#bounded-response-cleanup) for error,
cancellation, privacy and single-owner lifecycle semantics.

## Errors

httpx defines sentinels, not E-codes; match them with `errors.Is`.

| Sentinel | Returned by | Caller action |
|----------|-------------|---------------|
| `ErrInvalidConfig` | constructors and helpers that validate options | Fix the configuration |
| `ErrCircuitOpen` | `CircuitTransport` | Allow recovery time; later requests may be admitted as probes |
| `ErrRateLimited` | `RateLimitTransport` | Respect the rate budget; consider `WithWait` |
| `ErrLimiterTimeout` | `LimiterTransport` | Release open bodies, reduce concurrency or revise the acquisition budget |
| `ErrResponseTooLarge` | `Get`, `Post`, `Put`, `Patch`, `Do`, `DecodeResponse`, `ReadAtMost` | Use a deliberate larger bound or stream the body |
| `ErrNoCodec` | typed request helpers | Register the response content type's codec or decode explicitly |

None of these sentinels is retried by the default retry predicate. A later
application request may succeed after a circuit or local budget recovers;
that is separate from retrying the current call.

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
- `DrainAndClose(ctx, body, maxDrainBytes) CleanupResult` — bounded remaining-body cleanup and separate drain/close observations
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
