# Choosing httpx features

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

Use httpx to add HTTP behavior to a Go client through `http.RoundTripper`:
ETag revalidation, cache directives, compression negotiation, or safe retries
with backoff and `Retry-After`. You keep using `http.Client`,
`http.NewRequestWithContext`, `client.Do` and `http.Response`. Each transport
works independently, and can wrap a transport you already use.

For a simple request, `net/http` alone may be enough: it already provides
connection pooling, TLS, redirects, cookies and automatic gzip handling.
Choose httpx when you need the additional policies below. Application-object
caching, arbitrary function retries and server-side idempotency belong
outside these HTTP transports.

## Choose an entry point

| Entry point | Use it when | Included behavior |
|---|---|---|
| Individual `New*Transport` constructors | Add one policy to an existing client, or retain a layer's lifecycle/state handle | One decorator around your `RoundTripper` |
| `NewChain(...).Build(base)` | Compose several policies or include your own decorator | A `RoundTripper` for an ordinary client's `Transport`; first-added innermost |
| `client.Do(req)` | Make requests through any of those transports | Standard response/status/body handling, including streaming and custom decoding |
| `NewStandard(opts...)` | Create a new client with convenience defaults | Tuned HTTP/2-capable transport, gzip/deflate, three safe retry attempts, 30 s total timeout |
| `NewClient(opts...)` | Create a new client with a fixed layer order | Tuned transport and 30 s timeout; add policies explicitly |
| Typed `Get[T]` / `Do[T]` helpers | Optionally decode a complete bounded structured value | Response `Content-Type` chooses the codec; default maximum 10 MiB |

The builders return `*http.Client`; they are optional. `NewStandard` has no
cache, breaker, rate limit, concurrency limit, auth or hedging unless enabled.
Reuse clients and transports to retain their connection pools and policy
state. See [composition](composition.md) for preserving an existing client's
cookie jar, redirect policy, timeout and custom transport.

## Pick policies for the workload

| Workload | Useful starting point | Decision to make |
|---|---|---|
| Repeated GETs with ETag or Last-Modified | `NewCacheTransport` | Revalidate stale entries; a 304 reuses the stored body with updated metadata |
| Reads with explicit freshness | `NewCacheTransport` | Fresh hits avoid the network; choose stale permissions only when old data is acceptable |
| Compressed responses | `NewCompressionTransport` | Add deflate, optional Brotli, encoding preferences and consistent lazy decoding; standard Go already handles basic gzip |
| Transient network errors, 429 or temporary gateway failures | `NewRetryTransport` | Bound attempts and backoff by the request deadline; respect `Retry-After` and replay safety |
| Webhook, payment or resource creation | Retry transport only with a safe replay contract | POST/PATCH run once by default; an idempotency key is useful only when the server deduplicates it |
| Persistently failing upstream | Retry plus `NewCircuitTransport` | The breaker stops repeated attempts during sustained failure |
| Upstream quota | `NewRateLimitTransport` | Rate limits count attempts when inside retry/hedging; waiting consumes the deadline |
| Memory/socket pressure | `NewLimiterTransport` plus transport pool settings | A slot lasts until body EOF/error/Close; HTTP/2 connection count differs from in-flight requests |
| Read-heavy, latency-sensitive service | `NewHedgingTransport` with measured delay and strict budgets | Hedging adds load and accepts the first response, including an error status |
| Tenant-specific API | `NewAuthTransport` and, if caching, `WithCacheIdentityHeaders("X-Tenant-ID")` | Auth must run before cache lookup; separate stores for identities hidden in a transport, such as mTLS |
| SSE or long polling | Raw `client.Do`; per-request context; possibly `WithTimeout(0)` | Also tune the response-header timeout; leave cache/hedging off for streams and manage reconnection explicitly |
| Large upload | Reader body with a deadline | A non-replayable stream is sent once unless retry records the entire first attempt within its bound |
| Ranged download | Raw `client.Do` with `Range` | Partial encoded representations stay encoded; the caller interprets `Content-Range` and coding |
| Bounded JSON/XML response | Optional typed helpers on the same client | Set a response-size limit and handle non-2xx explicitly |
| Alternate representation format | An optional codec module | `EncodeRequest` chooses request encoding; the response's content type chooses decoding |

## Retry, hedge and circuit breaker are different

A retry starts after an attempt fails. A hedge starts while an earlier
attempt is still slow. A circuit breaker declines new attempts when an
origin has failed enough times. Combining them is useful only when the
extra attempts fit your latency budget and the upstream's capacity.

With both retry and hedging, the maximum origin attempts can reach
`MaxHedgedRequests × MaxAttempts`. Rate and concurrency admission apply
to each of those attempts. See [resilience](resilience.md) before changing
the defaults.

## Limits of the boundary

httpx supplies client-side HTTP policy. The application still owns server
idempotency/deduplication, authorization decisions, URL allowlists and SSRF
protection, business-error interpretation, distributed quotas, and streaming
protocol parsing. A credential binding controls where its provider adds
credentials; it does not validate every destination the application accepts.
