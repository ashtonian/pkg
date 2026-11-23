# HTTP contract and errors

[Package overview](../README.md)

## Request parameters

A list endpoint built on this package reads the `filter.Param*` query parameters, the names the TypeScript SDK and `parse/client` send, and `QueryParamParser` skips them. The routes those clients call, and the parse entry points that serve each, are the SDK's [server contract](../sdk/ts/README.md#server-contract); the [example server](../examples/server/) serves them all. Every other parameter naming a filterable field is a flat filter (`status=active&price_gte=10`); `Resource.ParseRequestFilter` ANDs those with the JSON `filter` parameter.

| Parameter | Constant | Meaning |
|---|---|---|
| `filter` | `ParamFilter` | JSON filter |
| `sort` | `ParamSort` | `-price,name` |
| `cursor` | `ParamCursor` | A `page_info` cursor |
| `page_size` | `ParamPageSize` | Items per page |
| `include_total` | `ParamIncludeTotal` | Populate `page_info.total_count` |
| `include_deleted` | `ParamIncludeDeleted` | Include soft-deleted rows |
| `search` | `ParamSearch` | Free-text search |
| `facets` | `ParamFacets` | Facet fields (on `OPTIONS`) |
| `fields` | `ParamFields` | Response field mask: the fields each item returns ([field mask](queries.md#field-mask)) |
| `include` | `ParamInclude` | Relation fields whose related IDs the page carries in `_relations` |

Lists answer with `filter.Page[T]`: `{"items": […], "page_info": {"has_next_page", "has_prev_page", "start_cursor", "end_cursor", "total_count"}, "_relations": {…}}`. Errors answer `{"code", "message", "path", "index", "details"}` (`parsehttp.Body`; `path`, `index` and `details` only when set), or an RFC 9457 problem document ([below](#rfc-9457-problem-details)).

### The same request as a POST body

A filter document travels in the URL, and every proxy, CDN and load balancer in front of the server caps URL length, commonly at 8 or 16 KB, below what a filter may hold once an endpoint raises `MaxArraySize`. A list endpoint therefore also accepts the same request as one JSON body on `POST` to the same path: the parameters above as members, `filter` as the filter document itself (an object), `page_size` as a number, the flags as booleans, and `facets`, `include` and `fields` as arrays of names.

```json
{"filter": {"status": "active", "id": {"$in": ["…", "…"]}}, "sort": "-price,name", "page_size": 50, "include_total": true}
```

`Resource.ParseListBody` validates it as `ParseList` validates a query string, with the same limits and codes. An unknown member, or one of the wrong JSON type, is E2067; a member named twice is E2086. The `filter` member is bounded by `MaxInputSize`, as the `filter` parameter is, and the whole body by `MaxListBodySize` ([`core.WithMaxListBodySize`](schema-configuration.md#parser-and-generator-options)): by default `MaxInputSize` plus 16 KiB for the other members, or 1 MiB when `MaxInputSize` is unlimited. A body over it is E2063, read no further. Flat field filters don't apply to a body. `parsehttp.ParseList(r, res)` serves both transports from one handler: the query string of a `GET` or `HEAD`, or the `application/json` body of a `POST`, whose query string may carry only the parameters the endpoint reads itself (`AcceptParams`). A framework that hands its handlers a request's parts rather than an `*http.Request` calls `Resource.ParseListPost(query, contentType, body)`, which applies the same rules. `client.ListParams.Body` encodes the document from Go.

The `POST` is a separate route a server opts into: register `POST` on the collection path for the list handler, which reads it through `parsehttp.ParseList` or `Resource.ParseListPost`. A server that also creates through `POST` on that path needs another path for lists. The TypeScript SDK sends every list as a `GET` until a client sets `maxURLLength` (lists whose URL would exceed it are sent as a `POST`) or a call passes `{ method: "POST" }` ([large filters](../sdk/ts/README.md#large-filters)); enable either only once the server routes POST lists, or it answers 405 or hands the list to its create handler. Only `list` and `listAll` have the body form: facets (`OPTIONS`) and a patch's `id[]` keys stay in the URL.

## Handler responsibilities

`ParseRequestFilter(r.URL.Query())` ANDs the JSON `filter` with flat field parameters and applies limits to the combined filter. It does not parse every list option or execute a query. Your handler validates page size/booleans, selects policy from authenticated context, passes sort/search/cursor options, and executes the plan. `Resource.ParseList` checks every `fields` name, but neither `fields` nor `include_deleted` takes effect by itself: the service drops the members the mask leaves out when it writes the rows ([field mask](queries.md#field-mask)), and grants deleted rows through `Scope.IncludeDeleted`. Neither is an authorization control.

A flat parameter naming no filterable field is E2071 unless it is a list parameter (`Param*`) or one the handler reads itself: pass those names with `filter.QueryParamAccept(names...)` (to `ParseRequestFilter` or `filter.NewQueryParamParser`) or `parse.AcceptParams` (to `Resource.ParseList`). `filter.QueryParamLenient()` ignores unknown names instead; a misspelt filter then returns every row, so prefer the accepted list. JSON filter fields are schema-checked either way.

## Schema routes

`parsehttp` is parse's one `net/http` adapter: the schema routes, `WriteError`, `WriteJSON` and `ParseList` take the same options, and one `parsehttp.WithErrorHandler` hook receives, with the request, whatever any of them could not put in a response. After registering resource types, mount the registry:

```go
report := parsehttp.WithErrorHandler(func(r *http.Request, err error) {
	level := slog.LevelWarn // the response could not be written
	if errors.Is(err, parse.ECodeSchemaEncodingFailed) || errors.Is(err, parsehttp.ErrUnencodableDetails) {
		level = slog.LevelError // a document or details that cannot be encoded: a server defect
	}
	logger.Log(r.Context(), level, "writing response", slog.String("path", r.URL.Path), slog.Any("error", err))
})
mux.HandleFunc("GET /api/{resource}/schema", parsehttp.SchemaHandler(registry, report))
mux.HandleFunc("GET /api/schemas", parsehttp.SchemaListHandler(registry, report))
```

The schema document advertises each resource's filterable fields and their operators, its patch paths, sort fields and search; [`testdata/schema_document.json`](../testdata/schema_document.json) is one with every member set. It is a versioned format: `version` is `parse.SchemaVersion`, which stays the same when a member is added and increments when one is removed, renamed or redefined. A client refuses a version it does not read, as the TypeScript SDK does with `SchemaVersionError`. `parsehttp.SchemaHandler` returns 404/E2078 for an unknown resource. Registration and discovery are separate from your list/patch routes; the [example server](../examples/server/server.go) shows both.

Both handlers encode the whole response before writing any of it, and send it with its `Content-Length`. A document that cannot be encoded is answered 500/E2120 with the catalog message only; the encoding error, which names Go types, goes to the `parsehttp.WithErrorHandler` hook alone, matching `parse.ECodeSchemaEncodingFailed` through `errors.Is`. A `Write` the `ResponseWriter` fails once the status is sent (a body past net/http's buffers going to a client that left, an `http.TimeoutHandler` past its deadline, a middleware writer's own error) reaches the hook as that error; the status is already sent, so nothing changes for the client. Reporting write failures is best effort: net/http buffers a response of up to about 6 KiB and sends it after the handler returns, so a client that disconnects in that window is not reported. The hook runs on the request's goroutine and must be safe for concurrent use. Without one, the handlers answer the same way and report nothing. Every member of the schema document encodes, so E2120 means a defect in parse: alert on it, not on write errors.

## Error handling

`parsehttp.WriteError(w, r, err, report)` answers a parse error anywhere in `err`'s chain with its code's status and `parsehttp.Body`, and reports whether it did; any other error is the caller's. A patch error names the member it is about in `path`, its JSON Pointer, and, from a JSON Patch, the operation in `index`, counting from 0, so a client binds it to a form field or an operation without reading the message:

```
PATCH /api/products/p01  {"category":"nope"}
422 {"code":"E2077","message":"invalid value for path '/category': value \"nope\" is not one of: electronics, clothing, books, home","path":"/category"}
```

A `*core.ParseError` marshalled by a server itself writes the same members. It encodes the body before writing anything and sends it with its `Content-Length`; middleware that rewrites the body, such as compression, must drop that header, as it must for `http.ServeContent`. A `Details` entry JSON cannot hold, such as a `NaN` from `strconv.ParseFloat`, is left out: the client still gets the status, code, message and the entries that encode, and `parsehttp.WithErrorHandler(fn)` receives an error matching `parsehttp.ErrUnencodableDetails` that names each dropped entry and wraps its encoding error. The hook also receives a `Write` that fails once the status is sent, on the same best-effort terms as the [schema routes](#schema-routes). Without it, both are dropped.

`parsehttp.WriteJSON(w, r, status, v, report)` answers a handler's own value the same way: encoded whole before anything is written, then sent with its `Content-Length`, a failed `Write` going to the hook. A value that cannot be encoded is returned with nothing written, for the handler to answer as its own failure; streaming a `json.Encoder` into the `ResponseWriter` instead sends the status before the encoder can fail, so the client receives a 200 with a truncated body.

A `*core.ParseError` means the request is at fault, and nothing else is one. What the server supplied and cannot build is a plain error wrapping a sentinel, which `WriteError` declines so the handler logs it and answers its own 500: a policy that does not compile (`filter.ErrPolicyInvalid`: a field the resource lacks, an operator or value the field's type does not take, a negated empty group, a membership lookup that does not apply), a policy-scoped relation built without the target's policy (`filter.ErrRelatedPolicyRequired`), a tenant-scoped pair table joined without a tenant (`filter.ErrRelationTenantRequired`), a missing tenant (`parse.ErrTenantRequired`), an update scope (`parse.ErrUpdateScope`), a computed column's expression refusing the dialect, a raw SQL fragment or a hand-built AST that does not build. A request value the dialect cannot hold is the request's: an unsigned integer above SQLite's 64-bit integers, or a set no PostgreSQL integer array holds, is E2073, as a typed field whose Go kind cannot hold it is.

Use `errors.As` to extract `*core.ParseError` through wrapping. Its `ErrorHTTPStatus()` provides the status; expose `Code`, `Message`, and appropriate `Details` in the error envelope. `Details` is keyed by `core.DetailField` (`"field"`, the request field a refusal is about) and `core.DetailParameter` (`"parameter"`, the list parameter). Treat unrelated database/internal errors through your normal server error handling rather than labeling every failure a bad request. Do not leak underlying SQL or credentials through error details.

Patch validation can return joined parse errors. `errors.As` returns the first; applications needing all failures traverse the joined errors. Declared enum/bounds failures expose `patch.ConstraintError{Constraint, Limit}` through each E2077's cause; [patch validation](patches.md#validation) defines the vocabulary and ordering. The application still owns its HTTP envelope and any translation of these typed causes into client details.

`errors.Is(err, filter.ECodeUnknownField)` matches a specific code through wrapping. `parse.Codes()` returns the distinct `parse.Code` values; each provides its wire string, default message and HTTP status. Each code is defined once, as a `core.Code` constant (`core.CodeUnknownField`) holding its status and message; `filter`, `patch` and `parse` name the codes they raise as aliases of those constants, so a limit both grammars enforce, such as E2063, is one value. The package imports no application error catalog. Consumers can map these values when registering their own catalog.

### RFC 9457 problem details

The TypeScript SDK also reads RFC 9457 problem documents (`application/problem+json`), so a server whose envelope is one keeps parse's codes and per-member failures by mapping a `*core.ParseError` onto it:

| Problem member | From |
|---|---|
| `status` | `ErrorHTTPStatus()` |
| `detail` | `Message` |
| `code` (extension) | `Code` |
| `errors` (extension) | One `{"pointer": Path, "detail": Message, "code": Code}` per joined patch failure; `Path` is the member's JSON Pointer |

`type` names the server's own problem type; `about:blank`, with `title` set to the status phrase, is valid when it has none. The SDK takes `ApiError.code` from the `code` extension, else from `type`, and `ApiError.errors` from the `errors` entries ([client errors](../sdk/ts/README.md#errors)).

`parsehttp` writes only parse's own body, for servers that have no envelope. A server that answers RFC 9457 has a problem writer for all its errors, not only parse's: authentication, not-found and internal failures share its `type` URIs, `instance` and extension members. Parse errors go through that writer with the mapping above, so a parse-specific problem writer would answer in a second, inconsistent dialect.

## Error codes

All known codes describe permanent failures and are listed below; retry and catalog classification belong to the consumer. A `*core.ParseError` answers its code's status itself (`ErrorHTTPStatus()`), so a handler writes `parsehttp.Body` with the status below and needs no table of its own.

| Code | HTTP | Meaning |
|------|------|---------|
| E2061 | 422 | Filter cost exceeds `MaxCost` |
| E2062 | 422 | `$in` / `$nin` value is not an array |
| E2063 | 422 | Filter or patch body exceeds `MaxInputSize`, or a POST list body exceeds `MaxListBodySize` |
| E2064 | 422 | Patch operation not allowed on the path |
| E2065 | 422 | Sort values at a page boundary too large for a cursor |
| E2066 | 422 | Too many patch operations |
| E2067 | 422 | Malformed or unsupported list parameter |
| E2068 | 422 | `test` cannot compile to SQL |
| E2069 | 500 | Patch path has no SQL column |
| E2070 | 422 | Patch operation cannot compile to SQL (move, copy, nested path, `array_append` on a document) |
| E2071 | 422 | Unknown filter field |
| E2072 | 422 | Operator not allowed |
| E2073 | 422 | Value does not fit the field |
| E2074 | 422 | `MaxDepth` exceeded |
| E2075 | 422 | Invalid JSON Patch operation or pointer |
| E2076 | 422 | Patch path not in the schema |
| E2077 | 422 | Patch value invalid |
| E2078 | 422 | Schema not found |
| E2079 | 422 | Sort field not allowed |
| E2081 | 422 | Too many sort fields |
| E2082 | 422 | Invalid cursor |
| E2083 | 422 | Invalid relation |
| E2084 | 422 | Patch path forbidden by policy |
| E2085 | 422 | Patch value violates policy |
| E2086 | 422 | Invalid JSON — a member named twice, invalid UTF-8 or data after the document included — or malformed `$and` / `$or` / `$not` |
| E2087 | 422 | Value not in the enum |
| E2088 | 422 | Array or document exceeds `MaxArraySize` |
| E2089 | 422 | Too many filter conditions |
| E2112 | 400 | Invalid geo coordinates or box |
| E2113 | 400 | Invalid geo distance |
| E2114 | 400 | Invalid geo polygon |
| E2115 | 400 | Geo operator or shape not supported by the dialect |
| E2116 | 400 | Invalid UUID |
| E2117 | 422 | Operation not allowed on a relation field |
| E2118 | 422 | Relation patch value is not a set of keys: an `add` or `remove` naming none, or a null key (a value that is not a list, or a key of the wrong type, is E2077) |
| E2119 | 422 | Relation patch exceeds its maximum: more keys in one relation operation than `PatchSchema.MaxRelationValues`; `Details` holds `count` and `limit` |
| E2120 | 500 | A schema discovery handler could not encode its document |

`parse.ParseError` and `core.ParseError` name the same type. `ErrorCode()` and `ErrorHTTPStatus()` support structural error introspection without a framework dependency; `errors.Is(err, filter.ECodeUnknownField)` holds through wrapping, and the original cause remains reachable.

See [the TypeScript client](../sdk/ts/README.md#errors) for `ApiError` and client-side schema-validation errors.
