# HTTP contract and errors

[Package overview](../README.md)

## Request parameters

A list endpoint built on this package reads the `filter.Param*` query parameters; `forge/fibermw/httputil`, `forge/feature` and the TypeScript SDK use the same names, and `QueryParamParser` skips them. Every other parameter naming a filterable field is a flat filter (`status=active&price_gte=10`); `Resource.ParseRequestFilter` ANDs those with the JSON `filter` parameter.

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
| `fields` | `ParamFields` | Response field mask |
| `include` | `ParamInclude` | Relation fields whose related IDs the page carries in `_relations` |

Lists answer with `filter.Page[T]`: `{"items": […], "page_info": {"has_next_page", "has_prev_page", "start_cursor", "end_cursor", "total_count"}, "_relations": {…}}`. Errors answer `{"code", "message", "details"}`.

### The same request as a POST body

A filter document travels in the URL, and every proxy, CDN and load balancer in front of the server caps URL length, commonly at 8 or 16 KB, below what a filter may hold once an endpoint raises `MaxArraySize`. A list endpoint therefore also accepts the same request as one JSON body on `POST` to the same path: the parameters above as members, `filter` as the filter document itself (an object), `page_size` as a number, the flags as booleans, and `facets`, `include` and `fields` as arrays of names.

```json
{"filter": {"status": "active", "id": {"$in": ["…", "…"]}}, "sort": "-price,name", "page_size": 50, "include_total": true}
```

`Resource.ParseListBody` validates it as `ParseList` validates a query string, with the same limits and codes. An unknown member, or one of the wrong JSON type, is E2067; a member named twice is E2086. The `filter` member is bounded by `MaxInputSize`, as the `filter` parameter is, and the whole body by `MaxListBodySize` ([`core.WithMaxListBodySize`](schema-configuration.md#parser-and-generator-options)): by default `MaxInputSize` plus 16 KiB for the other members, or 1 MiB when `MaxInputSize` is unlimited. A body over it is E2063, read no further. Flat field filters don't apply to a body. `parsehttp.ParseList(r, res)` serves both transports from one handler: the query string of a `GET` or `HEAD`, or the `application/json` body of a `POST`, whose query string may carry only the parameters the endpoint reads itself (`AcceptParams`). A framework that hands its handlers a request's parts rather than an `*http.Request` calls `Resource.ParseListPost(query, contentType, body)`, which applies the same rules. `client.ListParams.Body` encodes the document from Go.

The `POST` is a separate route a server opts into: register `POST` on the collection path for the list handler, which reads it through `parsehttp.ParseList` or `Resource.ParseListPost`. A server that also creates through `POST` on that path needs another path for lists. The TypeScript SDK sends every list as a `GET` until a client sets `maxURLLength` (lists whose URL would exceed it are sent as a `POST`) or a call passes `{ method: "POST" }` ([large filters](../sdk/ts/README.md#large-filters)); enable either only once the server routes POST lists, or it answers 405 or hands the list to its create handler. Only `list` and `listAll` have the body form: facets (`OPTIONS`) and a patch's `id[]` keys stay in the URL.

## Handler responsibilities

`ParseRequestFilter(r.URL.Query())` ANDs the JSON `filter` with flat field parameters and applies limits to the combined filter. It does not parse every list option or execute a query. Your handler validates page size/booleans, selects policy from authenticated context, passes sort/search/cursor options, and executes the plan. `fields` and `include_deleted` are conventions the service implements, not automatic projection or authorization controls.

A flat parameter naming no filterable field is E2071 unless it is a list parameter (`Param*`) or one the handler reads itself: pass those names with `filter.QueryParamAccept(names...)` (to `ParseRequestFilter` or `filter.NewQueryParamParser`) or `parse.AcceptParams` (to `Resource.ParseList`). `filter.QueryParamLenient()` ignores unknown names instead; a misspelt filter then returns every row, so prefer the accepted list. JSON filter fields are schema-checked either way.

## Schema routes

After registering resource types, mount the registry using standard `net/http` routes:

```go
mux.HandleFunc("GET /api/{resource}/schema", registry.SchemaHandler())
mux.HandleFunc("GET /api/schemas", registry.SchemaListHandler())
```

The schema document advertises each resource's filterable fields and their operators, its patch paths, sort fields and search; [`testdata/schema_document.json`](../testdata/schema_document.json) is one with every member set. It is a versioned format: `version` is `parse.SchemaVersion`, which stays the same when a member is added and increments when one is removed, renamed or redefined. A client refuses a version it does not read, as the TypeScript SDK does with `SchemaVersionError`. `SchemaHandler` returns 404/E2078 for an unknown resource. Registration and discovery are separate from your list/patch routes; the [example server](../examples/server/server.go) shows both.

## Error handling

Use `errors.As` to extract `*core.ParseError` through wrapping. Its `ErrorHTTPStatus()` provides the status; expose `Code`, `Message`, and appropriate `Details` in the error envelope. Treat unrelated database/internal errors through your normal server error handling rather than labeling every failure a bad request. Do not leak underlying SQL or credentials through error details.

Patch validation can return joined parse errors. `errors.As` returns the first; applications needing all failures traverse the joined errors. Declared enum/bounds failures expose `core.ConstraintError{Constraint, Limit}` through each E2077's cause; [patch validation](patches.md#validation) defines the vocabulary and ordering. The application still owns its HTTP envelope and any translation of these typed causes into client details.

`errors.Is(err, filter.ECodeUnknownField)` matches a specific code through wrapping. `parse.Codes()` returns the distinct `parse.Code` values; each provides its wire string, default message and HTTP status. The package imports no application error catalog. Consumers can map these values when registering their own catalog.

## Error codes

All known codes describe permanent failures and are listed below; retry and catalog classification belong to the consumer. A `*core.ParseError` answers its code's status itself (`ErrorHTTPStatus()`), so a handler writes `{code, message, details}` with the status below and needs no table of its own.

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
| E2080 | 422 | SQL generation failed |
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
| E2118 | 422 | Relation patch value must be an array |
| E2119 | 422 | Relation patch exceeds its maximum |

`parse.ParseError` and `core.ParseError` name the same type. `ErrorCode()` and `ErrorHTTPStatus()` support structural error introspection without a framework dependency; `errors.Is(err, filter.ECodeUnknownField)` holds through wrapping, and the original cause remains reachable.

See [the TypeScript client](../sdk/ts/README.md#errors) for `ApiError` and client-side schema-validation errors.
