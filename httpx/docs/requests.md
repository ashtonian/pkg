# Requests and responses

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

## Typed calls

`Get[T]`, `Post[T]`, `Put[T]` and `Patch[T]` create requests with your context.
`Do[T](client, req)` accepts a request you constructed when you need custom
headers, a method, an idempotency key or a different request codec.
`Delete` returns an undecoded `*http.Response`.

The helpers decode a 2xx representation using its `Content-Type`; JSON and
XML are built in. Non-2xx responses return zero `T`, the response, and nil
error. HEAD, 204 and 205 return zero `T` without decoding, even when the
server supplies a content type. A missing content type also leaves the body
undecoded; an unregistered content type returns `ErrNoCodec`.

Always check both the Go error and HTTP status. A 404 or 500 is not a
transport error, and a successful HTTP response can still contain a
business-level error. See the [tested quick start](../examples/quickstart/main.go).

## Bodies and ownership

| Result | What the caller owns |
|---|---|
| Successful typed decode | A response whose body contains the decoded bytes; close it |
| Non-2xx or a bodyless success | The live response body; inspect or close it |
| Codec/decode error with a non-nil response | The response body remains available; close it |
| `ErrResponseTooLarge` | The response body reads from the beginning, including the unread remainder; close or deliberately stream it |
| Underlying body read error | The helper closes the failed body and reports the read error |
| Raw `client.Do` | The streaming body; read to EOF when appropriate and always close it |

Check `resp != nil` and arrange its close before returning a decoding error.
Closing promptly also releases the concurrency limiter's slot and a hedge
winner's context. Do not drain an unbounded or attacker-controlled stream
just to reuse a connection; closing it is preferable when you abandon it.

`WithMaxResponseBytes(n)` bounds the **decoded/decompressed** bytes a typed
helper reads (10 MiB by default). For your own consumer, use
`ReadAtMost(resp.Body, limit)` when you require an entire bounded value.
It returns `ErrResponseTooLarge` rather than treating a prefix as a whole.
These limits do not limit raw `client.Do` reads for you.

## Sending JSON, other codecs and streams

`Post`, `Put` and `Patch` encode ordinary values as JSON. An `io.Reader` is
used directly, without changing its encoding. Construct a request yourself
when that stream needs a `Content-Type` or other headers.

`EncodeRequest(req, codec, value)` replaces and closes the old request body,
sets its content type and length, and installs `GetBody` from the same bytes.
Retries and 307/308 redirects therefore replay exactly the newly encoded
payload. Encoding is in-memory; use a reader for very large uploads.

For protobuf responses use a pointer type, for example `Do[*pb.Message]`.
The helper allocates the pointee before handing it to the codec. See the
[protobuf example](../codec/proto/README.md).

## Timeouts and cancellation

`NewClient` and `NewStandard` default to a 30-second overall timeout, which
covers connection setup, redirects, retries, waiting for admission and body
reads. A shorter request-context deadline wins. Pass the caller's context
through `http.NewRequestWithContext` or a typed helper.

The transport separately bounds dialing, TLS handshakes and response
headers. `WithTimeout(0)` disables the client-wide timeout, not those
transport bounds. A long poll that waits before sending headers may also
need `WithBaseTransport(WithResponseHeaderTimeout(...))`.

For SSE/downloads use `client.Do`, set the appropriate `Accept` header,
parse incrementally, and cancel/close the response when the consumer stops.
`httpx.Get[T]` buffers a complete value and is not a streaming decoder.

## Error inspection

Use `errors.Is(err, httpx.ErrResponseTooLarge)` and the other
[sentinels](reference.md#errors), or `errors.As` for an underlying network
error. Logging snapshots intentionally redact error details; the caller's
original error chain is preserved. Applications should use the returned
error for control flow and the logging hook for safe operational records.
