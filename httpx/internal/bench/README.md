# httpx/internal/bench

Head-to-head benchmarks of httpx against popular Go HTTP client libraries.

This is a **separate Go module** to isolate benchmark dependencies from the main httpx module. Users of httpx never pull in resty, go-retryablehttp or gobreaker v1.

## Running

```bash
GOWORK=off go test -tags=integration -bench=. -benchmem
```

The benchmarks carry the `integration` build tag, like every test that starts a server.

## Benchmark Suite

| Benchmark | What it measures | Libraries compared |
|-----------|------------------|--------------------|
| `BenchmarkRetry/*` | Retry transport overhead (no actual retries) | httpx, hashicorp/go-retryablehttp, resty |
| `BenchmarkCircuitBreaker/Closed/*` | Circuit breaker pass-through overhead | httpx, sony/gobreaker |
| `BenchmarkCircuitBreaker/Open/*` | Circuit breaker rejection speed | httpx, sony/gobreaker |
| `BenchmarkFullClient/*` | Complete client stack (retry+timeout+more) | httpx, httpx Chain API, resty |
| `BenchmarkCache/*` | HTTP caching vs no caching | httpx (RFC 9111 cache), resty (no cache) |
| `BenchmarkAllocation/*` | Memory allocation profiling | httpx only |

## Interpreting results

- Run repeated samples on the same machine with the same Go version before comparing results. These benchmarks are fixtures, not a performance guarantee.
- Retry comparisons use three total attempts, but the healthy server does not trigger a retry.
- The open-circuit case uses a deterministic fake 503 to open the measured origin; its timed loop performs no network I/O.
- `httpx_Hit` returns from memory while `resty_NoCache` performs a network request. They measure different work, not equivalent client overhead.
- The full-client configurations enable different features; read the setup before attributing a difference to one library.

For a quick executable check of every benchmark:

```bash
GOWORK=off go test -tags=integration -run '^$' -bench . -benchtime=1x
```

## Why a Separate Module?

Users of httpx should not have to pull in resty, go-retryablehttp or gobreaker v1. By isolating benchmarks here, the main httpx module stays lightweight: its only dependencies are `golang.org/x/time` and `github.com/sony/gobreaker/v2`.
