# Requests and responses

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

## Use standard requests and responses

Transport composition does not change the request API. Create requests with
`http.NewRequestWithContext`, set normal headers, and call `client.Do`:

```go
req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
if err != nil {
    return err
}
resp, err := client.Do(req)
if err != nil {
    return err
}
defer resp.Body.Close()
if resp.StatusCode != http.StatusOK {
    return fmt.Errorf("GET: HTTP %d", resp.StatusCode)
}
body, err := httpx.ReadAtMost(resp.Body, 1<<20)
```

A nil error does not mean HTTP success. Handle the status codes your
application accepts, then read bytes, call your own decoder, or stream the
body. `ReadAtMost` is an optional bounded reader: it returns
`ErrResponseTooLarge` rather than treating a prefix as a complete value.
Transport layers do not impose a general response-read limit for you.
See the [complete quick start](../examples/quickstart/main.go).

Close each response body. Read it to EOF when appropriate for connection
reuse, but do not drain an unbounded stream just to reuse a connection.
Closing also releases a concurrency limiter slot or hedge winner's context.

## Bounded response cleanup

`DrainAndClose(ctx, body, maxDrainBytes)` is an explicit cleanup operation after
the caller finishes inspecting a body or makes its protocol decision. It reads
at most the supplied budget from the body's current position and attempts
`Close` once, including after a read error, invalid budget or cancellation.
Replace an existing drain/close sequence with this operation; do not retain a
second deferred `Close`, call it repeatedly or use the body concurrently.
It does not close the client or its transport.

The result has `BytesDrained`, `EOF`, `DrainErr` and `CloseErr` fields. The budget
counts bytes exposed by that body, including a buffered prefix retained after
an oversized typed decode; it does not count earlier reads. Zero is close-only.
A negative budget or nil context records `ErrInvalidConfig` in `DrainErr`
without reading. Nil and typed-nil bodies are absent, and `http.NoBody` is known
empty; these report `EOF=true`. Other bodies report completion only when a read
returns `io.EOF`. A body exactly as long as the budget can leave `EOF=false`:
cleanup never reads an extra byte merely to prove completion. Exhausting the
budget alone is not a drain error. Repeated empty reads stop with
`io.ErrNoProgress`.

Cancellation is checked before and between reads. A context parameter cannot
interrupt an arbitrary blocking `Read` or `Close`; keep the original request
or client deadline and use a body that cooperates with cancellation. Cleanup
starts no goroutine and does not cancel the request's context.

Decide acceptance from the provider's protocol first. Required response
decoding still has to succeed; a later cleanup-only failure cannot reverse an
already accepted message or validated response. `CleanupResult` is an
observation value, not an error return. Keep its read and close errors separate
from delivery control flow. They preserve their underlying identities for
`errors.Is`, but may contain private diagnostics: log bounded classifications
or error-presence flags under the application's observation policy instead of
formatting arbitrary error strings.

## Streaming and request bodies

Pass an `io.Reader` to `http.NewRequestWithContext` for uploads, and set the
representation's `Content-Type` yourself. For small JSON payloads,
`json.Marshal` plus `bytes.NewReader` works with standard Go request-body
replay. Retry needs `GetBody` or a complete body recorded within its replay
bound; see [resilience](resilience.md).

For SSE, downloads and long polling, use `client.Do`, set the appropriate
`Accept` header, process the body incrementally, and cancel/close when the
consumer stops. Leave cache and hedging off for an unbounded stream and
manage reconnection at the application layer.

## Timeouts and cancellation

An ordinary `http.Client` keeps the timeout you configure. Request-context
deadlines and cancellation propagate through the transports, including
retry/backoff waits and rate-limit admission. Choose a total budget that
includes connection setup, redirects, attempts and response-body reads.

The optional `NewClient` and `NewStandard` builders default to a 30-second
overall timeout. `NewBaseTransport` separately bounds dialing, TLS
handshakes and response headers. `WithTimeout(0)` disables the builder's
client-wide timeout, not those transport bounds. A long poll may also need
`WithBaseTransport(WithResponseHeaderTimeout(...))`. When wrapping your own
base transport, its connection settings remain in effect.

## Optional typed calls

`Get[T]`, `Post[T]`, `Put[T]` and `Patch[T]` create requests with your context.
`Do[T](client, req)` accepts a request you constructed when you need custom
headers, a method, an idempotency key or a different request codec.
`Delete` returns an undecoded `*http.Response`. These helpers accept an
ordinary `*http.Client`, including one configured with your own transports.

They decode a 2xx representation using its `Content-Type`; JSON and XML are
built in. Non-2xx responses return zero `T`, the response, and nil error.
HEAD, 204 and 205 return zero `T` without decoding. A missing content type
also leaves the body undecoded; an unregistered type returns `ErrNoCodec`.
Check both the Go error and the HTTP status.

| Result | What the caller owns |
|---|---|
| Successful typed decode | A response whose body contains the decoded bytes; close it |
| Non-2xx or a bodyless success | The live response body; inspect or close it |
| Codec/decode error with a non-nil response | The response body remains available; close it |
| `ErrResponseTooLarge` | The body reads from the beginning, including the unread remainder; close or deliberately stream it |
| Underlying body read error | The helper closes the failed body and reports the read error |

Check `resp != nil` and arrange its close before returning a decoding error.
`WithMaxResponseBytes(n)` bounds the decoded/decompressed bytes a typed
helper reads (10 MiB by default). It does not bound raw `client.Do` reads.
Typed helpers buffer a complete value; they are not streaming decoders.

`Post`, `Put` and `Patch` encode ordinary values as JSON. An `io.Reader` is
used directly, without changing its encoding. `EncodeRequest(req, codec,
value)` replaces and closes the old request body, sets its content type and
length, and installs `GetBody` from the same bytes so retries and 307/308
redirects replay the new payload. Encoding is in-memory; use a reader for
large uploads.

For protobuf responses use a pointer type, for example `Do[*pb.Message]`.
The helper allocates the pointee before handing it to the codec. See the
[protobuf example](../codec/proto/README.md).

## Error inspection

Use `errors.Is(err, httpx.ErrResponseTooLarge)` and the other
[sentinels](reference.md#errors), or `errors.As` for an underlying network
error. Logging snapshots redact error details; the caller's original error
chain is preserved. Use the returned error for control flow and the logging
hook for records with the [documented redaction](security-observability.md#logging-and-redaction).
A successful HTTP status can still contain an application-level error that
your code must interpret.

An error while reading, decompressing or decoding a body can occur after
`client.Do` succeeds. Handle that error and close the body; the retry layer
does not restart a response once body consumption has begun. See
[failure handling and failover](resilience.md#failure-handling-and-failover)
for retry exhaustion, local admission errors and fallback behavior.
