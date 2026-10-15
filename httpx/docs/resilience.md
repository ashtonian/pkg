# Retries, breakers and request budgets

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

## Retries

Start with `NewStandard` for three total attempts. The default statuses are
429, 502, 503 and 504; transient network errors can also retry. Cancellation
and caller deadlines stop retries. `WithRetryIf` replaces the failure
predicate, but safe replay and body availability still apply.

Automatic replay is allowed for GET, HEAD, OPTIONS, TRACE, PUT and DELETE,
or a request carrying `Idempotency-Key`. POST/PATCH without that key are
sent once unless you explicitly select `WithRetryNonIdempotent`. A key is
only safe when the server actually deduplicates it; httpx cannot make an
arbitrary write idempotent.

A request's `GetBody` supplies a fresh body per attempt. Without it, retry
records bytes as the first attempt sends them and only replays if the whole
body was consumed within `WithMaxReplayBytes` (1 MiB). It never pre-reads a
pipe while the transport cannot cancel it. Larger or incompletely consumed
streams are sent once.

Backoff is exponential with jitter. `Retry-After` is a minimum server wait;
when that wait exceeds the configured maximum or the remaining deadline,
httpx returns the last response without another attempt. Numeric bounds
saturate instead of wrapping. Tune the attempt count and backoff together
with the overall timeout, not independently.

## Circuit breakers

Enable `WithCircuitBreaker` for a persistently failing upstream. Each
scheme/host/port has its own breaker. Five consecutive failures open it by
default; after 30 seconds it admits two half-open probes, and two successes
close it. Network errors and 5xx responses are failures by default; caller
cancellation and local rate/concurrency rejection do not trip it.

The bounded origin registry defaults to 1024 entries. Eviction restarts a
returning origin with a closed circuit; it is not a durable failure history
or a strict admission budget under adversarial origin churn. A single
client calling one unstable origin will not open another origin's circuit.

Use `TransportStack` to locate `*CircuitTransport` for `State(origin)` or
`Reset(origin)`. `WithOnStateChange` runs synchronously: keep it short and do
not call back into the transport from it. Thresholds must fit an unsigned
32-bit counter, from 1 through `math.MaxUint32`.

## Rate versus concurrency

| Limit | Controls | Release/refill |
|---|---|---|
| `WithRateLimit(rps, burst)` | Attempts per second, per origin | Tokens refill over time; default is immediate `ErrRateLimited` |
| `WithRateLimit(..., WithWait())` | The same rate, with bounded waiting | Wait uses the request context; a wait past the deadline is rejected |
| `WithLimiter(n)` | In-flight attempts across the client | Slot releases on response EOF, read error or Close |
| `WithMaxConnsPerHost(n)` | Transport connections per host | Connections may carry several HTTP/2 requests |

Limits sit inside retry and hedging, so each new attempt spends a token and
a slot. They sit outside the breaker so local refusal is not an upstream
failure. A hit in the cache spends neither; a background refresh goes
through the same protected network path. See [composition](composition.md).

## Hedging

Hedging can improve tail latency when reads sometimes stall while spare
upstream capacity exists. Measure the tail before choosing a delay. It
starts an extra request after the delay, up to two concurrent attempts by
default. It is off by default and only GET is eligible unless you change
`WithHedgeIf`. A body-bearing request needs `GetBody` so concurrent attempts
never share the same stream.

The first response wins, including an HTTP error status. The other attempts
are cancelled and their responses closed. The winner remains readable until
its body is finished. With retry enabled inside hedging, each hedge runs its
own retry sequence; capacity may be consumed faster than a logical-call
count suggests.

`NewAdaptiveHedgingTransport` derives the delay from a bounded latency
sample window. It is available as an individual transport; choose it only
when its observed latency distribution is appropriate for your workload.

All [options and defaults](reference.md#retry) are documented in the reference.
