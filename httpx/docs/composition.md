# Transport composition

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

## Fixed client stack

`NewClient` fixes layer order regardless of option order. `NewStandard`
uses the same builder with compression/retry/timeout defaults. Every layer
is optional; the table lists enabled layers in request order, outer to inner.

| Layer | What it observes |
|---|---|
| Logging | Redacted snapshots of the logical request and response |
| Metrics | Logical round-trip latency; retry/cache events are also forwarded |
| Auth | Destination before credential injection |
| Cache | Credential-bearing request, before origin admission |
| Hedging | Logical slow request; may run more than one retry sequence |
| Retry | Each sequential attempt and its status/error |
| Rate limit | Each attempted outbound request, by origin |
| Concurrency limiter | Each attempt through its response-body lifetime |
| Circuit breaker | Each admitted attempt's upstream outcome |
| Compression | Wire content coding and lazy response decoding |
| Base transport | Dialing, TLS, HTTP/1.1 or HTTP/2, pools and socket I/O |

This order makes cached hits available while an origin's breaker is open,
protects refresh traffic with the same budgets, and avoids counting local
admission refusal as an upstream failure. Auth precedes cache identity.

## Explicit chains

`NewChain` applies the first function innermost. This is the inverse of
request traversal: the last-added layer sees the request first. Start from
the fixed order unless you intentionally need different semantics:

```go
client, err := httpx.NewChain(
    httpx.WithCompressionT(),
    httpx.WithCircuitBreakerT(),
    httpx.WithLimiterT(16),
    httpx.WithRateLimitT(50, 100, httpx.WithWait()),
    httpx.WithRetryT(httpx.WithMaxAttempts(3)),
    httpx.WithCacheT(nil),
    httpx.WithAuthT(apiURL, httpx.NewBearerTokenProvider(token)),
).Client(nil, 10*time.Second)
if err != nil {
    return err
}
```

| Ordering change | Consequence |
|---|---|
| Breaker outside retry | Counts one logical outcome instead of each attempt |
| Rate/concurrency limit outside retry or hedge | Budgets calls rather than actual attempts |
| Breaker outside local admission | Can count local refusals as upstream failures |
| Cache inside breaker | An open breaker can block a usable hit |
| Cache outside auth | Identity can be captured before credentials are applied |

Preset chains have distinct defaults: `DefaultChain` includes compression,
a breaker and retry; `APIClientChain` includes compression, retry and cache;
`ResilientChain` adds a limiter and stale-if-error caching with more attempts.
They are not aliases for `NewStandard`. See [defaults](reference.md).

## Bring your own client or transport

`WithTransport(rt)` replaces the base while retaining the configured outer
layers. It overrides `WithBaseTransport`; the injected transport owns its
pool/TLS/proxy behavior. Individual decorators also work with an existing
`http.Client.Transport`. Copy a shared client before replacing its transport.

Every built-in decorator forwards `CloseIdleConnections` and exposes
`Unwrap`. Custom decorators should forward idle close too, respect context
cancellation, close request bodies on rejection, and preserve response body
interfaces when supporting protocol upgrades. A transport that ignores
cancellation can prevent background cleanup from finishing.

`TransportStack(client)` inspects layers outermost first. `NewClient`
threads a configured metrics hook into retry/cache; a custom chain needs
`WithRetryHook` and `WithCacheHook` explicitly. Per-layer hooks override
client-wide forwarding.
