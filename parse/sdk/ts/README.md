# @ashtonian/parse

TypeScript client for APIs built on the Go `parse` package: type-safe filter and sort builders, schema discovery, client-side validation for the shared schema contract, cursor pagination, facets, and JSON Merge Patch updates. Its request and response shapes are the server's wire format, verified against fixtures the Go package emits.

Zero runtime dependencies. Runs wherever `fetch` and `AbortSignal.any` exist (Node.js ≥ 20.3, current browsers, Deno, edge runtimes).

## Install

Build and package the SDK from this directory:

```bash
npm ci
npm run build
npm pack
```

Install the resulting tarball in the consuming project with `npm install /path/to/tarball.tgz`, or use your workspace package setup. The declared import name is `@ashtonian/parse`; these examples do not assume a public registry release.

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

const client = createClient({ baseURL: "https://api.example.com", validateBeforeSend: true });
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
| `baseURL` | string | — (required) | API origin and prefix, no trailing slash |
| `headers` | `Record<string, string>` | none | Headers sent with every request (e.g. `Authorization`) |
| `fetch` | `typeof fetch` | `globalThis.fetch` | Fetch implementation |
| `schemaPath` | string | `"api"` | Prefix of the schema endpoints (`{schemaPath}/{resource}/schema`, `{schemaPath}/schemas`) |
| `validateBeforeSend` | boolean | `false` | Validate filters, sorts and patches against the server schema before sending |
| `timeout` | number (ms) | `30000` | Per-request deadline, combined with any caller `AbortSignal` |

## Server Contract

The client speaks the list, patch and facet endpoints `forge/feature` serves over parse:

| Client call | Request |
|---|---|
| `list(opts)` | `GET /{resource}?filter=&sort=&cursor=&page_size=&include_total=&include_deleted=&search=&include=&facets=` |
| `listAll(opts)` | `list` repeated with `cursor = page_info.end_cursor` while `page_info.has_next_page` |
| `patch(id, input)` | `PATCH /{resource}/{id}` with a JSON Merge Patch body |
| `patchMany(ids, input)` | `PATCH /{resource}?id[]=…&id[]=…` with a JSON Merge Patch body |
| `facets(fields, opts)` | `OPTIONS /{resource}?facets=a,b&filter=` → `facets` |
| `schema()` | `GET /{schemaPath}/{resource}/schema` (parse `SchemaRegistry.SchemaHandler`) |

A list page is `{ items, page_info: { has_next_page, has_prev_page, start_cursor?, end_cursor?, total_count? }, _relations?, _truncated?, facets? }` — Go `filter.Page`. `_relations` carries `include` projections: relation field → owner id → related ids (at most 100 per owner by default); `_truncated` lists, per relation field, the owners whose ids were cut at that cap. `facets` carries the counts a list's `facets` option asked for, over the matched rows: field → buckets, a terms facet's by count then key, a range facet's as declared. A server that runs the plan with `parsepgx.QueryPage` answers it; `forge/feature` serves facets through `facets(fields, opts)` only. A patch returns `{ updated, failed? }`.

## Filtering

```typescript
const expr = filter<Product>()
  .where("category", { $in: ["books", "games"] })
  .where("name", { $like: "The %" })          // % any run, _ one character, \ escapes
  .where("labels", { $contains: { tier: "gold" } }) // JSON document containment
  .or(filter<Product>().where("price", { $lt: 5 }))
  .build();
```

Operator semantics are the server's: equality and ordering are case-sensitive (strings order by code point), `$contains` is a literal substring on strings, document containment on JSON fields and element containment on arrays, `$has_key` tests a JSON document for a top-level key (jsonb `?`), declared JSON paths are fields named `document.member`, and a predicate on a missing (NULL) value never matches, so `$ne` / `$nin` / `$not` exclude it. A plain object on a JSON field is document equality.

## Facets

```typescript
const counts = await products.facets(["category"], { filter: { price: { $lt: 20 } } });
// { category: [{ key: "books", count: 89 }, …] }

const page = await products.list({ filter: { price: { $lt: 20 } }, facets: ["category"] });
// page.facets: the same counts beside every page of the list
```

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

The schema document carries its format version; the client reads version `SCHEMA_VERSION` and refuses any other with `SchemaVersionError`, so a document whose members changed meaning never validates a request. With `validateBeforeSend: true` the schema is fetched once per resource (concurrent callers share the request; a failed or refused fetch is retried by the next call) and every filter, sort, include, facet and patch is validated first. A filter's relation predicates (`$some`, `$none`, `$is`, `$is_not` on a field whose `relation.target` names another resource) validate against that resource's schema, which the client fetches the same way, two levels deep; a JSON array's element filters validate against its `elements`. `validateFilter` (with an optional `RelatedSchemas` lookup), `relatedTargets`, `validateSort` and `validatePatch` are also exported for direct use.

Two Go-emitted corpora pin client to server: `testdata/filter_conformance.json` and `testdata/patch_conformance.json` record the Go parser's validity and error code for every input, and the client must give the same answer. Patch validation checks what the server checks per path — patchable, null only where `nullable`, the value's type, an integer within its Go kind (`number_kind`, exactly for kinds of 32 bits or fewer), the enum, the bounds (`min_length` / `max_length` in code points; `minimum` / `maximum`, exact decimal text compared exactly with an integer or decimal and at the kind's precision with a float), an array's element type with each element's enum and bounds, a relation's list of keys, and the operation count. Left to the server: timestamp and UUID spellings, 64-bit integer ranges, values inside JSON documents, array and geo operand shapes in filters, nesting and cost bounds, and server-specific policy.

## Errors

| Error | When | Fields |
|---|---|---|
| `ApiError` | Non-2xx response | `status`, `code` (server E-code, `""` for a non-JSON body), `body` (`{code, message, details?, trace_id?, timestamp?, path?}`) |
| `SchemaValidationError` | Client-side validation failed | `code` (the E-code the server would return), `field`, `detail` |
| `SchemaVersionError` | The server's schema document is in a format version this client does not read | `resource`, `version` (as served), `supported` (`SCHEMA_VERSION`) |

Validation codes match the Go parser: E2071 unknown field, E2072 operator not allowed, E2073 value type mismatch, E2062 non-array membership value, E2086 malformed `$and` / `$or` / `$not`, E2087 value outside the enum, E2116 non-string UUID, E2079 / E2081 sort field / count, E2064 / E2066 / E2076 patch operation / size / path.

## Testing

```bash
npm ci
npm run check   # TypeScript 7 native build-mode check over sources and tests
npm run check:compat # TypeScript 6 tooling API compatibility
npm run lint    # eslint, typescript-eslint strict type-checked
npm test        # vitest
npm run build   # emits dist/
```

Development requires Node.js 22.13+ (or 24+). Builds use TypeScript 7 through `@typescript/native`; typed ESLint uses the supported TypeScript 6 API through the `typescript` alias. These are development dependencies; the published client has no runtime dependencies. The tarball includes compiled output and original library source for declaration-map navigation; test sources stay out. JavaScript maps include their source text.

Three Go-emitted fixtures keep the client on the server's contract; each fails its Go test when stale:

| Fixture | Consumed by | Regenerate |
|---|---|---|
| `testdata/filter_conformance.json` — filter inputs with the Go parser's verdict and E-code | `src/filter/conformance.test.ts` | `go test -tags=unit -run TestEmitFilterConformance -update ./parse/` |
| `testdata/patch_conformance.json` — merge patches with the Go parser's verdict and E-code, and the schema document they are checked against | `src/patch/conformance.test.ts` | `go test -tags=unit -run TestEmitPatchConformance -update ./parse/` |
| `testdata/sdk_wire.json` — a keyset walk serialized as `filter.Page`, and a registry schema document with its version | `src/client.test.ts` | `go test -tags=unit -run TestSDKWireFixture -update ./parse/` |

## Package Structure

| Import | Contents |
|---|---|
| `@ashtonian/parse` | `createClient`, `SchemaValidator`, `SCHEMA_VERSION`, builders, validators, all types |
| `@ashtonian/parse/core` | `ApiError`, `SchemaValidationError`, `SchemaVersionError`, `ErrorBody`, `FieldType` |
| `@ashtonian/parse/filter` | `filter`, `sort`, `validateFilter`, `validateSort`, list / facet / schema types |
| `@ashtonian/parse/patch` | `validatePatch`, patch schema and result types |

## Key Files

| File | Responsibility |
|---|---|
| `src/client.ts` | `createClient` — requests, schema cache, pagination, patch, facets |
| `src/validate.ts` | `SchemaValidator` — validation + schema introspection per resource |
| `src/filter/builder.ts` | `FilterBuilder`, `SortBuilder` |
| `src/filter/validate.ts` | `validateFilter` / `validateSort`, mirroring the Go parser |
| `src/filter/types.ts` | Filter, list, page and facet wire types |
| `src/patch/validate.ts` | `validatePatch` — merge-patch semantics (null = remove) |
| `src/patch/types.ts` | Patch schema, input and result types |
| `src/schema.ts` | `ResourceSchema` (the schema document), `SCHEMA_VERSION` |
| `src/core/` | `ApiError`, `SchemaValidationError`, `SchemaVersionError`, shared types |

## References

- [Go package](../../README.md)
- [HTTP and error contract](../../docs/http-errors.md)
- [Integration responsibilities and numeric limits](../../docs/integrations.md#typescript-client)
