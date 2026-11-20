# parse/parsehttp

Reads list requests from either transport, and writes parse's request errors as HTTP responses for servers without an error envelope of their own: the status the error's code declares and a JSON body of code, message and details.

## Quick Start

```go
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if parsehttp.WriteError(w, err) {
		return
	}
	s.log.ErrorContext(r.Context(), "request failed", slog.Any("error", err))
	http.Error(w, "internal error", http.StatusInternalServerError)
}
```

## API Reference

- `WriteError(w http.ResponseWriter, err error) bool` — answers err when a `*core.ParseError` is in its chain, with `ParseError.ErrorHTTPStatus()` and `Body`; reports whether it did. Any other error is the caller's.
- `Body` — `{"code", "message", "details"}`, the shape the TypeScript SDK reads.
- `ParseList[T](r *http.Request, res *parse.Resource[T]) (*parse.ListRequest, error)` — a list from the query string of a `GET` or `HEAD`, or the `application/json` body of a `POST` ([the same request as a POST body](../docs/http-errors.md#the-same-request-as-a-post-body)). A POST's query string may carry only the endpoint's own parameters (`AcceptParams`); anything else, another content type or another method is E2067. It is `Resource.ParseListPost` over an `*http.Request`.

A parse error describes the request, never the database, so its message is safe to return; everything else is left to the caller to log and answer without internals.

## Key Files

| File | Responsibility |
|------|----------------|
| `parsehttp.go` | `WriteError`, `Body` |
| `list.go` | `ParseList`: GET query strings and POST bodies |
| `parsehttp_test.go` | Status and body of a wrapped parse error; other errors left alone |

## Dependencies

- *Imports*: `parse`, `parse/core`, `parse/filter`, standard library
- *Imported by*: `parse/examples/server`
- *Package boundary*: standalone Parse module

## References

- [Package overview](../README.md) · [HTTP and errors](../docs/http-errors.md)
