# parse/parsehttp

Reads list requests from either transport, and writes parse's request errors as HTTP responses for servers without an error envelope of their own: the status the error's code declares and a JSON body of code, message and details.

## Quick Start

```go
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	report := parsehttp.WithErrorHandler(func(err error) {
		s.log.WarnContext(r.Context(), "writing parse error", slog.Any("error", err))
	})
	if parsehttp.WriteError(w, err, report) {
		return
	}
	s.log.ErrorContext(r.Context(), "request failed", slog.Any("error", err))
	http.Error(w, "internal error", http.StatusInternalServerError)
}
```

## API Reference

- `WriteError(w http.ResponseWriter, err error, opts ...Option) bool` — answers err when a `*core.ParseError` is in its chain, with `ParseError.ErrorHTTPStatus()` and `Body`, encoded before anything is written and sent with its `Content-Length`; reports whether it did. Any other error is the caller's. `Details` that cannot be encoded (a `NaN`, a channel, a value whose `MarshalJSON` fails) are left out, so the client still reads the code and message.
- `WithErrorHandler(fn func(error)) Option` — receives what `WriteError` could not put in the response: unencodable `Details`, as an error matching `ErrUnencodableDetails` that wraps the encoding error, and a `Write` that fails once the status is sent. net/http buffers a small response and sends it after the handler returns, so a client that disconnects in that window is not reported. Without it, both are dropped.
- `ErrUnencodableDetails` — the sentinel for `Details` `WriteError` left out.
- `Body` — `{"code", "message", "details"}`, the shape the TypeScript SDK reads.
- `ParseList[T](r *http.Request, res *parse.Resource[T]) (*parse.ListRequest, error)` — a list from the query string of a `GET` or `HEAD`, or the `application/json` body of a `POST` ([the same request as a POST body](../docs/http-errors.md#the-same-request-as-a-post-body)). A POST's query string may carry only the endpoint's own parameters (`AcceptParams`); anything else, another content type or another method is E2067. The body is read no further than the resource's list body limit (`core.WithMaxListBodySize`, by default `MaxInputSize` plus 16 KiB); a longer one is E2063. It is `Resource.ParseListPost` over an `*http.Request`.

A parse error describes the request, never the database, so its message is safe to return; everything else is left to the caller to log and answer without internals.

## Key Files

| File | Responsibility |
|------|----------------|
| `parsehttp.go` | `WriteError`, `Body`, `WithErrorHandler`, `ErrUnencodableDetails` |
| `list.go` | `ParseList`: GET query strings and POST bodies |
| `parsehttp_test.go` | `WriteError` (unit): a wrapped parse error answered with its code's status, `Body` and `Content-Length`; unencodable `Details` left out and reported; a failed `Write` reported; any other error, or nil, left to the caller with nothing written |
| `list_test.go` | `ParseList` (unit): GET, HEAD and POST read the same request, with `AcceptParams` names left in a POST's query and `application/json` media type parameters accepted; a POST's list parameters or field filters in the query, another or a missing content type, and another method refused as E2067 |

## Dependencies

- *Imports*: `parse`, `parse/core`, `parse/filter`, `parse/internal/httpjson`, standard library
- *Imported by*: `parse/examples/server`
- *Package boundary*: standalone Parse module

## References

- [Package overview](../README.md) · [HTTP and errors](../docs/http-errors.md)
