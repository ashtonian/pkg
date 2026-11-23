# parse/parsehttp

Parse's `net/http` adapter. It reads list requests from either transport, serves the schema discovery routes, and answers parse's request errors and a handler's own JSON. Every response is encoded whole before anything is written and sent with its `Content-Length`, so a value that cannot be encoded is still answered with a status of its own. One `WithErrorHandler` hook receives, with the request, whatever a response could not carry.

## Quick Start

```go
report := parsehttp.WithErrorHandler(func(r *http.Request, err error) {
	level := slog.LevelWarn // a response that could not be written
	if errors.Is(err, parsehttp.ErrUnencodableDetails) || errors.Is(err, parse.ECodeSchemaEncodingFailed) {
		level = slog.LevelError // details or a schema this server cannot encode: a defect
	}
	logger.Log(r.Context(), level, "writing response", slog.String("path", r.URL.Path), slog.Any("error", err))
})
mux.HandleFunc("GET /api/{resource}/schema", parsehttp.SchemaHandler(registry, report))
mux.HandleFunc("GET /api/schemas", parsehttp.SchemaListHandler(registry, report))

func (s *server) list(w http.ResponseWriter, r *http.Request) {
	req, err := parsehttp.ParseList(r, s.devices)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// … BuildList, run the plan …
	if err := parsehttp.WriteJSON(w, r, http.StatusOK, page, report); err != nil {
		s.fail(w, r, err)
	}
}

func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if parsehttp.WriteError(w, r, err, report) {
		return
	}
	logger.ErrorContext(r.Context(), "request failed", slog.Any("error", err))
	_ = parsehttp.WriteJSON(w, r, http.StatusInternalServerError, parsehttp.Body{Code: "E5001", Message: "internal error"}, report)
}
```

## Configuration

`parsehttp` reads no environment and holds no state. Each entry point takes `...Option`:

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithErrorHandler(fn)` | `func(*http.Request, error)` | none: failures are dropped, the responses are the same | Receives what a response could not carry: a parse error's `Details` entries that cannot be encoded (matching `ErrUnencodableDetails`, naming each entry and wrapping its encoding error), a schema document that cannot be encoded (matching `parse.ECodeSchemaEncodingFailed`), and a `Write` that fails once the status is sent. It runs on the request's goroutine and must be safe for concurrent use |

The limits a request is read under are the resource's (`core.WithMaxInputSize`, `core.WithMaxListBodySize`; [parser options](../docs/schema-configuration.md#parser-and-generator-options)).

## API Reference

- `ParseList[T](r *http.Request, res *parse.Resource[T]) (*parse.ListRequest, error)` — a list from the query string of a `GET` or `HEAD`, or the `application/json` body of a `POST` ([the same request as a POST body](../docs/http-errors.md#the-same-request-as-a-post-body)). The query string is decoded by `Query`. A POST's query string may carry only the endpoint's own parameters (`AcceptParams`); anything else, another content type or another method is E2067. The body is read no further than the resource's list body limit (`core.WithMaxListBodySize`, by default `MaxInputSize` plus 16 KiB); a longer one is E2063. It is `Resource.ParseList` or `Resource.ParseListPost` over an `*http.Request`.
- `Query(r *http.Request) (url.Values, error)` — the query string, decoded whole or refused whole as E2067 when any pair does not decode (an invalid percent-escape, a `;`). `r.URL.Query()` drops such a pair and keeps the rest, so a filter the request named would vanish; pass `Query`'s values to `ParseList`, `ParseRequestFilter` and any handler that reads its own parameters.
- `SchemaHandler(reg *parse.SchemaRegistry, opts ...Option) http.HandlerFunc` — the schema document of the resource the `{resource}` path value names; 404/E2078 with `details.resource` for a resource `reg` has not registered; `{"resources": [...]}`, the registered names, on a route without `{resource}`.
- `SchemaListHandler(reg *parse.SchemaRegistry, opts ...Option) http.HandlerFunc` — `{"schemas": [...]}`, every registered resource's document.
- `WriteError(w http.ResponseWriter, r *http.Request, err error, opts ...Option) bool` — answers err when a `*core.ParseError` is in its chain, with `ParseError.ErrorHTTPStatus()` and `Body`, and reports whether it did. Any other error is the caller's, with nothing written. `Details` entries that cannot be encoded (a `NaN`, a channel, a value whose `MarshalJSON` fails) are left out, so the client still reads the code, the message and the entries that encode.
- `WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any, opts ...Option) error` — answers a handler's own value. A `v` that cannot be encoded is returned with nothing written, for the caller to answer.
- `Body` — `{"code", "message", "path", "index", "details"}`; `path` is the JSON Pointer of the patch member a patch error is about, `index` the JSON Patch operation (from 0), and each of the three is written only when set. A `*core.ParseError` marshals to the same members; it is the body the TypeScript SDK's `ErrorBody` reads.
- `ErrUnencodableDetails` — the sentinel for `Details` entries `WriteError` left out.

## External Dependencies

None at runtime: `net/http` and the parse module.

## Build, Run, Test

```sh
make test-parse-unit   # includes go test -tags=unit ./parse/parsehttp
```

The tests are unit tests over `httptest`; they need no database.

## Operations

- **What reaches the hook.** `ErrUnencodableDetails` or `parse.ECodeSchemaEncodingFailed` is a defect in this server (a `Details` value or a document type that cannot be encoded): alert on it. A failed `Write` means the client left or a middleware writer failed; the status was already sent, so nothing changed for the client. Log it at `Warn`.
- **Reporting a failed write is best effort.** net/http buffers a response of up to about 6 KiB and sends it after the handler returns, so a client that disconnects in that window is not reported.
- **`Content-Length` is always set.** Middleware that rewrites the body, such as compression, must drop that header, as it must for `http.ServeContent`.
- **A 500/E2120 from a schema route** means a schema document did not encode. Every member parse builds encodes, so check for a `CustomizeSchemas` hook or a custom type in a schema that cannot; the hook's error names the type.

## Security

A `*core.ParseError` describes the request, never the database, so `WriteError` returns its message to the client. Only a request fault is one: a policy, scope, tenant or computed expression the server supplied and cannot build is a plain error, so `WriteError` never tells a client its valid request was invalid or shows it the server's policy ([HTTP errors](../docs/http-errors.md#error-handling)). Everything else is left to the caller, which logs it and answers without internals. `WriteJSON` writes exactly the value it is given. Nothing here authenticates or authorizes; `parse.Scope` carries what the server decides.

## Code Map

| File | Responsibility |
|------|----------------|
| `parsehttp.go` | `Body`, `Option`, `WithErrorHandler`, `WriteError`, `WriteJSON`, `ErrUnencodableDetails` |
| `schema.go` | `SchemaHandler`, `SchemaListHandler`: discovery routes, 500/E2120 for a document that cannot be encoded |
| `list.go` | `ParseList`: GET query strings and POST bodies; `Query`: the query string decoded whole or refused |
| `parsehttp_test.go` | `WriteError` (unit): a wrapped parse error answered with its code's status, `Body` and `Content-Length`; a patch error's `path` and `index`; a marshalled `ParseError` writing the same members; unencodable `Details` entries left out, the rest kept, and reported; a failed `Write` reported with its request; any other error, or nil, left to the caller with nothing written. `WriteJSON`: the body and its length, a value that cannot be encoded returned with nothing written, a failed `Write` reported |
| `schema_test.go` | The schema routes (unit): a document, 404/E2078, the names list, every document; a failed `Write` reported as itself and not as E2120; no hook; a document that cannot be encoded answered 500/E2120 with its cause reported |
| `codes_test.go` | The catalog (unit): every code in `parse.Codes()` raised by a request and answered with its code and the table's status; `filter.Codes()`, `patch.Codes()` and the discovery codes are the table |
| `list_test.go` | `ParseList` (unit): GET, HEAD and POST read the same request, with `AcceptParams` names left in a POST's query and `application/json` media type parameters accepted; a POST's list parameters or field filters in the query, another or a missing content type, and another method refused as E2067; a query string that does not decode refused whole as E2067 on every method, and by `Query` |

## Dependencies

- *Imports*: `parse`, `parse/core`, `parse/filter`, `parse/internal/httpjson`, standard library
- *Imported by*: `parse/examples/server`, the `parse/parsepgx` recipe
- *Package boundary*: standalone Parse module

## References

- [Package overview](../README.md) · [HTTP and errors](../docs/http-errors.md)
