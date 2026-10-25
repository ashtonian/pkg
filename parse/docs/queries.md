# Filters and list queries

[Package overview](../README.md)

## Choose an entry point

Use `Resource.ParseFilter` for a JSON filter or `ParseRequestFilter` for an HTTP request containing JSON and flat field parameters. Both return a parsed filter; pass its AST to `filter.ListFilterAST` to build a list without reparsing it. Use `filter.NewSQLBuilder` when you only need a WHERE clause.

## Filter grammar

JSON filters (`{"status":"active","price":{"$gte":10}}`) and flat query parameters (`status=active&price_gte=10`, via `filter.QueryParamParser`) compile to the same operator map and the same validation. Predicates follow SQL three-valued logic: a predicate on a NULL field is generally UNKNOWN, so ordinary `ne`, `nin`, and `$not` comparisons do not match NULL rows. Empty membership lists and explicit null tests have their own semantics below. Backend-specific differences are documented rather than hidden.

| Operator | JSON | Query parameter | Meaning |
|----------|------|-----------------|---------|
| `eq` / `ne` | `{"f": v}`, `{"f": {"$ne": v}}` | `f=v`, `f_ne=v` | Equality; on JSON fields a document comparison (key order and `1` vs `1.0` irrelevant) |
| `gt` `gte` `lt` `lte` | `{"$gt": 10}` | `f_gt=10` | Ordering; strings by code point (`COLLATE pg_c_utf8` on PostgreSQL 17+ or `"C"` with `CollationC`, `BINARY` on SQLite), as keyset sorts are |
| `in` / `nin` | `{"$in": ["a","b"]}` | `f_in=a,b` | Membership; `[]` matches nothing / everything |
| `like` / `ilike` | `{"$like": "a%"}` | `f_like=a%25` | `%` any run, `_` one character, `\` escapes; `ilike` folds case (ASCII only on SQLite) |
| `contains` | `{"$contains": x}` | `f_contains=x` | String: literal substring. JSON: jsonb `@>`. Array: holds every element |
| `null` / `not_null` / `exists` | `{"$null": true}`, `{"$not_null": true}` | `f_null=true`, `f_not_null=true` | IS NULL / IS NOT NULL; one capability — declaring any allows all three |
| `near` / `within` / `intersects` | `{"$near": {"lat": 1, "lon": 2, "distance": "5km"}}` | — | Geo, WGS 84; polygons and `intersects` need PostgreSQL + PostGIS |
| `$and` / `$or` / `$not` | `{"$or": [{…}, {…}]}` | — | Boolean composition |

Values normalize before any SQL is built: integers exactly (range-checked against the Go kind, floats beyond ±2^53 rejected), UUIDs to canonical form, timestamps to RFC 3339 UTC with relative tokens (`now-7d`) resolved against the parser clock, JSON documents to a canonical form (their numbers within int64, or decimals a float64 holds exactly — E2073 otherwise, since SQLite would round them). See the sections below and [relations](relations.md).

## Type capabilities

`filter_ops` (and `core.GeneratorOptions.DefaultFilterOps`) may name only the operators `OperatorsFor(type, relation)` returns; anything else fails schema generation:

| Type | Operators | Default when `filter_ops` is omitted |
|---|---|---|
| string | eq ne gt gte lt lte in nin like ilike contains null exists | eq ne contains like ilike in nin |
| int, float | eq ne gt gte lt lte in nin null exists | eq ne gt gte lt lte in nin |
| timestamp | eq ne gt gte lt lte in nin null exists | eq ne gt gte lt lte |
| uuid | eq ne in nin null exists | eq ne in nin |
| bool | eq ne in nin null exists | eq ne |
| json | eq ne contains has_key null exists | eq ne contains has_key exists null |
| object | eq ne contains has_key null exists | none |
| array | contains null exists | contains null |
| geo | near within intersects null exists | near within |
| relation | the target ID type's operators, without contains | eq ne in nin null not_null |

The operator contract is shared where supported. Backend differences and relation limits are explicit below:

- A predicate on a NULL field is NULL, so `ne`, `nin` and `$not` never match NULL rows; `$in: []` matches nothing, `$nin: []` everything.
- Strings compare case-sensitively and order by code point. LIKE patterns: `%` any run, `_` one character, `\` escapes; a trailing `\` is rejected. `$like` is case-sensitive (SQLite: GLOB), `$ilike` folds case (SQLite: ASCII only). `$contains` is a literal substring. `MatchLike` is the reference matcher.
- JSON documents: `$eq` / `$ne` compare by value (key order and `1` vs `1.0` irrelevant, array order significant); `$contains` is jsonb `@>`. `$has_key` is jsonb `?`: the document has the key at the top level (whatever its value, null included), a top-level array has it as a string element, or the document is that string. `JSONEqual` / `JSONContains` / `JSONHasKey` are the reference semantics; SQLite compiles the needle into `json_each` predicates. A plain object on a JSON field is equality, not an operator object. A document declared `storage:"json"` or `"text"` is read as jsonb on PostgreSQL.
- Arrays: `$contains` holds every listed element (order and multiplicity ignored; a lone value is a one-element list).
- Geo (WGS 84): `$near` is a great-circle distance (PostGIS `ST_DWithin` on geography; SQLite spherical haversine — near the radius the two differ by up to ~0.5%). `$within` a box is boundary-inclusive; polygons bind one WKT ring; SQLite rejects polygons and `$intersects` (E2115). Boxes must not cross the antimeridian.
- Relations compile to correlated `EXISTS` / `NOT EXISTS`: `$eq x` — some related row is x; `$ne x` — none is; `$null: true` — no related rows.

Values normalize at parse (the AST carries canonical values): exact `int64` / `uint64`, finite `float64`, canonical UUID strings, RFC 3339 UTC timestamps with relative tokens (`now-7d`, `now+1h`) resolved, canonical JSON documents bounded by `MaxDepth` / `MaxArraySize`.

`filtertest` holds the conformance fixture — an entity with a field of every type, its rows and the expected rows per filter; a backend passes with `filtertest.Run(t, name, backend)`.

## JSON paths

A document column's typed members are filterable as dotted fields — `{"attrs.color": "red"}`, `{"settings.window.from": {"$gt": "now-1h"}}` — once they are declared. Requests never add paths.

| Declaration | Where | Paths |
|---|---|---|
| Struct (object) column | members tagged `parse:"filter"` / `parse:"sort"` / `parse:"facet"` (nested structs too, each step tagged) | `<column>.<member>`, by the members' JSON names; `filter_ops`, `facet`, `indexed` and `label` on a member apply to its path |
| Map, `any` or `json.RawMessage` column | `paths:"color:string:filter+sort+facet;size:int:filter+indexed;dims.w:float"` | each `member.path:type[:caps]`; caps `filter` (implied), `sort`, `facet` (terms) and `indexed` |
| Per request (tenant custom fields) | `resource.WithPaths(filter.PathDecl{Path: "custom.tier", Type: core.FieldTypeString, Sort: true, Facet: &filter.FacetSchema{Type: filter.FacetTerms}})` — or, below a resource, `schema.WithPaths` / `sorts.WithPaths(schema, …)` | a copy-on-write view that parses, lists and updates like the resource; the shared resource is unchanged |

Members are identifiers (`[A-Za-z0-9_-]+`), at most `filter.MaxPathDepth` (8) deep, typed string, int, float, bool, timestamp or uuid. Operators default to the type's column defaults plus the NULL tests.

A path's value is the member when it has the declared JSON kind:

| Type | Member | Compared as |
|---|---|---|
| string | a JSON string | text, code-point order |
| int, float | a JSON number | by value: integers exactly, decimals as float64 |
| bool | true or false | boolean |
| timestamp | a UTC RFC 3339 string (`Z`, up to nine fractional digits — what `encoding/json` writes for a UTC `time.Time`) | its canonical text (`core.TimestampLayout`), so instants order correctly |
| uuid | a string that is a UUID once ASCII-lower-cased | lower-cased text |

Anything else is **absent**: a missing key, JSON null, another kind, a timestamp with another offset, a non-object on the way, a NULL column. Absent is SQL NULL on every backend, so the NULL rules above apply: `$ne` / `$nin` / `$not` never match it and `$null: true` does.

| Backend | Rendering |
|---|---|
| PostgreSQL | `CASE WHEN jsonb_typeof(doc #> '{a,b}') = '<kind>' THEN (doc #>> '{a,b}')[::numeric \| ::boolean] END`; `json` and `text` storage read as jsonb |
| SQLite | `CASE WHEN json_type(doc, '$."a"."b"') IN (…) THEN json_extract(doc, '$."a"."b"') END` |
| Memory | `filter.PathValue(doc, field)` — the reference rule the SQL renderings are tested against row by row |

A facetable path counts like a column: terms over its values, ranges over its numbers (`facet:"range:0-5,5+"` on a struct member, `PathDecl.Facet` per request); an absent member is in no bucket.

A path sort key orders like a nullable column: an absent member is NULL, greatest (`NULLS LAST` ascending, `NULLS FIRST` descending), text members by code point. A cursor reads the member from the document's row column with the same value rule, so keyset pages walk forwards and backwards over paths; a numeric path's cursor carries an int64 or a float64, since an int path may hold a decimal member. `filter.KeyValue(key, rowValue)` is that rule for any key, which a memory backend uses to order rows.

The rendered expression is immutable, so an expression index on `filter.FieldExpr(dialect, field)` serves it; declare the path `indexed` to lower its cost. A document column's storage is its `storage` tag: `jsonb` (default), `json`, `text`, or `scalar:<type>` for a struct the database holds as a scalar, such as a decimal, which then filters as that scalar and has no paths. Run `filtertest.RunPaths(t, schema, backend)` to prove a backend's paths. A merge patch into a typed document member reads back through its path on every backend; a merge into a document that may be absent (a pointer struct) must supply every member.

### Array elements

A JSON array of objects filters by its elements: every predicate inside one `$some` holds for one element, and `$none` holds when no element satisfies them (a document that is not an array, or is NULL, has no elements). Element paths are declared like paths — the members tagged `parse:"filter"` of a slice-of-struct document's element type, or an `elements:"by:string;priority:int"` tag on any JSON array — and named by their member path inside the element (`dims.w`).

```text
{"items":{"$some":{"sku":"a","qty":{"$gt":2}}}}
postgres EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(orders.items) = 'array' THEN orders.items END) AS elem1__(v) WHERE (… elem1__.v #>> '{qty}' … AND … elem1__.v #>> '{sku}' …))
sqlite   EXISTS (SELECT 1 FROM json_each(CASE WHEN json_type(orders.items) = 'array' THEN orders.items END) AS elem1__ WHERE (… json_extract(…elem1__.value…, '$."qty"') …))
```

An element that is not an object has no members, so a mixed array (`["a", {"sku":"a"}]`) is tested on its objects. A memory backend reads each element of `filter.DocumentElements(doc)` with `filter.PathValue(element, field)`. An element predicate costs 10 (1 when the document is tagged `indexed`) plus its filter.

## Lists and keyset pagination

A list endpoint declares its resource once — `parse.NewResource[T](table, PrimaryKey, TenantColumn, DeletedColumn, PageSize, AcceptParams, ParserOptions)` — and serves each request in three steps:

1. `ParseList(url.Values)` reads every `filter.Param*` parameter into a `*ListRequest`: the JSON filter ANDed with the flat field parameters and bounded as one expression, sort, cursor, page size (missing: the default; above the maximum: clamped; malformed or not positive: E2067), `include_total` and `include_deleted` (booleans; anything else is E2067), search (E2067 on a resource without search fields), facets (unknown or not facetable: E2071), includes and the field mask. A parameter that is none of these, not a filterable field and not accepted with `AcceptParams` is E2071, so a misspelt filter cannot return an unfiltered list. Off HTTP — an RPC, a message, a saved view — build a `ListInput` (the JSON filter, `FieldFilters` keyed `field` or `field_<op>`, sort, cursor, page size, flags and name lists) and call `ParseListInput`: `ParseList` is that call behind a query-string adapter, with the same limits and codes; a `ListInput` page size of zero is the default and a negative one E2067.
2. `BuildList(dialect, req, parse.Scope{Tenant, Governance, Policy, IncludeDeleted})` compiles it under the server's decisions: the tenant floor (or governance in its place), soft-delete masking unless `IncludeDeleted`, and the policy, ANDed with the request; the key as the final keyset column; the count and facets when asked. A tenant-scoped resource without `Scope.Tenant` is `ErrTenantRequired`. The `*ListPlan` holds `Page` (the full `SELECT` of the resource's `Columns`), `Fragment` (its `WHERE … ORDER BY … LIMIT` alone, for a caller's own projection with `Page.Args`), `Count`, `Facets`, `Include` and `Keyset`.
3. `Page(rows, plan)` (or `PagePointers`) builds the page from the fetched rows. `IncludeQueries(plan, keys)` returns the include statements for the page's keys (`Key(row)`), tenant-scoped as the list was; read each one's rows with its `Scan` into `Page.Relations` (and `Page.Truncated` for owners cut at the cap).

Underneath, `NewListQuery(filterSchema, sortSchema, dialect).Build(opts...)` returns a `*ListResult`:

| Field | Content |
|---|---|
| `Fragment` | `WHERE … ORDER BY … LIMIT n` to append to `SELECT … FROM <table>` |
| `Where`, `OrderBy`, `Args` | The parts, for custom composition |
| `FetchLimit` / `Limit` | Rows to fetch (limit + 1) / the requested page size |
| `Cursor` | The keyset plan `NewPage` needs |
| `CountSQL`, `CountArgs` | `SELECT COUNT(*)` with the same WHERE (`ListWithCount`) |
| `Facets` | One `WITH base AS … UNION ALL` facet query (`ListFacets`); scan with `ScanFacetUnion` |
| `Sort` | The parsed sort |

`NewPage(rows, res.Limit, res.Cursor, value)` truncates the fetched rows, restores presentation order after a backward seek, sets `has_next_page` / `has_prev_page`, and mints `start_cursor` / `end_cursor` (only toward pages that exist). `value(row, column)` resolves a key column; `CursorValueByTag(row, "db", column)` does it by struct tag. Keys are the sort fields plus the primary key declared on the resource; NULL sorts greatest (`NULLS LAST` ascending, `NULLS FIRST` descending) and seeks are NULL-aware. Cursors carry a generation and are capped at 4 KiB; a token from another sort or generation fails with E2082.

Facets name facetable fields; any other name is E2071, like an unknown filter field. A relation facet counts distinct owners per target, and `BuildIncludeQuery` returns distinct `(owner, target)` pairs, at most `WithMaxIncludePerRelation` per owner, so two junction rows pairing the same rows (typed edges) count once. Search covers string fields; schema generation refuses `search` on any other type unless a `ColumnMapper` supplies the expression. On PostgreSQL an array argument binds as a typed Go slice (`[]string`, `[]int64`, `[]float64`, `[]bool`, `[]time.Time`; UUIDs as their canonical text), which pgx encodes in every query mode, including `QueryExecModeExec` and the simple protocol.

## Search and facets

`filter.ListSearch` adds a predicate without changing sort order. PostgreSQL uses `to_tsvector` / `plainto_tsquery`; SQLite uses a LIKE phrase match. These have different tokenization and matching behavior. Tag string fields with `parse:"search"`; `search_weight:"A"` through `"D"` controls PostgreSQL weights. A custom expression must produce text for the chosen dialect.

`filter.ListFacets` builds aggregations for fields tagged `parse:"facet"`. Use `facet:"terms:20"` for the top terms or `facet:"range:0-50,50-100,100+"` for ranges. `ListWithCount` and facets share the filter, policy, and search population, excluding the page cursor. Execute the returned statements separately and scan the facet union with `filter.ScanFacetUnion`. The base page does not automatically execute counts, facets, or includes.

`filter.ResolveInclude(ctx, field, ownerKeys, lookup, opts...)` resolves the same include projection on non-SQL stores. The `IncludeLookup` supplies a batched iterator of owner/target pairs matching the lookup predicate; parse resolves target visibility, orders distinct keys and reports truncated owners. The SQL and non-SQL paths share the include options.

## Configuration

A `parse.Resource` declares its primary key once with `parse.PrimaryKey` (default `id`). Every list, including `Resource.ListQuery`, appends that key automatically. Hand-built filter schemas declare `ResourceSchema.PrimaryKey` before constructing a `ListQuery`; the key is schema metadata, never a request option.

`ListOption`s for `ListQuery.Build`:

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `ListFilter(json)` | string | none | JSON filter, parsed with the query's config |
| `ListFilterAST(ast)` | `*FilterAST` | none | A parsed filter (e.g. from `QueryParamParser`) |
| `ListSort(param)` | string | schema default | `-price,name` |
| `ListSortResult(sr)` | `*SortResult` | none | A parsed sort |
| `ListCursor(c)` | string | none | Resume after a cursor |
| `ListLimit(n)` | int | `50` (`DefaultListLimit`) | Page size |
| `ListMaxLimit(n)` | int | `1000` (`MaxListLimit`) | Upper bound on `ListLimit` |
| `ListWithCount(b)` | bool | `false` | Build `CountSQL` |
| `ListFacets(fields...)` | `...string` | none | Build the facet query |
| `ListSearch(q)` | string | none | Full-text predicate (WHERE only, order unchanged) |
| `ListPolicy(pc)` | `*PolicyConstraints` | none | Server-side constraints ANDed with the filter |
| `ListTenant(v)` | any | none | Tenant for tenant-scoped relation junctions (filter, count, facets) |
| `ListUnscopedRelations()` | — | off | Build tenant-scoped junctions without a tenant — platform tooling only |
| `ListStartParam(n)` | int | `0` | Offset the main fragment after `n` earlier arguments; standalone count/facet statements keep independent numbering |

`SQLBuilderOption`s for `NewSQLBuilder`:

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithStartParam(n)` | int | `0` | First placeholder is `n+1` (e.g. after an UPDATE's SET arguments) |
| `WithTenant(v)` | any | none | Tenant for tenant-scoped relation junctions |
| `WithUnscopedRelations()` | — | off | Opt out of junction tenancy |

Facet, include, and flat-query options:

| Option | Type | Default | Description |
|---|---|---|---|
| `WithFacetTenant(v)` | any | none | Tenant binding for standalone facet queries |
| `WithFacetUnscopedRelations()` | — | off | Explicitly omit junction tenancy in facet queries |
| `WithIncludeTenant(v)` | any | none | Tenant binding for include queries |
| `WithIncludeUnscoped()` | — | off | Explicitly omit include tenancy |
| `WithMaxIncludePerRelation(n)` | int | `100` | Targets per owner; distinct keys capped in SQL or non-SQL resolution. The cap must leave room for the truncation sentinel |
| `QueryParamAccept(names...)` | `...string` | none | Parameters the endpoint reads itself; left alone like the list parameters |
| `QueryParamLenient()` | — | off | Ignore a parameter naming no filterable field instead of refusing it (E2071) |
| `QueryParamConfig(cfg)` | `*core.ParserConfig` | `core.DefaultConfig()` | Limits and value configuration for flat filters |
| `ListQuery.WithFilterConfig(cfg)` | `*core.ParserConfig` | default parser config | Configure JSON filter parsing before using the builder |
| `ListQuery.WithSearchSchema(schema)` | `*SearchSchema` | nil | Supply searchable fields for `ListSearch`; typed parsers do this automatically |

A relation that declares a `tenant_col` fails the build unless a tenant is bound or an unscoped option is given — junction rows of other tenants never match.

A flat parameter that is neither a list parameter (`filter.Param*`), an accepted name nor a filterable field is E2071 on every entry point — `Resource.ParseList` (`AcceptParams`), `Resource.ParseRequestFilter` and `filter.NewQueryParamParser` (`QueryParamAccept`) — so a misspelt filter cannot return every row.

PostgreSQL dialect:

| Field | Type | Default | Description |
|---|---|---|---|
| `PostgresDialect.Collation` | `filter.PostgresCollation` | `CollationPgCUTF8` | Code-point collation for string sorts, keyset seeks and string ranges: `CollationPgCUTF8` (`pg_c_utf8`, PostgreSQL 17+) or `CollationC` (`"C"`, PostgreSQL 14–16). Both order by code point; index the sorted columns with the same collation |

## Request-to-SQL examples

The examples compile against this resource ([source and executable outputs](../examples_sql_test.go); below, long UUIDs and repeated predicates are shortened with …):

```go
type Product struct {
	ID          uuid.UUID      `json:"id"          db:"id"          parse:"filter,sort"`
	Name        string         `json:"name"        db:"name"        parse:"filter,sort,patch,search" search_weight:"A"`
	Category    string         `json:"category"    db:"category"    parse:"filter,facet,patch" facet:"terms:10" enum:"tools,books,garden" indexed:"true"`
	Price       float64        `json:"price"       db:"price"       parse:"filter,sort,facet,patch" facet:"range:0-50,50-100,100+"`
	Stock       *int64         `json:"stock"       db:"stock"       parse:"filter,patch" filter_ops:"eq,gt,lt,null"`
	Tags        []string       `json:"tags"        db:"tags"        parse:"filter,patch" indexed:"true"`
	Attrs       map[string]any `json:"attrs"       db:"attrs"       parse:"filter,patch" indexed:"true"`
	SupplierID  uuid.UUID      `json:"supplier_id" db:"supplier_id" parse:"filter" indexed:"true"`
	CreatedAt   time.Time      `json:"created_at"  db:"created_at"  parse:"filter,sort" sort_default:"desc"`
	Collections []uuid.UUID    `json:"collections" db:"-" parse:"filter,patch,include" relation:"product_collections,product_id,collection_id" indexed:"true"`
	Reviews     []uuid.UUID    `json:"reviews"     db:"-" parse:"filter,include" relation:"reviews,product_id,id" indexed:"true"`
}
```

**Operators.** Membership, ranges, NULL tests, boolean composition, case-folded patterns, and a relative time resolved against the parser clock (fixed at 2026-10-14T12:00Z):

```text
{"category":"tools","name":{"$in":["saw","drill"]}}
postgres WHERE (products.category = $1 AND products.name IN ($2, $3)) ORDER BY products.created_at DESC [tools saw drill]

{"$or":[{"category":"books"},{"$not":{"price":{"$gt":50}}}]}
postgres WHERE (products.category = $1 OR NOT (products.price > $2)) ORDER BY products.created_at DESC [books 50]

{"name":{"$ilike":"%pro%"},"created_at":{"$gte":"now-7d"}}
postgres WHERE (products.created_at >= $1 AND products.name ILIKE $2 ESCAPE '\') ORDER BY products.created_at DESC [2026-10-07T12:00:00.000000000Z %pro%]
sqlite   WHERE (products.created_at >= ?1 AND products.name LIKE ?2 ESCAPE '\') ORDER BY products.created_at DESC [2026-10-07T12:00:00.000000000Z %pro%]
```

**Flat query parameters** parse to the same filter (`filter.NewQueryParamParser`):

```text
?category=tools&price_gte=10&stock_lt=5
postgres WHERE (products.category = $1 AND products.price >= $2 AND products.stock < $3) ORDER BY products.created_at DESC [tools 10 5]
```

**Keyset pages.** The first page fetches `limit + 1` rows so the page knows another exists; the next page seeks past the last row's key instead of an `OFFSET`:

```text
first: WHERE 1=1 ORDER BY products.price DESC, products.id DESC LIMIT 3 []
next:  WHERE (1=1 AND (products.price, products.id) < ($1, $2)) ORDER BY products.price DESC, products.id DESC [80 0190f3c2-…-000000000012]
sqlite WHERE (1=1 AND ((products.price < ?1) OR (products.price = ?1 AND products.id < ?2))) ORDER BY products.price DESC, products.id DESC [80 0190f3c2-…-000000000012]
```

**Count, facets and search** share the request's filter; the count and facets ignore the cursor:

```text
filter {"price":{"$lt":200}}, search "cordless drill", include_total, facets category
where:  (products.price < $1 AND to_tsvector('english', coalesce(products.name, '')) @@ plainto_tsquery('english', $2)) [200 cordless drill]
count:  SELECT COUNT(*) FROM products WHERE (…same predicate…) [200 cordless drill]
facets: WITH base AS MATERIALIZED (SELECT products.category AS facet__0 FROM products WHERE (…)) SELECT facet_field, facet_key, facet_count FROM (SELECT 'category' AS facet_field, k AS facet_key, c AS facet_count, 0 AS facet_ord, 0 AS facet_rank FROM (SELECT facet__0 AS k, COUNT(*) AS c FROM base WHERE facet__0 IS NOT NULL GROUP BY facet__0 ORDER BY c DESC, facet__0 COLLATE pg_c_utf8 LIMIT 10) s0) facets__ ORDER BY facet_ord, facet_rank, facet_count DESC, facet_key COLLATE pg_c_utf8 [200 cordless drill]
```

Facet rows come back in a fixed order: facets as requested, a terms facet's buckets by count with ties by key in code-point order — the same order decides which buckets a terms limit keeps — and a range facet's buckets as declared. A terms facet on a list column (`[]string`, `[]int64`, …) counts each distinct element once per row: `["a","a"]` is one row for `a`. A list page carries the facets as `facets`, keyed by field (`filter.FacetMap`); a backend that counts in Go orders them with `filter.FacetBuckets`.

SQLite searches with `LIKE '%cordless drill%'` — a phrase, where PostgreSQL's `plainto_tsquery` matches both words anywhere.

**Patches** compile to one `SET` expression per column. A merge-patch object merges into the stored document; `null` deletes a key or clears a nullable column:

```text
{"name":"Cordless drill","stock":null,"attrs":{"color":"blue","legacy":null}}
postgres SET attrs = (COALESCE(jsonb_path_query_first(attrs, 'strict $ ? (@.type() == "object")'), '{}'::jsonb) - $1::text[] || $2::jsonb), name = $3, stock = NULL [[legacy] {"color":"blue"} Cordless drill]
sqlite   SET attrs = json_patch(COALESCE(attrs, '{}'), ?1), name = ?2, stock = NULL [{"color":"blue","legacy":null} Cordless drill]

[{"op":"array_append","path":"/tags","value":"clearance"},{"op":"replace","path":"/price","value":19.99}]
postgres SET tags = array_append(COALESCE(tags, '{}'), $1), price = $2 [clearance 19.99]
```

## Cursor validation

Schema-backed cursor keys validate type, numeric range, and nullability before SQL construction (E2082). Manually constructed schemas need `CursorKey.NumberKind` / `SortFieldSchema.NumberKind` for numeric bounds. An identifier-only tie-breaker without schema metadata validates canonical non-null scalars only. Include the key in your schema for typed checks. Pointer-receiver `driver.Valuer` / text-marshaler scalar keys can supply canonical values, but the package cannot infer a custom database scalar's ordering from its Go encoding (for example decimal text versus a NUMERIC column). Choose keys with a known consistent SQL/Go representation. Cursors carry sort metadata, not authorization: reapply server policy on every page.

[HTTP parameters](http-errors.md) · [JSON documents](patches.md#json-documents) · [Relations](relations.md)
