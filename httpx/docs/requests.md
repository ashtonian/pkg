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

They decode a 2xx representation with the codec for its media type; JSON
and XML are built in. Media types match as HTTP defines them: type and
subtype case-insensitively, parameters ignored, so `Application/JSON;
charset=utf-8` decodes as JSON. By default, non-2xx responses return zero `T`,
the response, and nil error. HEAD, 204 and 205 return zero `T` without
decoding. A missing content type also leaves the body undecoded. An
unregistered type, or a `Content-Type` that `mime.ParseMediaType` rejects
(malformed or conflicting parameters included), returns `ErrNoCodec` with the
body unread. Check both the Go error and the HTTP status.

| Result | What the caller owns |
|---|---|
| Successful typed decode | A response whose body contains the decoded bytes; close it |
| Non-2xx or a bodyless success | The live response body; inspect or close it |
| Codec/decode error with a non-nil response | The response body remains available; close it |
| `ErrResponseTooLarge` | The body reads from the beginning, including the unread remainder; close or deliberately stream it |
| Body read error after the headers arrived | The response, with its status and headers, and the read error unchanged for `errors.Is`. The helper has closed the failed body, releasing its connection; `resp.Body` replays the bytes received and then fails with the same error, and closing it is a no-op |
| Error before any response | A nil response |

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

A typed helper decodes `T` through a `*T`, exactly as
`DecodeResponse(resp, codec, &v)` decodes into `var v T`, so both paths give
the same value. For a pointer `T` the codec decides what it holds: JSON
`null` leaves it nil, so an optional response is distinguishable from a
zero-valued object, which yields a non-nil pointer. The protobuf codec
allocates the message, so protobuf responses use a pointer type, for example
`Do[*pb.Message]`; see the [protobuf example](../codec/proto/README.md). A
custom codec receives a pointer to the destination, as `json.Unmarshal` does.

### Choose the response codecs

When the request has no `Accept` header, a typed helper sends a copy of it
that lists the media types it can decode; the request you passed is never
modified, so reusing it with other options negotiates afresh. An explicit
`Accept` is sent unchanged.

By default the helpers can decode every registered codec, and importing an
optional codec module advertises its media type on every typed request in
the process. An origin that prefers that format then answers in it, even to
an endpoint whose destination type cannot decode it: a plain Go DTO cannot
decode protobuf. Select the codecs one call or endpoint uses instead:

```go
jsonOnly := httpx.WithResponseCodecs(httpx.JSON)
item, resp, err := httpx.Get[Item](ctx, client, url, jsonOnly)
```

The selection replaces the registry for that call: `Accept` lists its media
types in the order given, and a successful response in any other media type
returns `ErrNoCodec`, even when a codec is registered for it. An explicit
`Accept` header is still sent unchanged, and the selection still decides what
decodes. Repeated `WithResponseCodecs` options add to the selection; an empty
selection, a nil codec or one media type named twice (in any spelling) is
`ErrInvalidConfig`, returned before anything is sent. An option value holds no
per-call state, so keep one per endpoint and share it across goroutines. The
global registry is unchanged, so other endpoints keep import-based discovery.

## Error inspection

### Problem details

Pass `WithProblemErrors()` to a typed helper to decode non-2xx
`application/problem+json` responses into `*httpx.Problem` errors:

```go
value, resp, err := httpx.Get[Item](ctx, client, endpoint,
    httpx.WithProblemErrors(), httpx.WithMaxResponseBytes(1<<20))
if resp != nil {
    defer resp.Body.Close()
}
var problem *httpx.Problem
if errors.As(err, &problem) {
    var code string
    if decodeErr := problem.Extension("code", &code); decodeErr != nil {
        return decodeErr
    }
    // Classify using the application's code and resp.StatusCode.
}
```

`Problem` exposes the RFC 9457 `Type`, `Title`, `Status`, `Detail` and
`Instance` members. Unknown members remain `json.RawMessage` values in
`Extensions`, preserving numeric precision and application-specific schemas.
`Extension(name, target)` explicitly decodes one; a missing member returns
`ErrProblemExtensionNotFound`. Standard members with incompatible types are
ignored. Missing `type` defaults to `about:blank`. HTTP status codes in `Status`
are advisory; always use `resp.StatusCode` for the transport outcome. Relative
type and instance URIs resolve against `resp.Request.URL` when available.
Nothing fetches those URIs.

`DecodeProblem(resp, opts...)` also works with ordinary `client.Do` responses
and decodes regardless of status. The default media type is
`application/problem+json`, including parameters. Accept a known vendor JSON
type or an API's `application/json` problem format explicitly with
`WithProblemMediaTypes(...)`; this option alone does not enable typed errors.
Unselected media types return `ErrNotProblem` without reading or closing the
body. With `WithProblemErrors`, those responses retain the default nil-error
behavior; HEAD responses are also left alone. Inspect their status and body.

Both paths use the same bounded read and body-retention rules as
`DecodeResponse`. A malformed or oversized document returns a decode error,
not a partially decoded problem; the response remains available. With typed
problem errors enabled, the automatic `Accept` lists the decodable media
types, then `application/problem+json` and any `WithProblemMediaTypes`, each
once; an explicit `Accept` is preserved. Problem media types match
case-insensitively, like response codecs. Server text is untrusted.
`Problem.Error()` omits detail, instance and extensions, but its title or type
can still contain private data; choose safe fields for application logs.

### Transport and decode errors

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
