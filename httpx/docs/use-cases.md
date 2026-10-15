# Choosing httpx features

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

Use httpx when your policy depends on HTTP: methods, status codes,
`Retry-After`, cache directives, credentials or content types. For a simple
one-off request, an ordinary `http.Client` with a timeout may be sufficient.
For retrying a function or caching application objects, use a library for
that operation instead of wrapping it in HTTP semantics.

## Choose an entry point

| Entry point | Use it when | Included behavior |
|---|---|---|
| `NewStandard(opts...)` | Most API clients, webhooks and service-to-service calls | Tuned HTTP/2-capable transport, gzip/deflate, safe retry with three attempts, 30 s total timeout; options override defaults |
| `NewClient(opts...)` | You need explicit control over which policies exist | Tuned transport and 30 s timeout; add compression, retry and other layers explicitly |
| `NewChain(...).Client(base, timeout)` | You need a different order or a custom decorator between layers | Exactly the layers you list, first-added innermost; timeout is explicit |
| Individual `New*Transport` constructors | You already own a client or need direct lifecycle/state access | One concern around your existing `RoundTripper` |
| Typed `Get[T]` / `Do[T]` helpers | The complete response is a bounded structured value | Status-aware decoding by response `Content-Type`; default maximum 10 MiB |
| `client.Do(req)` | Downloads, SSE, large uploads, streaming or custom decoding | Streaming `net/http` body ownership; configure bounds/deadlines for your workload |

`NewStandard` has no cache, breaker, rate limit, concurrency limit, auth or
hedging unless you enable them. Reuse a client across calls to retain its
connection pools, breaker history and rate buckets.

## Pick policies for the workload

| Workload | Useful starting point | Decision to make |
|---|---|---|
| JSON/XML API or SDK | `NewStandard`, typed helpers, origin-bound auth | Set a response-size limit and handle non-2xx explicitly |
| Webhook, payment or resource creation | `NewStandard`; construct a request with an idempotency key only if the server implements that contract | POST/PATCH run once by default; retrying a side effect can duplicate it |
| Flaky upstream | Retry plus a per-origin breaker | Retries absorb transient failure; the breaker stops sustained failure from consuming your budget |
| Upstream quota | `WithRateLimit(rps, burst, WithWait())` | Rate limits count attempts, including retries/hedges; waiting consumes the request deadline |
| Memory/socket pressure | `WithLimiter(n)` plus transport pool settings | A limiter slot lasts until body EOF/error/Close; connection count and in-flight request count differ under HTTP/2 |
| Repeated reads with an explicit freshness contract | `WithCache` | Follow cache headers, partition all identity headers, and decide whether stale data is acceptable |
| Read-heavy, latency-sensitive service | Measured hedge delay plus strict attempt budgets | Hedging creates extra load and does not wait for a successful status; avoid it when the server is overloaded |
| Tenant-specific API | `WithAuth` and, if caching, `WithCacheIdentityHeaders("X-Tenant-ID")` | Separate clients/stores for identities not represented in headers, such as mTLS certificates |
| SSE or long polling | Raw `client.Do`; per-request context; possibly `WithTimeout(0)` | Also tune the response-header timeout; leave cache/hedging off for streams and manage reconnection explicitly |
| Large upload | Reader body with a deadline | A non-replayable stream is sent once unless retry records the entire first attempt within its bound |
| Ranged download | Raw `client.Do` with `Range` | Partial encoded representations stay encoded; the caller interprets `Content-Range` and coding |
| Alternate wire format | An optional codec module | `EncodeRequest` chooses request encoding; the response's content type chooses decoding |

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
