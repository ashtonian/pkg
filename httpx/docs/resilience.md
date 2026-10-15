# Retries, breakers and request budgets

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

## Retries

Wrap your transport to add retries to an ordinary `http.Client`:

```go
transport, err := httpx.NewRetryTransport(http.DefaultTransport,
    httpx.WithMaxAttempts(3),
    httpx.WithBackoff(100*time.Millisecond, 2*time.Second, 2),
)
if err != nil {
    return err
}
client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
defer client.CloseIdleConnections()
```

`NewRetryTransport` defaults to three total attempts. The default statuses are
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

## Failure handling and failover

The layers preserve `net/http`'s separation between an HTTP response and a
Go error. Retry and breaker classification are policies for handling an
attempt; they do not turn every unsuccessful HTTP status into an error.

| Situation | Layer behavior and caller responsibility |
|---|---|
| Transient connection failure | Retry when the method/key and body permit replay. After the attempt budget, the last transport error remains available through `errors.Is` / `errors.As`. |
| HTTP 429, 502, 503 or 504 | Retry within the attempt, backoff and deadline budgets. If the budget is exhausted or `Retry-After` is too long, return the last response; inspect its status and close its body. |
| HTTP 400, 401, 403, 404, 500 or another status outside the retry set | Return the response without retry by default. Configure `WithRetryOn` / `WithRetryIf` when the API's contract calls for different behavior; authorization repair and business errors stay with the application. |
| Open circuit | Reject before calling the origin with `ErrCircuitOpen`; admit recovery probes after the configured interval. A cache outside the breaker can still serve usable entries. |
| Local rate or concurrency refusal | Return `ErrRateLimited` or `ErrLimiterTimeout`. These are not retried by default; local admission refusal does not count as an origin failure in the recommended layer order. |
| Cancellation or expired request budget | Stop retry waits and admission waits. Context errors retain `context.Canceled` / `context.DeadlineExceeded` for `errors.Is`. |
| Upstream error with a previously cached response | `stale-if-error` may serve that representation when its freshness/identity policy permits. `Cache-Status` identifies the fallback; `no-cache` and `must-revalidate` still prohibit stale use. |
| Failure while reading/decompressing/decoding the body | Surface it to the body reader or typed helper. Retry operates on the round trip and does not restart a response after body consumption begins. |

### Choosing a backup upstream

httpx has no built-in endpoint list, host rotation, service discovery or
cross-region failover. Retry resubmits the same target; hedging overlaps
attempts to that target; a circuit breaker records origin health but does
not choose another origin. Stale-cache fallback reuses a representation and
does not contact a backup server.

For multiple origins, select the destination in application code before
the request enters auth/cache layers, or use a load balancer behind a stable
logical origin. Carry one overall deadline across the operation, preserve
safe body replay and server idempotency, and bind credentials/cache identity
to the selected origin. The [composition guide](composition.md) explains
where your own transport fits and how layer order affects those policies.

## Circuit breakers

Add `NewCircuitTransport` for a persistently failing upstream. Each
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
| `NewRateLimitTransport(base, rps, burst)` | Attempts per second, per origin | Tokens refill over time; default is immediate `ErrRateLimited` |
| `NewRateLimitTransport(..., WithWait())` | The same rate, with bounded waiting | Wait uses the request context; a wait past the deadline is rejected |
| `NewLimiterTransport(base, n)` | In-flight attempts across the transport | Slot releases on response EOF, read error or Close |
| `WithMaxConnsPerHost(n)` | Transport connections per host | Connections may carry several HTTP/2 requests |

Put limits inside retry and hedging so each new attempt spends a token and
a slot. Put them outside the breaker so local refusal is not an upstream
failure. A hit in the cache spends neither; a background refresh goes
through the same protected network path. The optional client builders use
this order; explicit chains choose their own. See [composition](composition.md).

## Hedging

`NewHedgingTransport` can improve tail latency when reads sometimes stall while spare
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
