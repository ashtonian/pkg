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

Lists answer with `filter.Page[T]`: `{"items": […], "page_info": {"has_next_page", "has_prev_page", "start_cursor", "end_cursor", "total_count"}, "_relations": {…}}`. Errors answer `{"code", "message", "details"}`, or an RFC 9457 problem document ([below](#rfc-9457-problem-details)).

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

### RFC 9457 problem details

The TypeScript SDK also reads RFC 9457 problem documents (`application/problem+json`), so a server whose envelope is one keeps parse's codes and per-member failures by mapping a `*core.ParseError` onto it:

| Problem member | From |
|---|---|
| `status` | `ErrorHTTPStatus()` |
| `detail` | `Message` |
| `code` (extension) | `Code` |
| `errors` (extension) | One `{"pointer": Path, "detail": Message, "code": Code}` per joined patch failure; `Path` is the member's JSON Pointer |

`type` names the server's own problem type; `about:blank`, with `title` set to the status phrase, is valid when it has none. The SDK takes `ApiError.code` from the `code` extension, else from `type`, and `ApiError.errors` from the `errors` entries ([client errors](../sdk/ts/README.md#errors)).

`parsehttp` writes only parse's `{code, message, details}` body, for servers that have no envelope. A server that answers RFC 9457 has a problem writer for all its errors, not only parse's: authentication, not-found and internal failures share its `type` URIs, `instance` and extension members. Parse errors go through that writer with the mapping above, so a parse-specific problem writer would answer in a second, inconsistent dialect.

## Error codes

All known codes describe permanent failures and are listed below; retry and catalog classification belong to the consumer. A `*core.ParseError` answers its code's status itself (`ErrorHTTPStatus()`), so a handler writes `{code, message, details}` with the status below and needs no table of its own.

| Code | HTTP | Meaning |
|------|------|---------|
| E2061 | 422 | Filter cost exceeds `MaxCost` |
| E2062 | 422 | `$in` / `$nin` value is not an array |
| E2063 | 422 | Body exceeds `MaxInputSize` |
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
