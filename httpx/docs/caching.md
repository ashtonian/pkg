# HTTP caching

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

## When to enable it

Caching is opt-in. Use it for repeated GETs whose origin supplies an explicit
freshness or validation contract, and when serving a previously fetched
representation is correct for the caller. Choose stale-if-error for reads
that can tolerate old data during failure; choose stale-while-revalidate
when low latency matters more than seeing a newly changed value immediately.
Neither is a good default for authorization decisions or fresh account balances.

`WithCache(nil)` installs a bounded memory store: at most 1024 entries,
64 MiB of accounted storage, 1 MiB per body, and 24-hour retention. Configure
these independently for your payloads and traffic. Responses larger than the
body bound pass through without being stored.

## Identity and authority

Keys include the effective target authority (including `Request.Host`) and
hash `Authorization`, `Cookie` and every configured identity header. For
multi-tenant calls, use `WithCacheIdentityHeaders("X-Tenant-ID")` when that
header changes the representation. Credential changes create distinct
partitions. `Vary: Host` reads the effective authority too.

A client certificate or identity hidden inside a custom transport is not a
header: use a separate client/store or an explicit identity header. Custom
chains must put auth outside cache so credentials exist when the cache key
is computed. Do not share a store across different identities while omitting
those identity dimensions.

## Supported HTTP policy

| Case | Behavior |
|---|---|
| Fresh GET response | Reuse within its identity and matching `Vary` fields |
| `Cache-Control: no-store` | Do not store; a revalidation replacement retires the older representation |
| `no-cache` | Revalidate before reuse |
| `must-revalidate` | Do not use stale fallback after expiry |
| `max-age`, `min-fresh`, `max-stale` | Respect the request's freshness constraints |
| `only-if-cached` | Return a synthetic 504 if no acceptable cached response exists |
| ETag / Last-Modified | Conditional revalidation; merge 304 metadata and recheck storage policy |
| `Vary: *`, including a wildcard within a list | Refuse storage |
| `Date` / `Age` | Correct freshness for response delay and time already spent cached |
| No explicit freshness | Store only with a validator, and revalidate every use |
| HEAD, Range, conditional GET, or GET with a body | Bypass cache |
| Successful unsafe method | Invalidate target plus same-origin `Location` / `Content-Location` targets |
| Read failure | Preserve the original read error; never cache a truncated body as successful |

Stale permissions can come from RFC 5861 response directives or configured
`WithStaleIfError` / `WithStaleWhileRevalidate`. They remain subject to
`no-cache` and `must-revalidate`. An in-flight fetch that crosses an
invalidation cannot publish its older representation back into the store.

## Background work and stores

Background revalidations coalesce per key and are bounded by concurrency
and timeout (defaults: 8, 30 seconds). `CacheTransport.Close()` cancels them
and waits. Call it during shutdown, then close idle transport connections.
For a client built with `WithCache`, find its cache using `TransportStack`:

```go
for _, transport := range httpx.TransportStack(client) {
    if cache, ok := transport.(*httpx.CacheTransport); ok {
        if err := cache.Close(); err != nil {
            return err
        }
    }
}
client.CloseIdleConnections()
```

A custom `CacheStore` must be concurrent-safe and treat stored
`CachedResponse` values as immutable. Implement `Get`, `Set` with a TTL, and
`Delete`; constructor injection keeps a Redis-backed or other store possible.
Request coalescing and invalidation-generation protection belong to a
`CacheTransport` instance. They are not a distributed coordination protocol
between separate processes/transports sharing a store.

## Diagnose a result

Read `Cache-Status`: `httpx; hit` is fresh; `detail=stale-while-revalidate` or
`stale-if-error` identifies stale use; `fwd-status=304` shows revalidation;
`fwd=miss; stored` shows a fetch and store. A missing hit can be correct when
identity, `Vary`, directives, body size or method makes reuse unsafe.

See the [cache and memory options](reference.md#cache) and
[executable cache example](../example_test.go).
