# Development and release checks

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

## Test lanes

The HTTPx tests are tagged. Plain `go test ./...` does not run them. In the
core module, use:

```sh
GOWORK=off go build ./...
GOWORK=off go vet -tags=unit,integration ./...
GOWORK=off go test -tags=unit -race -count=1 ./...
GOWORK=off go test -tags=integration -race -count=1 ./...
```

Unit tests use scripted transports and `testing/synctest` for time-based
contracts. Integration tests exercise real HTTP/TLS servers, redirects,
body lifetime, cache/retry composition and compression. The quick start is
compiled as part of the module and tested against a real local API.

Run optional modules in the repository workspace, or against released
parent versions with `GOWORK=off`. The three optional README examples are
also compiled examples. See [Gin](../examples/gin/README.md) for the complete
server/client demonstration and its integration tests.

## Release archives

From the repository root, `make httpx-release-check` creates separate Go
module ZIPs for the core, MessagePack, protobuf and Brotli modules. Fresh
consumers resolve them through a local module proxy, outside the workspace,
with their own module cache. The check also validates the Gin example and
comparison benchmarks against the candidate modules.

This catches private imports, missing module files, invalid parent-version
requirements and nested-module packaging problems that a workspace build
can hide. Every published module needs its own license and its own
path-prefixed release tag. The optional modules require a real released
parent version and contain no local `replace` directives.

The initial public module version is `v0.1.0`. Later modules can version
independently. Release the parent first when an optional module needs a new
parent API, then raise that module's requirement. The public repository's
release workflow owns tags and changelogs.

## Benchmarks

```sh
GOWORK=off go test -tags=integration -run '^$' -bench=. -benchmem ./
cd internal/bench
GOWORK=off go test -tags=integration -run '^$' -bench=. -benchmem ./
```

The [comparison suite](../internal/bench/README.md) compares equivalent
operations with resty, go-retryablehttp and gobreaker. Report the toolchain,
machine, payload and configured layers with results; a local benchmark is
not an application throughput guarantee.

## Code map
| File | Responsibility |
|------|----------------|
| `client.go` | `NewClient`, `Option`s and the fixed layer order |
| `standard.go` | `NewStandard`, `MustNewStandard` |
| `chain.go` | `Chain`, `TransportFunc`, `With*T` layer functions, presets |
| `base.go` | `NewBaseTransport` and `BaseOption`s (timeout matrix, pooling) |
| `transport.go` | Shared interfaces (`CacheStore`, `CachedResponse`, `MetricsHook`, `LoggingHook`, `AuthProvider`), content types, `RetryCause` |
| `origin.go` | `Origin`, `ParseOrigin`, `OriginOf` |
| `originstate.go` | Bounded per-origin LRU registry behind the breaker and rate limiter |
| `compression.go` | `CompressionTransport`, lazy decoding, gzip and zlib-deflate compressors |
| `cache.go` | `CacheTransport`: keys, storage, revalidation, invalidation, `Cache-Status` |
| `cachepolicy.go` | `Cache-Control` parsing, corrected age, freshness |
| `memcache.go` | `MemoryCache`, the bounded LRU `CacheStore` |
| `circuitbreaker.go` | `CircuitTransport`, `State`, per-origin gobreaker breakers |
| `retry.go` | `RetryTransport`: safe-replay gate, backoff, `Retry-After` |
| `hedging.go` | `HedgingTransport`, `AdaptiveHedgingTransport` |
| `limiter.go` | `LimiterTransport` |
| `ratelimit.go` | `RateLimitTransport` |
| `auth.go` | `AuthTransport`, `AuthBinding` and the providers |
| `metrics.go` | `MetricsTransport`, `CountingHook`, `LatencyHook`, `MultiHook` |
| `logging.go` | `LoggingTransport`, redaction, `StructuredLoggingHook`, `LogEntry` |
| `request.go` | `Get` / `Post` / `Put` / `Patch` / `Delete` / `Do[T]` |
| `decode.go` | `DecodeOption`, `WithMaxResponseBytes`, bounded read, pointer-aware target |
| `codec.go` | `Codec`, JSON and XML codecs, `EncodeRequest`, `DecodeResponse` |
| `registry.go` | Codec and compressor registries |
| `body.go` | Body-lifetime release and `CloseIdleConnections` forwarding |
| `helpers.go` | Request cloning, body replay (GetBody, or recorded as the first attempt sends it), drain-and-close |
| `pool.go` | `BufferPool` |
| `errors.go` | Sentinels and `ErrInvalidConfig` wrapping |
| `doc.go` | Package documentation |
