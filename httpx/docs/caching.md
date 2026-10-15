# HTTP caching

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

## Add ETag revalidation to a standard client

Wrap the client's transport and keep using `client.Do`. The cache manages
validators and stored responses; callers do not need a cache-specific request
or response type:

```go
cache, err := httpx.NewCacheTransport(http.DefaultTransport, nil)
if err != nil {
    return err
}
client := &http.Client{Transport: cache, Timeout: 10 * time.Second}
defer client.CloseIdleConnections()
defer cache.Close()
```

For a server returning `ETag: "v1"` with `Cache-Control: no-cache`:

1. The first GET fetches and stores the response.
2. The next GET sends `If-None-Match: "v1"` to the server.
3. A `304 Not Modified` updates stored metadata and returns the original
   successful response with its body to the caller. A new `200` replaces it.

`Last-Modified` works similarly through `If-Modified-Since`. If the server
allows freshness with `max-age`, an entry can be reused without revalidation
until it becomes stale. `ETag` alone allows storage for validation, not a
fresh hit. The [quick start](../examples/quickstart/main.go) combines this
with compression and retries; its tests exercise real HTTP responses.

Requests with caller-supplied conditions bypass the cache. Set
`If-None-Match` yourself when you want to handle a raw 304; set `If-Match`
yourself for a conditional write. The cache does not implement application
conflict handling or generate validators for the server.

## When to enable it

Caching is opt-in. Use it for repeated GETs whose origin supplies an explicit
freshness or validation contract, and when serving a previously fetched
representation is correct for the caller. Choose stale-if-error for reads
that can tolerate old data during failure; choose stale-while-revalidate
when low latency matters more than seeing a newly changed value immediately.
Neither is a good default for authorization decisions or fresh account balances.

`NewCacheTransport(base, nil)` installs a bounded memory store: at most 1024 entries,
64 MiB of accounted storage, 1 MiB per body, and 24-hour retention. Configure
these independently for your payloads and traffic. Responses larger than the
body bound pass through without being stored. For a potentially storable
response, the cache reads up to the bound plus one byte before returning it,
then restores those bytes for the caller. Leave caching off for streams that
must deliver bytes immediately.

## Identity and authority

Keys include the effective target authority (including `Request.Host`) and
hash `Authorization`, `Cookie` and every configured identity header. A change
in one of those values creates a distinct partition. Other headers are not
included automatically: for API-key authentication, configure
`WithCacheIdentityHeaders("X-API-Key")`, or the actual custom header name.
Configure every header the application uses to identify the caller.
`Vary` also checks the fields named by the response; `Vary: Host` reads the
effective authority.

A client certificate or identity hidden inside a custom transport is not a
header: use a separate client/store or an explicit identity header. Custom
chains must put auth outside cache so credentials exist when the cache key
is computed. Do not share a store across different identities while omitting
those identity dimensions.

Partitioning separates stored responses by those inputs. The application
still owns authentication, authorization and tenant boundaries. Query-string
credentials remain part of the URL in the cache key; they are not hashed by
the header-partition mechanism. Protect a custom store accordingly.

## Supported HTTP policy

Storage requires a validator or explicit freshness, plus one of these status
codes: 200, 203, 204, 300, 301, 308, 404, 405, 410, 414 or 501. Other statuses
pass through without being stored. Each primary key retains one matching
`Vary` variant at a time; fetching another variant can replace it.

| Case | Behavior |
|---|---|
| Fresh GET response | Reuse within its identity and matching `Vary` fields |
| `Cache-Control: no-store` | Do not store; a revalidation replacement retires the older representation |
| `no-cache` | Revalidate before reuse |
| `must-revalidate` | Do not use stale fallback after expiry |
| `max-age`, `min-fresh`, `max-stale` | Respect the request's freshness constraints |
| `only-if-cached` on a cache-eligible GET | Return a synthetic 504 if no acceptable cached response exists |
| ETag / Last-Modified | Conditional revalidation; merge 304 metadata and recheck storage policy |
| `Vary: *`, including a wildcard within a list | Refuse storage |
| `Date` / `Age` | Correct freshness for response delay and time already spent cached |
| No explicit freshness | Store only with a validator, and revalidate every use |
| HEAD, Range, conditional GET, or GET with a body | Bypass cache |
| Unsafe method with a response below 400 | Invalidate target plus same-origin `Location` / `Content-Location` targets within the request's cache partition |
| Read failure | Preserve the original read error; never cache a truncated body as successful |

Stale permissions can come from RFC 5861 response directives or configured
`WithStaleIfError` / `WithStaleWhileRevalidate`. They remain subject to
`no-cache` and `must-revalidate`. Stale-if-error accepts inner transport
errors, including open-circuit and local rate/concurrency refusals, or 5xx
responses. Caller cancellation and expired caller deadlines remain errors;
fallback does not cover failures while reading a response body.

Invalidation applies within the request's cache partition and this transport's
coordination state. It does not invalidate other identities' entries or
related collection URLs. An in-flight fetch that crosses an invalidation in
the same transport cannot publish its older representation back into the store.

## Background work and stores

Background revalidations coalesce per key and are bounded by concurrency
and timeout (defaults: 8, 30 seconds). `CacheTransport.Close()` cancels them
and waits. Call it during shutdown, then close idle transport connections.
The direct constructor returns that handle. If you instead use `WithCache`
or `WithCacheT`, find the cache using `TransportStack`:

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

Read `Cache-Status`: `httpx; hit` means reuse and can include a stale response
accepted by the request's `max-stale`; `detail=stale-while-revalidate` or
`stale-if-error` identifies those fallback paths; `fwd-status=304` shows revalidation;
`fwd=miss; stored` shows a fetch and store. A missing hit can be correct when
identity, `Vary`, directives, body size or method makes reuse unsafe.

See the [cache and memory options](reference.md#cache) and
[executable cache example](../example_test.go).
