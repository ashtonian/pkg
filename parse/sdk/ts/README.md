# @ashtonian/parse

TypeScript client for APIs built on the Go `parse` package: type-safe filter and sort builders, schema discovery, client-side validation for the shared schema contract, cursor pagination, facets, and JSON Merge Patch updates. Its request and response shapes are the server's wire format, verified against fixtures the Go package emits.

Zero runtime dependencies. Runs wherever `fetch` and `AbortSignal.any` exist: Node.js 22.13 or later (the `engines` floor, which CI tests exactly), current browsers, Deno and edge runtimes. The package is an ES module; its exports' `default` condition also serves `require`, which loads ES modules on every Node.js the `engines` floor admits.

## Install

Install the published package:

```bash
npm install @ashtonian/parse
```

To build and package a local checkout from this directory:

```bash
pnpm install --frozen-lockfile
pnpm run build
pnpm pack
```

Install the resulting tarball in the consuming project with `npm install /path/to/tarball.tgz`, or use your workspace package setup. The import name is `@ashtonian/parse` for both registry and local installations.

## Quick Start

```typescript
import { createClient, filter } from "@ashtonian/parse";

interface Product {
  id: string;
  name: string;
  category: string;
  price: number;
  labels: Record<string, string>;
}

const client = createClient({ baseURL: "https://api.example.com/api", validateBeforeSend: true });
const products = client.resource<Product>("products");

const page = await products.list({
  filter: filter<Product>().where("category", "books").where("price", { $lt: 20 }).build(),
  sort: "-price,name",
  pageSize: 25,
  includeTotal: true,
});
console.log(page.items, page.page_info.total_count, page.page_info.end_cursor);
```

## Configuration

`createClient(cfg: ClientConfig)`:

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `baseURL` | string | — (required) | API origin and prefix (`https://api.example.com/api`); a resource's path and its schema are below it. A trailing slash is ignored |
| `headers` | `Record<string, string>` | none | Headers sent with every request (e.g. `Authorization`). Names are case-insensitive: a request's own `headers` replace them, and a `content-type` replaces the default rather than joining it |
| `fetch` | `typeof fetch` | `globalThis.fetch` | Fetch implementation |
| `schemaPrefix` | string | none: schemas beside the collections | Path below `baseURL` of the schema endpoints, for a server that serves them elsewhere: `{schemaPrefix}/{name}/schema`, `{schemaPrefix}/schemas`. Slashes at either end are ignored |
| `validateBeforeSend` | boolean | `false` | Validate filters, sorts, includes, facets, search and patches against the server schema before sending |
| `timeout` | positive integer (ms), at most 2^32 - 1 | `30000` | Per-request deadline, combined with any caller `AbortSignal`. `createClient` throws `RangeError` for any other value, `0` included |
| `maxURLLength` | positive integer | none: every list is a GET | Opts `list` and `listAll` into the [POST body transport](#large-filters): the longest list URL sent as a GET, past which the same request is sent as a JSON body in a POST to the same path. Set it only once the server routes POST lists, through Go `parsehttp.ParseList` or `Resource.ParseListPost`. `createClient` throws `RangeError` for any other value |

## Server Contract

`client.resource<T>(path, { schema? })` is a collection at `{baseURL}/{path}`. Its schema document is `{baseURL}/{schemaPrefix}/{name}/schema`, where `name` is the resource's registry name (Go: its table, the document's `resource`): `schema` when given, else the path's last segment. Relation targets are fetched by registry name too, and the schema cache is keyed by it, so `schemas()` followed by any handle's `schema()` sends nothing. A collection served under another name than its table, `/api/work-orders` over `work_orders`, is `client.resource("work-orders", { schema: "work_orders" })`. A schema document for another resource than the one requested is `SchemaResourceError`. An empty path throws `RangeError`.

The client calls the routes a server built on the Go module serves; the [example server](../../examples/server/) serves every one, with `baseURL` set to its origin and `/api`. Every public method has a row:

| Client call | Request | Served in Go by |
|---|---|---|
| `list(opts)` | `GET /{resource}?filter=&sort=&cursor=&page_size=&include_total=&include_deleted=&search=&include=&facets=&fields=` | `parsehttp.ParseList` (or `Resource.ParseList` over `parsehttp.Query`), `Resource.BuildList`, `Resource.Page` |
| `list(opts, { method: "POST" })`, or a list URL past `maxURLLength` | `POST /{resource}` with `{"filter": {…}, "sort", "cursor", "page_size", "include_total", "include_deleted", "search", "include": […], "facets": […], "fields": […]}` as `application/json` ([HTTP contract](../../docs/http-errors.md#the-same-request-as-a-post-body)) | The same handler: `parsehttp.ParseList` reads either transport |
| `listAll(opts)` | `list` repeated with `cursor = page_info.end_cursor` while `page_info.has_next_page`, each page on the transport `list` would take | The list route |
| `patch(id, input)` | `PATCH /{resource}/{id}` with a JSON Merge Patch body (`application/json`) | `parsehttp.ParsePatch` (or `Resource.ParsePatchRequest`), `Resource.BuildUpdate(…, parse.ByID(id), …)` |
| `patchMany(ids, input)` | `PATCH /{resource}?id[]=…&id[]=…` with a JSON Merge Patch body (`application/json`) | `parsehttp.Query`, `parsehttp.ParsePatch`, `Resource.BuildUpdate(…, parse.ByIDs(ids…), …)`; a key that updated no row is an item of `failed` |
| `facets(fields, opts)` | `OPTIONS /{resource}?facets=a,b&filter=` → `facets` | `parsehttp.ParseFacets` (or `Resource.ParseFacets`), `Resource.BuildFacets`; `cursor`, `sort`, `page_size`, `include_total`, `include` or `fields` is E2067 |
| `schema()` | `GET /{schemaPrefix}/{name}/schema` | `parsehttp.SchemaHandler` |
| `schemas()` | `GET /{schemaPrefix}/schemas` → `{ schemas: […] }` | `parsehttp.SchemaListHandler` |
| `validator()` | `schema()`'s request, once per handle | The schema route |
| `resource(path, opts)` | Nothing: a handle whose calls send the requests above | — |

Members a list leaves zero (an empty `sort`, `include`, `facets` or `fields`, `pageSize: 0`) are not sent, so the server applies its defaults. `fields` is the response field mask, the members each item carries; the schema document lists no maskable names, so `validateBeforeSend` leaves the mask to the server. `patch("")` and `patchMany([])` throw `RangeError` before sending: either would `PATCH` the collection itself. A request carries `Content-Type: application/json` only when it has a body.

A list page is `{ items, page_info: { has_next_page, has_prev_page, start_cursor?, end_cursor?, total_count? }, _relations?, _truncated?, facets? }` — Go `filter.Page`. `_relations` carries `include` projections: relation field → owner id → related ids (at most 100 per owner by default); `_truncated` lists, per relation field, the owners whose ids were cut at that cap. `facets` carries the counts a list's `facets` option asked for, over the matched rows: field → buckets, a terms facet's by count with ties in the field's type order (numbers by value), a range facet's as declared. A server answers them when it runs the list plan's facet statement, as `parsepgx.QueryPage` and the example server do; one that does not serves facets through `facets(fields, opts)` only. A patch returns `{ updated, failed? }`: `failed` lists the keys of a `patchMany` that updated no row, each with the server's `code` and `error`.

## Filtering

```typescript
import { and, filter, not, or } from "@ashtonian/parse";

// Gold-tier books and games titled "The …", or anything under 5.
const expr = or<Product>(
  filter<Product>()
    .where("category", { $in: ["books", "games"] })
    .where("name", { $like: "The %" })          // % any run, _ one character, \ escapes
    .where("labels", { $contains: { tier: "gold" } }), // JSON document containment
  { price: { $lt: 5 } },
);
const inRange = filter<Product>().where("price", { $gte: 1 }).where("price", { $lte: 5 }).build();
// { price: { $gte: 1, $lte: 5 } }
const notArchived = and<Product>(inRange, not<Product>({ category: "archive" }));
```

`filter<T>()` builds a conjunction of field conditions. It is immutable: each `where` returns a new builder. A second `where` on a field merges its operators into the first; an operator the field already has, or a second condition beside a bare value, throws `RangeError`, so no call drops a condition already added. `and(...)`, `or(...)` and `not(...)` take expressions or builders and return expressions, as the Go client's `And`, `Or` and `Not` do: empty expressions match every row (`and()` is `{}`, an `or` holding one is `{}`), nested groups of the same kind flatten, and `or()` of nothing and `not({})`, which would match no row, throw `RangeError`.

Operator semantics are the server's: equality and ordering are case-sensitive (strings order by code point), `$contains` is a literal substring on strings, document containment on JSON fields and element containment on arrays, `$has_key` tests a JSON document for a top-level key (jsonb `?`), declared JSON paths are fields named `document.member`, and a predicate on a missing (NULL) value never matches, so `$ne` / `$nin` / `$not` exclude it. A plain object on a JSON field is document equality.

## Facets

```typescript
const counts = await products.facets(["category"], { filter: { price: { $lt: 20 } } });
// { category: [{ key: "books", count: 89 }, …] }

const page = await products.list({ filter: { price: { $lt: 20 } }, facets: ["category"] });
// page.facets: the same counts beside every page of the list
```

`facets` sends `OPTIONS`, the method of a CORS preflight too: the server's handler must receive an `OPTIONS` that is not a preflight (one without `Access-Control-Request-Method`), so a layer in front of the application that answers every `OPTIONS` itself breaks `facets`. A facets request has no body form, so `maxURLLength` does not apply to it; counts under a filter too large for a URL come from `list` over `POST` with `facets`.

## Large filters

A filter travels in the list URL, and the proxies, CDNs and load balancers in front of a server cap URL length, commonly at 8 or 16 KB. A filter by a large set of keys passes that cap long before the server's own limits. The same list request can instead travel as a JSON body in a `POST` to the collection path, which the Go server reads with `parsehttp.ParseList` (one handler for both transports) or `Resource.ParseListPost`.

The client sends every list as a `GET` until you opt in, because a server that does not route `POST /{resource}` to its list handler answers 405, or hands the list to its create handler. Once the server routes it:

```typescript
// Every list whose URL would pass 2000 characters is sent as a POST body.
const client = createClient({ baseURL: "https://api.example.com", maxURLLength: 2000 });
const products = client.resource<Product>("products");

const page = await products.list({ filter: { id: { $in: ids } }, pageSize: 100 });

// Or choose the transport per call, whatever maxURLLength says.
const viaPost = await products.list({ filter: { id: { $in: ids } } }, { method: "POST" });
```

`listAll` takes the same option and follows cursors on either transport: a cursor from one continues on the other. Only `list` and `listAll` have the body transport. `facets` always sends its filter in the `OPTIONS` URL and `patchMany` its ids in the `PATCH` URL, past `maxURLLength` or not; the server contract has no body form for either. For facet counts under a filter too large for a URL, ask `list` for `facets` over `POST`: every page carries the counts of the whole matched population.

## Patching

`patch` sends a JSON Merge Patch (RFC 7396): a field set to a value replaces it (objects merge recursively, their `null` members delete keys), a field set to `null` removes it.

```typescript
await products.patch("prod-123", { name: "Updated", labels: { tier: "gold", legacy: null } });
await products.patchMany(["id1", "id2"], { category: "archive" });
```

## Schema Discovery & Validation

```typescript
const v = await products.validator();
v.filterableFields();       // ["category", "id", "labels", "name", "price"]
v.operatorsFor("price");    // ["eq", "ne", "gt", "gte", "lt", "lte", "in", "nin"]
v.enumValuesFor("category");
v.fieldsByGroup();          // Map of sidebar group → fields, ordered
v.includableFields();       // relation fields a list can `include`
```

The document lists every name the server's filter accepts, the resource's key included whether or not it is tagged, and for each the operators the server takes on it: a field that declares one NULL test (`null`, `not_null`, `exists`) lists all three. `filterableFields()` and `operatorsFor()` read them as listed.

The schema document carries its format version; the client reads version `SCHEMA_VERSION` and refuses any other with `SchemaVersionError`, so a document whose members changed meaning never validates a request. With `validateBeforeSend: true` the schema is fetched once per resource and every filter, sort, include, facet, search and patch is validated first, by `list` and `facets` alike; a search on a resource whose schema has no search enabled is E2067, as the server answers. A patch path is looked up as the server looks it up: its exact key, else the most specific `*` / `**` pattern that matches it. A filter's relation predicates (`$some`, `$none`, `$is`, `$is_not` on a field whose `relation.target` names another resource) validate against that resource's schema, which the client fetches the same way, two levels deep; a JSON array of objects' element filters validate against its `elements`, and a scalar array's element conditions (`{"$some": {"$gte": 5, "$lt": 10}}`, typed by `ElementCondition<E>`) against its `element`, as do its `$contains` members. `validateFilter` (with an optional `RelatedSchemas` lookup), `relatedTargets`, `validateSort` and `validatePatch` are also exported for direct use.

Concurrent calls that need a resource's schema share one request, sent with the first call's headers. It runs under the client's `timeout`, not any one call's `signal`: a call whose signal aborts rejects with the signal's reason while the others keep waiting, and the request is cancelled once every waiting call has aborted. A call with no signal always waits for the result. A failed, cancelled or refused fetch is retried by the next call; only a schema the client reads is cached.

Two Go-emitted corpora pin client to server: `testdata/filter_conformance.json` records the server's verdict and error code on filters, sort strings and list requests, and `testdata/patch_conformance.json` on merge patches, each with the schema documents the registry serves; the client validates every input against those documents and must give the same answer. A schema document without a filter, patch or sort section validates as one with no fields, as the server answers: each name is E2071, E2076 or E2079 (E2078 is the schema route's 404 for a resource the server does not register, read as an `ApiError`). Patch validation checks what the server checks per path — patchable, null only where `nullable`, the value's type, an integer within its Go kind (`number_kind`, exactly for kinds of 32 bits or fewer, and for every kind when the path's `wire` is `"string"`), the enum, the bounds (`min_length` / `max_length` in code points; `minimum` / `maximum`, exact decimal text compared exactly with an integer or decimal and at the kind's precision with a float), an array's element type and element counts (`min_items` / `max_items`) with each element's enum and bounds, a relation's list of keys and their count once repeats are removed (`max_relation_values`, E2119), and the operation count. A field or path whose schema `wire` is `"string"` (Go `json:",string"`) takes its value as a JSON string holding it (`"9007199254740993"`, never a bare number), which the client checks as the server reads it; a 64-bit integer is then compared exactly, so declare such fields `string` in generated types. Left to the server: timestamp and UUID spellings, 64-bit integer ranges of filter operands, values inside JSON documents, the `$contains` members of an array whose schema names no `element`, geo operand shapes in filters, nesting and cost bounds, and server-specific policy.

## Errors

| Error | When | Fields |
|---|---|---|
| `ApiError` | Non-2xx response | `status` (the response's), `code`, `message`, `body` (`{code, message, path?, index?, details?}`: `path` is the JSON Pointer of the patch member and `index` the JSON Patch operation a patch error is about), `problem` (the RFC 9457 document, or `undefined`), `errors` (its field errors, or `[]`) |
| `ResponseError` (an `ApiError`) | A 2xx without the JSON body the call reads: a 204, an empty 200, a proxy's HTML 200 | `status`, `url`, `code` `""`, `message` naming the status, URL and reason |
| `SchemaResourceError` | The schema endpoint answered another resource's document than the registry name requested | `requested`, `resource` |
| `SchemaValidationError` | Client-side validation failed | `code` (the E-code the server would return), `field`, `detail`, optional `constraint` / `limit` |
| `SchemaValidationErrors` | Multiple merge-patch members or values failed | `errors` (ordered individual failures); inherited `code`, `field`, `detail` and constraint metadata identify the first |
| `SchemaVersionError` | The server's schema document is in a format version this client does not read | `resource`, `version` (as served), `supported` (`SCHEMA_VERSION`) |

`validatePatch` reports every independent member/value failure in sorted member order. Declared rules set `constraint` to `enum`, `minlen`, `maxlen`, `min`, `max`, `minitems` or `maxitems`, and `limit` to the schema bound text (comma-separated enum allowlist). Type failures leave both undefined. Single failures remain `SchemaValidationError`; the aggregate extends that class, so `instanceof SchemaValidationError` and its first field/code remain useful. [Go error contract](../../docs/patches.md#validation).

`ApiError` reads two error bodies. A JSON object with string `code` and `message` is parse's error body and is kept as `body`. An RFC 9457 problem document is kept as `problem`. It is one when sent as `application/problem+json`, and otherwise only when its `type` is an absolute URI or it has a string `title` and an integer HTTP `status`; a lone `status` or a bare-word `type` belongs to other error envelopes. The problem holds `type` (resolved against the response URL, `"about:blank"` when absent or empty), `title`, `status`, `detail`, `instance` and every other member under `extensions`. A standard member of the wrong type is dropped, as RFC 9457 requires; `problem.status` is advisory and never replaces `ApiError.status`. A problem that is not also in parse's shape sets `code` from its `code` extension, else its `type` (`""` for `about:blank`), and `message` from `detail`, else `title`. `errors` holds the object entries of the problem's `errors` extension (RFC 9457 §3) as `FieldError`s, with the `pointer`, `detail` and `code` members each sends as strings; everything else stays in `problem.extensions`. Any other body (an HTML error page, an empty body, other JSON) gives `code` `""` and the status text as `message`. The client sends `Accept: application/json, application/problem+json`.

```typescript
try {
  await products.patch("prod-123", { price: -1 });
} catch (err) {
  if (!(err instanceof ApiError)) throw err;
  console.warn(err.status, err.code, err.message, err.problem?.instance);
  for (const field of err.errors) console.warn(field.pointer, field.code, field.detail);
}
```

All of it is server-supplied text; treat it as untrusted.

Validation codes match the Go parser: E2071 unknown field, E2072 operator not allowed, E2073 value type mismatch, E2062 non-array membership value, E2086 malformed `$and` / `$or` / `$not`, E2087 value outside the enum, E2116 non-string UUID, E2079 / E2081 sort field / count, E2064 / E2066 / E2076 patch operation / size / path, E2118 / E2119 a null relation key / too many relation keys, E2067 a search on a resource without one.

## Testing

```bash
pnpm install --frozen-lockfile
pnpm run check        # TypeScript 7 native build-mode check over sources and tests
pnpm run check:compat # TypeScript 6 tooling API compatibility (tsc6)
pnpm run lint         # eslint, typescript-eslint strict type-checked
pnpm test             # vitest
pnpm run build        # emits dist/
```

Development requires Node.js 22.13 or later and pnpm 12 at the version `packageManager` pins; CI installs that version. `pnpm-workspace.yaml` sets `pmOnFail: ignore`, so another local pnpm runs as itself instead of switching: pinning would make pnpm write a second YAML document into `pnpm-lock.yaml`, which GitHub's dependency graph, its SBOM export and Dependabot alerts do not read. CI runs `make test-parse-sdk`, the registry checks and the packed consumer on Node.js 22.13.0, exactly the `engines` floor, and on the newest LTS; that job fails when the floor leg's version and `engines.node` differ, so raise both together. `make test-parse-sdk`, which CI and the release workflow run, starts with `tools/check-parse-sdk-pnpm.mjs`. It fails when the pnpm on `PATH` is not the pinned release (`pnpm self-update X.Y.Z` installs it), when `pmOnFail: ignore` is missing, or when `pnpm-lock.yaml` has YAML document markers. pnpm keeps a leading `packageManagerDependencies` document once written, so delete it by hand (from the first `---` line through the second). Builds use TypeScript 7 through `@typescript/native`; typed ESLint uses the supported TypeScript 6 API through the `typescript` alias, Microsoft's `@typescript/typescript6` package, whose compiler command is `tsc6`. These are development dependencies; the published client has no runtime dependencies. The tarball includes compiled output and original library source for declaration-map navigation; test sources stay out. JavaScript maps include their source text.

From the repository root, `make parse-sdk-release-check` installs the built tarball in a fresh consumer and verifies both TypeScript compilers, runtime subpath imports and packaged sourcemaps. To verify an existing public release, run `bash tools/check-parse-sdk-release.sh --registry X.Y.Z`; it requires the SDK's development dependencies and installs that exact version from the public npm registry with a fresh pnpm store and cache, then checks that the integrity pnpm locked matches the registry's own metadata. `PARSE_SDK_DIR` selects an alternate SDK source directory, including its compiler dependencies. See [the release procedure](../../../CONTRIBUTING.md#parse-sdk-publication) for publication and authentication setup.

Three Go-emitted fixtures keep the client on the server's contract; each fails its Go test when stale:

| Fixture | Consumed by | Regenerate |
|---|---|---|
| `testdata/filter_conformance.json` — `{schemas, cases, sort, list}`: the schema documents of the corpus's resources, and filters, sort strings and list query strings with the server's verdict and E-code | `src/filter/conformance.test.ts` | `go test -tags=unit -run TestEmitFilterConformance -update ./parse/` |
| `testdata/patch_conformance.json` — merge patches with the Go parser's verdict and E-code, and the schema document they are checked against | `src/patch/conformance.test.ts` | `go test -tags=unit -run TestEmitPatchConformance -update ./parse/` |
| `testdata/sdk_wire.json` — requests parse's Go client built and the answers `parsehttp`'s handlers gave them: a keyset walk, a backward page, a total, a POST list, facets, patches, the schema routes and their 404, a 422 parse error | `src/client.test.ts`, which sends each request and must equal the recorded one | `go test -tags=unit -run TestSDKWireFixture -update ./parse/` |

## Operations

- *Deadlines*: every request runs under `timeout` (30 s by default), combined with the call's `AbortSignal`. The client does not retry; a caller that retries decides which failures are safe to repeat (a list is, a patch is not unless the server makes it so).
- *Schema cache*: `resource.schema()` fetches a resource's document once per client and caches it by registry name; concurrent calls share one request, and a failed fetch is not cached. `schemas()` fetches every document and replaces the cached ones. A server whose schemas change is read afresh by a new client or a call to `schemas()`.
- *Troubleshooting*: a `ResponseError` is a 2xx without the JSON the call reads, usually a proxy or a route that is not parse's. A `SchemaResourceError` means `schemaPrefix` or the resource's registry name points at another resource's document. A 414 from a proxy on a long filter means the list needs `maxURLLength` set, so it goes as a POST body.

## Security

Credentials go in `headers` (`Authorization`), sent with every request to `baseURL` and nowhere else. Filters, sorts and patch values are encoded as JSON and query parameters with `URLSearchParams`, so a value never changes a request's structure. `validateBeforeSend` checks a request against the server's schema before it leaves; the server validates it again whatever the client does, so client-side validation is a convenience, not a control. `ApiError.message` is the server's text: whether to show it to a user is the application's decision.

## Package Structure

| Import | Contents |
|---|---|
| `@ashtonian/parse` | `createClient`, `SchemaValidator`, `SCHEMA_VERSION`, builders and combinators, validators, all types |
| `@ashtonian/parse/core` | `ApiError`, `ResponseError`, `SchemaResourceError`, `SchemaValidationError`, `SchemaValidationErrors`, `SchemaVersionError`, `ConstraintName`, `ConstraintFailure`, `ErrorBody`, `Problem`, `FieldError`, `FieldType` |
| `@ashtonian/parse/filter` | `filter`, `and`, `or`, `not`, `sort`, `validateFilter`, `validateSort`, list / facet / schema types |
| `@ashtonian/parse/patch` | `validatePatch`, patch schema and result types |

## Key Files

| File | Responsibility |
|---|---|
| `src/client.ts` | `createClient` — requests, schema cache, pagination, patch, facets |
| `src/validate.ts` | `SchemaValidator` — validation + schema introspection per resource |
| `src/filter/builder.ts` | `FilterBuilder` (immutable field conjunctions), `and` / `or` / `not`, `SortBuilder` |
| `src/filter/validate.ts` | `validateFilter` / `validateSort`, mirroring the Go parser |
| `src/filter/types.ts` | Filter, list, page and facet wire types |
| `src/patch/validate.ts` | `validatePatch` — merge-patch semantics (null = remove) |
| `src/patch/pattern.ts` | Patch path lookup: exact key, else the most specific `*` / `**` pattern, as `parse/patch` resolves it |
| `src/patch/types.ts` | Patch schema, input and result types |
| `src/schema.ts` | `ResourceSchema` (the schema document), `SCHEMA_VERSION` |
| `src/core/errors.ts` | `ApiError`, `ResponseError`, `SchemaResourceError`, `SchemaValidationError`, `SchemaValidationErrors`, `SchemaVersionError` |
| `src/core/problem.ts` | RFC 9457 `Problem` and `FieldError`: recognition and normalization |
| `src/core/types.ts` | Shared types: `ErrorBody`, `FieldType`, `RequestOptions` |

## References

- [Go package](../../README.md)
- [HTTP and error contract](../../docs/http-errors.md)
- [Integration responsibilities and numeric limits](../../docs/integrations.md#typescript-client)
