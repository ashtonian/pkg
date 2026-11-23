# parse/parsehttp

Parse's `net/http` adapter. It reads list requests from either transport, routes and reads the key of one row's item route, serves the schema discovery routes, and answers a list page under its field mask, parse's request errors and a handler's own JSON. Every response is encoded whole before anything is written and sent with its `Content-Length`, so a value that cannot be encoded is still answered with a status of its own. One `WithErrorHandler` hook receives, with the request, whatever a response could not carry.

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
	if err := parsehttp.WriteList(w, r, req, page, report); err != nil {
		s.fail(w, r, err)
	}
}

// fail reads why work stopped from the request's context, which carries the
// request's deadline: a request that ran out of time, or whose client left,
// is no server fault.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if parsehttp.WriteError(w, r, err, report) {
		return
	}
	ctx := r.Context()
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		logger.DebugContext(ctx, "request cancelled", slog.Any("error", err)) // no one reads an answer
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		logger.WarnContext(ctx, "request deadline exceeded", slog.Any("error", err))
		_ = parsehttp.WriteJSON(w, r, http.StatusGatewayTimeout, parsehttp.Body{Code: "E5004", Message: "the request did not finish within its deadline"}, report)
	default:
		logger.ErrorContext(ctx, "request failed", slog.Any("error", err))
		_ = parsehttp.WriteJSON(w, r, http.StatusInternalServerError, parsehttp.Body{Code: "E5001", Message: "internal error"}, report)
	}
}
```

Put the request's deadline on its context (`r.WithContext`) before the handler runs, so every query runs under it and `fail` reads it; the [example](../examples/server) does this for every route.

## Configuration

`parsehttp` reads no environment and holds no state. Each entry point takes `...Option`:

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithErrorHandler(fn)` | `func(*http.Request, error)` | none: failures are dropped, the responses are the same | Receives what a response could not carry: a parse error's `Details` entries that cannot be encoded (matching `ErrUnencodableDetails`, naming each entry and wrapping its encoding error), a schema document that cannot be encoded (matching `parse.ECodeSchemaEncodingFailed`), and a `Write` that fails once the status is sent. It runs on the request's goroutine and must be safe for concurrent use |

The limits a request is read under are the resource's (`core.WithMaxInputSize`, `core.WithMaxListBodySize`; [parser options](../docs/schema-configuration.md#parser-and-generator-options)).

## API Reference

- `ParseList[T](r *http.Request, res *parse.Resource[T]) (*parse.ListRequest, error)` — a list from the query string of a `GET` or `HEAD`, or the `application/json` body of a `POST` ([the same request as a POST body](../docs/http-errors.md#the-same-request-as-a-post-body)). The query string is decoded by `Query`. A POST's query string may carry only the endpoint's own parameters (`AcceptParams`); anything else, another content type or another method is E2067. The body is read no further than the resource's list body limit (`core.WithMaxListBodySize`, by default `MaxInputSize` plus 16 KiB); a longer one is E2063. It is `Resource.ParseList` or `Resource.ParseListPost` over an `*http.Request`.
- `ParsePatch[T](r *http.Request, res *parse.Resource[T], opts ...parse.PatchOption) (*patch.ParsedPatch, error)` — a patch from the body of `r` in the grammar its `Content-Type` names: `application/merge-patch+json` or `application/json` a JSON Merge Patch, `application/json-patch+json` a JSON Patch; another media type, or none, is E2086. The body is read no further than the resource's `MaxInputSize`, and a longer one is E2063. It is `Resource.ParsePatchRequest` over an `*http.Request` ([reading a patch request](../docs/patches.md#reading-a-patch-request)).
- A list or patch body that could not be read to its end is a `*parse.BodyReadError` from `ParseList` and `ParsePatch`: not a `ParseError`, so `WriteError` declines it, and not a server fault. Answer it as a bad request and log it at `WARN`.
- `ItemPattern(res Keyed) (string, error)` — the `ServeMux` pattern of one row below its collection's path: a wildcard per key column, named for the column, in key order. A one-column key is one segment (`{id}`), a key of several a segment per column (`{warehouse_id}/{sku}`). Register the item route as `"PATCH /api/stock/" + pattern`; a Go client addresses it with `client.ItemPath(key...)`. A key column whose name is no wildcard name (a column mapper's may be one) is an error, as is a resource with no key column.
- `ItemKey(r *http.Request, res Keyed) (parse.Key, error)` — the key of the row an item route registered with `ItemPattern` names: each wildcard's path value, which `ServeMux` unescapes segment by segment, read by `Resource.ParseKey` as its column's text (an integer column's as decimal digits, a UUID's in any case, a timestamp as RFC 3339). This is the item URL's wire form: a segment per key value in key order, each escaped on its own, so the key `("w1", "a/b")` is `/stock/w1/a%2Fb` and its `%2F` stays in the SKU's segment; the TypeScript SDK's `patch` sends it. A text its column cannot hold is `parse.ErrKeyInvalid`: no row has that key, so answer it as not found. A key column without a value is `ErrNoKeyValue`. Both name the column. Pass the key to `parse.ByKey`.
- `Keyed` — what `ItemPattern` and `ItemKey` read: `PrimaryKeyColumns()` and `ParseKey(texts ...string)`. `*parse.Resource[T]` is one.
- `ErrNoKeyValue` — an item request whose route holds no value for a key column: a route registered without `ItemPattern`'s wildcards, or another router that matched an empty segment. Behind a `ServeMux` route registered with `ItemPattern` it never happens, so answer it as a server fault.
- `ParseFacets[T](r *http.Request, res *parse.Resource[T]) (*parse.ListRequest, error)` — a facets request from the query string, decoded by `Query`: `facets`, the filters, `search` and `include_deleted`, with a page parameter (`cursor`, `sort`, `page_size`, `include_total`, `include`, `fields`) refused as E2067. `Resource.BuildFacets` compiles it. The SDK sends it as `OPTIONS`, so the route must receive an `OPTIONS` that is not a CORS preflight; it has no body form.
- `Query(r *http.Request) (url.Values, error)` — the query string, decoded whole or refused whole as E2067 when any pair does not decode (an invalid percent-escape, a `;`). `r.URL.Query()` drops such a pair and keeps the rest, so a filter the request named would vanish; pass `Query`'s values to `ParseList`, `ParseRequestFilter` and any handler that reads its own parameters.
- `SchemaHandler(reg *parse.SchemaRegistry, opts ...Option) http.HandlerFunc` — the schema document of the resource the `{resource}` path value names; 404/E2078 with `details.resource` for a resource `reg` has not registered; `{"resources": [...]}`, the registered names, on a route without `{resource}`.
- `SchemaListHandler(reg *parse.SchemaRegistry, opts ...Option) http.HandlerFunc` — `{"schemas": [...]}`, every registered resource's document.
- `PatchAnswer[T]{Updated []T; Failed []PatchFailure}` — a patch's answer as the SDK's `patch` and `patchMany` read it: `{"updated": [...], "failed": [...]}`, `updated` written `[]` when there is no row and `failed` left out when empty. `PatchFailure{ID, Code, Error}` is one key of a batch that updated no row; its code is the application's.
- `FacetsAnswer{Facets map[string][]filter.FacetBucket}` — a facets request's answer, `{"facets": {...}}`, written `{}` when there are no counts.
- `WriteError(w http.ResponseWriter, r *http.Request, err error, opts ...Option) bool` — answers err when a `*core.ParseError` is in its chain, with `ParseError.ErrorHTTPStatus()` and `Body`, and reports whether it did. Any other error is the caller's, with nothing written. `Details` entries that cannot be encoded (a `NaN`, a channel, a value whose `MarshalJSON` fails) are left out, so the client still reads the code, the message and the entries that encode.
- `WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any, opts ...Option) error` — answers a handler's own value. A `v` that cannot be encoded is returned with nothing written, for the caller to answer.
- `WriteList[T](w http.ResponseWriter, r *http.Request, req *parse.ListRequest, page *filter.Page[T], opts ...Option) error` — answers a list with status 200 and its page under the request's [field mask](../docs/queries.md#field-mask): each item narrowed by `MaskPage` to the JSON members the mask's fields encode as (`ListRequest.FieldMembers`). Without a mask, or with a nil `req`, it is `WriteJSON` of the page, byte for byte. A page that cannot be encoded is returned with nothing written, as `WriteJSON` returns it.
- `MaskPage[T](page *filter.Page[T], names []string) (*filter.Page[json.RawMessage], error)` — the page with each item narrowed to the JSON members `names` lists, for a handler that writes the page itself. Each item is encoded as `encoding/json` encodes it (its `MarshalJSON`, its members' encoders, their tag options and HTML escaping) and keeps the members of that object `names` lists, in the order and with the bytes the encoder wrote them; a member `omitempty` left out stays out, and a name no item has keeps nothing. The page info (cursors, total), `_relations`, `_truncated` and `facets` are the page's whatever `names` lists, and a page of no items has `items` `[]`. An item that does not encode as a JSON object (a nil pointer, a scalar) is an error naming its index. The items are encoded once and scanned once, and the masked items are written over that encoding, so a masked page costs about one more pass over its items than an unmasked one: on a 100-item page of eight members, narrowed to two, about twice the time and 1.7 times the allocation of `WriteJSON` (`BenchmarkWriteList`).
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
- **A list handler that answers 500 with a masked page** got an error from `WriteList`: `parsehttp: masking list item N: it encodes as …, not an object` means the page's item `N` is a nil pointer or an item type whose `MarshalJSON` writes no object, which a field mask cannot narrow; `parsehttp: encoding list items:` is a value in the page that `encoding/json` cannot encode, which the unmasked page fails on too.
- **A masked item missing a member the request named** is a field whose JSON member differs from its name under the field-name tag: `WriteList` keeps `ListRequest.FieldMembers`, the names `encoding/json` writes, and an item type with its own `MarshalJSON` must write its members under those names.
- **`Content-Length` is always set.** Middleware that rewrites the body, such as compression, must drop that header, as it must for `http.ServeContent`.
- **A 500/E2120 from a schema route** means a schema document did not encode. Every member parse builds encodes, so check for a `CustomizeSchemas` hook or a custom type in a schema that cannot; the hook's error names the type.
- **`ErrNoKeyValue` from `ItemKey`** means the handler serves a route whose pattern lacks a key column's wildcard: check that the route is registered with `ItemPattern` of the same resource, and that the resource's key has not gained a column since. A 404 from `ServeMux` itself (a plain-text body, not JSON) on an item URL means the path has another number of segments than the key has columns; check the client sends a value per key column, each escaped on its own.

## Security

`ItemKey` reads only the route's path values, and `Resource.ParseKey` refuses any text its column cannot hold before a statement binds it; a key is always a bound parameter. A `*core.ParseError` describes the request, never the database, so `WriteError` returns its message to the client. Only a request fault is one: a policy, scope, tenant or computed expression the server supplied and cannot build is a plain error, so `WriteError` never tells a client its valid request was invalid or shows it the server's policy ([HTTP errors](../docs/http-errors.md#error-handling)). Everything else is left to the caller, which logs it and answers without internals. `WriteJSON` writes exactly the value it is given. Nothing here authenticates or authorizes; `parse.Scope` carries what the server decides.

## Code Map

| File | Responsibility |
|------|----------------|
| `parsehttp.go` | `Body`, `Option`, `WithErrorHandler`, `WriteError`, `WriteJSON`, `ErrUnencodableDetails` |
| `schema.go` | `SchemaHandler`, `SchemaListHandler`: discovery routes, 500/E2120 for a document that cannot be encoded |
| `list.go` | `ParseList`: GET query strings and POST bodies; `ParseFacets`: a facets request; `Query`: the query string decoded whole or refused |
| `patch.go` | `ParsePatch`: a patch body by its media type, bounded by `MaxInputSize` |
| `item.go` | `ItemPattern`, `ItemKey`, `Keyed`, `ErrNoKeyValue`: an item route with a path segment per key column, and the key it names |
| `answers.go` | `PatchAnswer`, `PatchFailure`, `FacetsAnswer`: the answers the SDK reads |
| `page.go` | `WriteList`, `MaskPage`: a list page under its field mask, each item narrowed to the masked members as its encoder wrote them |
| `parsehttp_test.go` | `WriteError` (unit): a wrapped parse error answered with its code's status, `Body` and `Content-Length`; a patch error's `path` and `index`; a marshalled `ParseError` writing the same members; unencodable `Details` entries left out, the rest kept, and reported; a failed `Write` reported with its request; any other error, or nil, left to the caller with nothing written. `WriteJSON`: the body and its length, a value that cannot be encoded returned with nothing written, a failed `Write` reported |
| `schema_test.go` | The schema routes (unit): a document, 404/E2078, the names list, every document; a failed `Write` reported as itself and not as E2120; no hook; a document that cannot be encoded answered 500/E2120 with its cause reported |
| `codes_test.go` | The catalog (unit): every code in `parse.Codes()` raised by a request and answered with its code and the table's status; `filter.Codes()`, `patch.Codes()` and the discovery codes are the table |
| `answers_test.go` | The answers (unit): written as the SDK reads them, no row `[]` and no count `{}`, and read back |
| `page_bench_test.go` | `BenchmarkWriteList`: a 100-item page written whole and under a two-member mask |
| `page_test.go` | `MaskPage` and `WriteList` (unit): masked members kept in the encoder's order with its wire names, quoted numbers, `MarshalJSON` text and escaping; an `omitempty` member left out; every page member but the items carried over (a reflection guard for members `Page` gains); no items `[]`; a non-object item refused naming its index; `WriteList` narrowing to the request's mask, writing an unmasked or nil request as `WriteJSON` does, returning an unencodable page with nothing written, and narrowing a resource with another field-name tag to its fields' JSON members |
| `patch_test.go` | `ParsePatch` (unit): the body read in the grammar the request's `Content-Type` names, and refused without one |
| `item_test.go` | `ItemPattern` (unit): a wildcard per key column, refused for a column that is no wildcard name or no key column; `ItemKey` through a `ServeMux` route: a segment per column each unescaped on its own (an escaped `/` stays in its segment), an integer column read from its digits, a text its column cannot hold or a missing wildcard refused naming the column, and a path of another shape not routed |
| `list_test.go` | `ParseList` (unit): GET, HEAD and POST read the same request, with `AcceptParams` names left in a POST's query and `application/json` media type parameters accepted; a POST's list parameters or field filters in the query, another or a missing content type, and another method refused as E2067; a query string that does not decode refused whole as E2067 on every method, and by `Query` |

## Dependencies

- *Imports*: `parse`, `parse/core`, `parse/filter`, `parse/patch`, `parse/internal/httpjson`, standard library
- *Imported by*: `parse/examples/server`, the `parse/parsepgx` recipe
- *Package boundary*: standalone Parse module

## References

- [Package overview](../README.md) · [HTTP and errors](../docs/http-errors.md)
