# Transport composition

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

## Add a behavior to an existing client

Each `New*Transport` accepts an `http.RoundTripper` and implements that same
interface. You can use one layer without adopting a client builder or typed
request API. For example, add retry/backoff to a configured client:

```go
base := existing.Transport
if base == nil {
    base = http.DefaultTransport
}
retrying, err := httpx.NewRetryTransport(base,
    httpx.WithMaxAttempts(3),
    httpx.WithBackoff(100*time.Millisecond, 2*time.Second, 2),
)
if err != nil {
    return err
}
client := *existing
client.Transport = retrying
// client.Do(req) keeps existing.Timeout, CheckRedirect and Jar.
```

Configure the client before using it; do not change its fields while requests
are running. This copy shares the original transport's connection pool and
cookie jar. Your transport still controls proxies, TLS, dialing and HTTP
versions. Closing idle connections through either client affects that shared
pool. Set an overall client timeout or request-context deadline appropriate
for the work, including any retry waits and response reads.

## Compose several behaviors

`NewChain(...).Build(base)` returns a transport, not a new request API.
The first layer listed is innermost; the last sees the request first:

```go
transport, err := httpx.NewChain(
    httpx.WithCompressionT(),
    httpx.WithRetryT(httpx.WithMaxAttempts(3)),
).Build(http.DefaultTransport)
if err != nil {
    return err
}
cache, err := httpx.NewCacheTransport(transport, nil)
if err != nil {
    return err
}
client := &http.Client{Transport: cache, Timeout: 10 * time.Second}
// Use client.Do normally. At shutdown, close cache before idle connections.
```

The request travels through cache → retry → compression → your transport.
A fresh hit skips the network; revalidation passes through retry and
compression; the cache retains the decoded response. Keeping the cache
handle makes `cache.Close()` explicit at shutdown. The
[quick start](../examples/quickstart/main.go) includes the complete lifecycle.

You can insert any compatible decorator as a `TransportFunc`, or wrap the
result yourself. `Build(nil)` constructs a tuned `NewBaseTransport`.
`Chain.Client(base, timeout)` is an optional shorthand for putting the built
transport on a new `http.Client`; its timeout is explicit, and zero disables
the client-wide timeout.

## Layer order

Order controls behavior. This is the order used by the optional `NewClient`
and `NewStandard` builders, regardless of option order. Every layer is
optional; the table lists enabled layers in request order, outer to inner.

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

## Optional builders and lifecycle

With `NewClient` or `NewStandard`, `WithTransport(rt)` replaces the base while
retaining the configured outer layers. It overrides `WithBaseTransport`;
the injected transport owns its pool/TLS/proxy behavior. These constructors
create a new client; use the pattern above to preserve an existing client's
settings.

Every built-in decorator forwards `CloseIdleConnections` and exposes
`Unwrap`. A cache also needs `CacheTransport.Close()` to stop background
refreshes; closing idle connections alone does not stop them. Custom
decorators should forward idle close too, respect context
cancellation, close request bodies on rejection, and preserve response body
interfaces when supporting protocol upgrades. A transport that ignores
cancellation can prevent background cleanup from finishing.

`TransportStack(client)` inspects layers outermost first. `NewClient`
threads a configured metrics hook into retry/cache; a custom chain needs
`WithRetryHook` and `WithCacheHook` explicitly. Per-layer hooks override
client-wide forwarding.
