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
| `in` / `nin` | `{"$in": ["a","b"]}` | `f_in=a,b` | Membership; `[]` matches nothing / everything; the set binds as one parameter ([sets](#sets)) |
| `like` / `ilike` | `{"$like": "a%"}` | `f_like=a%25` | `%` any run, `_` one character, `\` escapes; `ilike` compares characters lower-cased (ASCII letters only on SQLite) |
| `contains` | `{"$contains": x}` | `f_contains=x` | String: literal substring. JSON: jsonb `@>`. Array: holds every element |
| `some` / `none` | `{"$some": {"$gte": 5, "$lt": 10}}` | — | Array: one element meets the condition / none does ([array elements](#array-elements)). Relation: a related row matches / none does ([relations](relations.md)) |
| `null` / `not_null` / `exists` | `{"$null": true}`, `{"$not_null": true}` | `f_null=true`, `f_not_null=true` | IS NULL / IS NOT NULL; one capability — declaring any allows all three |
| `near` / `within` / `intersects` | `{"$near": {"lat": 1, "lon": 2, "distance": "5km"}}` | — | Geo, WGS 84: PostGIS, SQLite through `parsesqlite`'s functions, and `filter.Evaluate` decide them alike on a JSONB point; native `geography` / `geometry` storage needs PostGIS |
| `$and` / `$or` / `$not` | `{"$or": [{…}, {…}]}` | — | Boolean composition |

A field's object value is an operator object when one of its keys is an operator or every key starts with `$`; any other object is a JSON document compared for equality. A misspelt operator (`{"meta":{"$contain":{…}}}`) is therefore E2072, never a document no row equals, and a document whose keys all start with `$` is written as the operand of `$eq` (`{"meta":{"$eq":{"$ref":"x"}}}`).

Both forms name the same fields (`ResourceSchema.RequestField`, enumerated by `RequestFields`, which the schema document lists): the request fields (`parse:"filter"`) and the resource's key, with the operators the key's system field takes (`eq`, `gt`, `gte`, `lt`, `lte`, `in`). The tenant and soft-delete columns are system fields only a [server policy](#policy-fields) names; a request reaches deleted rows through `include_deleted` alone, and a filter on either column is E2071 in both forms. Flat parameters ANDed together build one operator map per field, so two that set the same operator are E2072 naming both: `status=a&status=b` coalesces to `$in`, and `status_in=c` beside it would otherwise replace it. `status=a&status_in=c` keeps both predicates.

Values normalize before any SQL is built: integers exactly (range-checked against the Go kind, floats beyond ±2^53 rejected), decimals to their canonical exact text ([decimals](schema-configuration.md#decimals)), UUIDs to canonical form, timestamps to RFC 3339 UTC with relative tokens (`now-7d`) resolved against the parser clock, JSON documents to a canonical form (their numbers within int64, or decimals a float64 holds exactly — E2073 otherwise, since SQLite would round them). See the sections below and [relations](relations.md).

## Type capabilities

`filter_ops` (and `core.GeneratorOptions.DefaultFilterOps`) may name only the operators `OperatorsFor(type, relation)` returns; anything else fails schema generation:

| Type | Operators | Default when `filter_ops` is omitted |
|---|---|---|
| string | eq ne gt gte lt lte in nin like ilike contains null exists | eq ne contains like ilike in nin |
| int, float, decimal | eq ne gt gte lt lte in nin null exists | eq ne gt gte lt lte in nin |
| timestamp | eq ne gt gte lt lte in nin null exists | eq ne gt gte lt lte |
| uuid | eq ne in nin null exists | eq ne in nin |
| bool | eq ne in nin null exists | eq ne |
| json | eq ne contains has_key null exists | eq ne contains has_key exists null |
| object | eq ne contains has_key null exists | none |
| array | contains some none null exists | contains some none null |
| geo | near within intersects null exists | near within |
| relation | the target ID type's operators, without contains | eq ne in nin null not_null |

The operator contract is shared where supported. Backend differences and relation limits are explicit below:

- A predicate on a NULL field is NULL, so `ne`, `nin` and `$not` never match NULL rows; `$in: []` matches nothing, `$nin: []` everything. A NULL set member (programmatic ASTs only; the parser refuses one) makes a miss Unknown, as `x IN (…, NULL)` does.
- Strings compare case-sensitively and order by code point. LIKE patterns: `%` any run, `_` one character, `\` escapes; a trailing `\` is rejected. `$like` is case-sensitive (SQLite: GLOB). `$ilike` compares each character lower-cased, PostgreSQL's ILIKE under `pg_c_utf8`: `İ` matches `i`, while `ſ` is no `s`, `ς` no `σ` and `µ` (micro sign) no `μ`, which Unicode case folding would equate. SQLite, and PostgreSQL columns under `CollationC`, lower-case ASCII letters only. `$contains` is a literal substring. `MatchLike` is the reference matcher. A request string holding U+0000 (a value, pattern, set member, document key or member, search term) is E2073, and a cursor key holding one E2082: PostgreSQL refuses the character in text and jsonb, and SQLite ends a pattern at it (`core.CheckNUL`). Stored values are read as they are.
- JSON documents: `$eq` / `$ne` compare by value (key order and `1` vs `1.0` irrelevant, array order significant); `$contains` is jsonb `@>`. `$has_key` is jsonb `?`: the document has the key at the top level (whatever its value, null included), a top-level array has it as a string element, or the document is that string. `core.JSONEqual` and `filter.JSONContains` / `JSONHasKey` are the reference semantics; SQLite compiles the needle into `json_each` predicates. A plain object on a JSON field is equality, not an operator object. A document declared `storage:"json"` or `"text"` is read as jsonb on PostgreSQL.
- Arrays: `$contains` holds every listed element (order and multiplicity ignored; a lone value is a one-element list). `$some` / `$none` test the elements one at a time ([array elements](#array-elements)).
- Geo (WGS 84): `$near` is the geodesic distance on the WGS 84 ellipsoid, as PostGIS's `ST_DWithin` measures geography (Karney's algorithm, to nanometres); a stored latitude beyond a pole reads as geography coerces it (100 is 80). `$within` and `$intersects` test a box or a polygon (one ring) planar in longitude and latitude, as `ST_CoveredBy` and `ST_Intersects` test geometry in SRID 4326: the boundary is included, so for a point the two agree. Boxes must not cross the antimeridian. A JSONB point's `lat` and `lon` members are numbers, or strings holding numbers, as `(doc->>'lat')::float8` reads them (hexadecimal, `NaN` and `Infinity` included; a point holding NaN or an infinity matches nothing), and a duplicated member reads its last value; a point that is not an object, or lacks a member or holds a null one, is NULL. SQLite decides every geo operator through `parsesqlite`'s functions ([parsesqlite](../parsesqlite/README.md)); it and `filter.Evaluate` refuse native `geography` / `geometry` storage (E2115). A member holding anything else (a boolean, text float8 rejects, a number beyond its range) makes PostgreSQL refuse the query, where SQLite and `filter.Evaluate` read the point as NULL.
- Relations compile to correlated `EXISTS` / `NOT EXISTS`: `$eq x` — some related row is x; `$ne x` — none is; `$null: true` — no related rows.

Values normalize at parse (the AST carries canonical values): exact `int64` / `uint64`, finite `float64`, canonical decimal text, canonical UUID strings, RFC 3339 UTC timestamps with relative tokens (`now-7d`, `now+1h`) resolved, canonical JSON documents bounded by `MaxDepth` / `MaxArraySize`. Every relative token of one request resolves against one reading of the parser clock (`WithClock`): its JSON filter, its flat parameters, the policy `BuildList` applies and each related table's policy, however often a relation names it, so `{"$gte":"now","$lte":"now"}` can hold and `now-1h … now` is an hour. A list request keeps the instant it was parsed at, and its includes resolve at it too; a parse or builder handed no instant reads its clock once. A field tagged `json:",string"` — a filter field, the primary key, or a relation's target key under its shorthands — takes its comparison and set operands as JSON strings holding the value (`{"id":"9007199254740993"}`), as it reads in responses; a bare JSON number is E2073 ([quoted values](schema-configuration.md#quoted-values)).

`filtertest` holds the conformance fixture — an entity with a field of every type, its rows and the expected rows per filter; a backend passes with `filtertest.Run(t, backend)`: every case must return exactly its rows, and any error fails it.

### Sets

A `$in` / `$nin` set, an array's `$contains` list ([array elements](#array-elements)) and a lookup's `WhereIn` list ([policy constraints](#policy-constraints)) bind as one array parameter, so a statement's parameter count is independent of the set's size (PostgreSQL caps a statement at 65,535 parameters). `MaxArraySize` alone bounds a set, and a set costs one condition whatever its size: raise `MaxArraySize` and `MaxInputSize` for an endpoint that filters by large sets.

| Backend | Column | [Path](#json-paths) |
|---|---|---|
| PostgreSQL | `col = ANY($1)` / `col <> ALL($1)`: the parameter takes the column's array type, so the column's own equality applies and its index serves the set; decimals `= ANY($1::text[]::numeric[])` | `expr IN (SELECT unnest($1::text[]) AS v)` / `NOT IN`, unnested to the type the path renders (`::numeric`, `::boolean[]`) |
| SQLite | `col IN (SELECT value AS v FROM json_each(?1))` / `NOT IN`; floats encode as REAL, decimals compare as canonical text, `parse_decimal(col) IN (SELECT value AS v FROM json_each(?1))` ([decimals](schema-configuration.md#decimals)) | the same |

A path's set joins its members as a relation, which PostgreSQL hashes in every plan. A column's `= ANY` is served by an index on the column; without one, under a generic plan (pgx caches prepared statements, and PostgreSQL may plan one generically after five executions) it probes the array member by member for each row it scans, so index a column that large sets filter. SQLite holds integers within int64: a `uint64` member above it is an error, as binding one is.

## JSON paths

A document column's typed members are filterable as dotted fields — `{"attrs.color": "red"}`, `{"settings.window.from": {"$gt": "now-1h"}}` — once they are declared. Requests never add paths.

| Declaration | Where | Paths |
|---|---|---|
| Struct (object) column | members tagged `parse:"filter"` / `parse:"sort"` / `parse:"facet"` (nested structs too, each step tagged) | `<column>.<member>`, by the members' JSON names; `filter_ops`, `facet`, `indexed`, `label` and `enum` on a member apply to its path |
| Map, `any` or `json.RawMessage` column | `paths:"color:string:filter+sort+facet;size:int:filter+indexed;dims.w:float"` | each `member.path:type[:caps]`; caps `filter` (implied), `sort`, `facet` (terms) and `indexed` |
| Per request (tenant custom fields) | `resource.WithPaths(filter.PathDecl{Path: "custom.tier", Type: core.FieldTypeString, Sort: true, Facet: &filter.FacetSchema{Type: filter.FacetTerms}})` — or, below a resource, `schema.WithPaths` / `sorts.WithPaths(schema, …)` | a copy-on-write view that parses, lists and updates like the resource, with the relations it declared; the shared resource is unchanged. A view declares no relations and is no relation's target: `Relate` refuses both |

Members are identifiers (`[A-Za-z0-9_-]+`), at most `filter.MaxPathDepth` (8) deep, typed string, int, float, decimal, bool, timestamp or uuid, or an array of one of those (`tags:[]string`, or a struct member of slice type). Operators default to the type's column defaults plus the NULL tests.

A path's value is the member when it has the declared JSON kind:

| Type | Member | Compared as |
|---|---|---|
| string | a JSON string | text, code-point order |
| int, float | a JSON number | by value: integers exactly, decimals as float64 |
| decimal | a JSON number, or a JSON string in plain notation (`"9.50"`, not `"1e3"`) of at most 1000 characters | exactly, by value: `numeric` on PostgreSQL; on SQLite the member's JSON literal as written (`doc -> '$.price'`) through `parse_decimal`; `core.CompareDecimals` in memory |
| bool | true or false | boolean |
| timestamp | a UTC RFC 3339 string (`Z`, up to nine fractional digits — what `encoding/json` writes for a UTC `time.Time`) | its canonical text (`core.TimestampLayout`), so instants order correctly |
| uuid | a string that is a UUID once ASCII-lower-cased | lower-cased text |
| array of a type above | a JSON array | its elements of that type, each read as a member of the type is; elements of another kind are no elements, and an array with none is absent ([array elements](#array-elements)) |

Anything else is **absent**: a missing key, JSON null, another kind, a timestamp with another offset, a non-object on the way, a NULL column. Absent is SQL NULL on every backend, so the NULL rules above apply: `$ne` / `$nin` / `$not` never match it and `$null: true` does. Every member is an object key: `tags.0` is the member named `0`, absent when `tags` is an array, and a member named `null` is one like any other.

| Backend | Rendering |
|---|---|
| PostgreSQL | `CASE WHEN jsonb_typeof(doc -> 'a' -> 'b') = '<kind>' THEN (doc -> 'a' ->> 'b')[::numeric \| ::boolean] END`, one object key per `->`, which is NULL on an array or a scalar; `json` and `text` storage read as jsonb. Containment where it is exact, below |
| SQLite | `CASE WHEN json_type(doc, '$."a"."b"') IN (…) THEN json_extract(doc, '$."a"."b"') END` |
| Memory | `filter.PathValue(doc, field)` — the reference rule the SQL renderings are tested against row by row, and how `filter.Evaluate` reads a path |

On PostgreSQL a string, bool, int or float path of a `jsonb` column compares `$eq` by containment, `doc @> '{"a":{"b":v}}'`, and `$in` with at most `PostgresDialect.ContainmentMaxValues` members as `doc @> ANY($1::jsonb[])`, which a GIN index on the document serves (`jsonb_path_ops` suffices). Containment compares numbers by value and keeps a member holding an array apart from its elements, as the typed expression does, but it is False where the typed comparison is Unknown, so it is used only where an even number of `$not` (usually none) enclose the condition within its statement or subquery (`$and` / `$or` are monotone, so the statement is monotone in the condition there, and `$some` / `$none` and relation predicates select their elements and rows): there a row is selected exactly when it is with every Unknown condition False. Under an odd number of `$not`, and for decimals (a decimal member may be a string), the typed expression stays. An array path of strings, bools or numbers takes `$contains` by containment on the same terms, `doc @> '{"tags":["a","b"]}'`, and `$some` of an element `$eq` or `$in` (ContainmentMaxValues at most) at any polarity, as `(doc IS NOT NULL AND doc @> '{"tags":["a"]}')`: an element condition is never Unknown, and containment holds exactly when the array has an equal element. `SQLResult.Where` is therefore a selection predicate for a WHERE, ON or FILTER clause, not a value to negate or project.

A facetable path counts like a column: terms over its values, ranges over its numbers (`facet:"range:0-5,5+"` on a struct member, `PathDecl.Facet` per request); an absent member is in no bucket. An array path counts terms only, each row once per distinct element.

A path sort key orders like a nullable column (an array path is no sort key): an absent member is NULL, greatest (`NULLS LAST` ascending, `NULLS FIRST` descending), text members by code point. A cursor reads the member from the document's row column with the same value rule, so keyset pages walk forwards and backwards over paths; a numeric path's cursor carries an int64 or a float64, since an int path may hold a decimal member, and a decimal path's its canonical text. `filter.KeyValue(key, rowValue)` is that rule for any key, which a memory backend uses to order rows.

The rendered expression is immutable, so an expression index on `filter.FieldExpr(dialect, field)` serves it (on SQLite a decimal's calls `parse_decimal`, and an index on it needs the functions on every connection that writes the table); declare the path `indexed` to lower its cost. A document column's storage is its `storage` tag: `jsonb` (default), `json`, `text`, or `scalar:<type>` for a struct the database holds as a scalar, such as a decimal library type without the `core.Decimal` method set, which then filters and sorts as that scalar and has no paths. Run `filtertest.RunPaths(t, schema, backend)` to prove a backend's paths. A merge patch into a typed document member reads back through its path on every backend; a merge into a document that may be absent (a pointer struct) must supply every member.

### Array elements

`$some` holds when one element of an array meets its condition and `$none` when none does. Neither is ever Unknown: an array that is NULL, empty, or (in a document) not an array has no elements.

#### Arrays of scalars

An array column (`[]string`, `[]int64`, …) or an array path (`paths:"tags:[]string"`, a struct member of slice type) takes a condition on one element: the element type's operators but the NULL tests, combined by `$and` / `$or` / `$not`, every comparison holding for the same element. `{}` holds for any element of the type.

```text
{"scores":{"$some":{"$gte":5,"$lt":10}}}                 a score in [5, 10)
{"tags":{"$some":{"$or":[{"$like":"a%"},{"$eq":"b"}]}}}  a tag starting with a, or b
{"tags":{"$none":{"$in":["spam","test"]}}}               no tag is spam or test
{"doc.tags":{"$some":{}}}                                the member holds a string
```

An array path's value is its elements of the declared type, each read as a member of that type is: `9.5` and `"10.50"` are both decimals, `7.0` an int, `"7"` no int. Elements of another kind are no elements, and a member with none is absent, so `$null` holds for it and `$contains` on it is Unknown. An array column keeps SQL's rules: `$contains` is Unknown on a NULL column and False on an empty one, unless it lists nothing. A NULL element of an array column, which a PostgreSQL array holds whatever the Go type is, is no element: the SQL renderings skip it, and `filter.Evaluate` reads it as SQL does.

| Backend | Array column | Array path |
|---|---|---|
| PostgreSQL | `EXISTS (SELECT 1 FROM unnest(col) AS elem1__(v) WHERE elem1__.v IS NOT NULL AND …)` | `EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(doc -> 'tags') = 'array' THEN doc -> 'tags' END) AS elem1__(v) WHERE <typed element> IS NOT NULL AND …)`, or containment ([JSON paths](#json-paths)) |
| SQLite | `EXISTS (SELECT 1 FROM json_each(CASE WHEN json_type(col) = 'array' THEN col END) AS elem1__ WHERE <typed element> …)` over the JSON text column | the same over `json_each(…, '$."tags"')`; a decimal element reads its literal as written, `parse_decimal(elem1__.json -> elem1__.fullkey)` |
| Memory | `filter.Evaluate` over the Go slice, or a decoded JSON array | each element as `filter.PathValue` reads it |

`$contains` binds its list as one [set](#sets) parameter and counts: `((SELECT count(DISTINCT <typed element>) FROM <elements> WHERE <typed element> IN (<set>)) = (SELECT count(DISTINCT v) FROM (<set>) AS elem1__set))`, the two uses of `<set>` reading the one parameter. The distinct elements equal to a listed value number as many as the distinct listed values exactly when each has an equal element, so repeats in either are irrelevant, and the statement's size and parameter count are independent of the list's: one EXISTS per value would pass SQLite's expression depth (1,000) and PostgreSQL's parameter limit. The set is hashed (PostgreSQL) or indexed (SQLite) once per statement; each row reads its own elements. On PostgreSQL an array column keeps `col @> $1`, and a `jsonb` array path of strings, bools or numbers keeps containment where the condition is two-valued ([JSON paths](#json-paths)).

An array path is no sort key, and its facets are terms only; schema generation refuses either.

#### Arrays of objects

A JSON array of objects filters by its elements: every predicate inside one `$some` holds for one element, and `$none` holds when no element satisfies them. Element paths are declared like paths — the members tagged `parse:"filter"` of a slice-of-struct document's element type, or an `elements:"by:string;priority:int"` tag on any JSON array — and named by their member path inside the element (`dims.w`).

```text
{"items":{"$some":{"sku":"a","qty":{"$gt":2}}}}
postgres EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(orders.items) = 'array' THEN orders.items END) AS elem1__(v) WHERE (… elem1__.v ->> 'qty' … AND … elem1__.v ->> 'sku' …))
sqlite   EXISTS (SELECT 1 FROM json_each(CASE WHEN json_type(orders.items) = 'array' THEN orders.items END) AS elem1__ WHERE (… json_extract(…elem1__.value…, '$."qty"') …))
```

An element that is not an object has no members, so a mixed array (`["a", {"sku":"a"}]`) is tested on its objects, and a string element is a JSON string even when its text is an object's JSON. `filter.Evaluate` tests the condition on each element of the document as a `filter.ElementRow`, which reads element paths on the decoded element. A backend with its own evaluator of `$some` / `$none` does the same: it reads a document's elements with `filter.DocumentElements` and tests each with `filter.Evaluate(condition, filter.ElementRow{Element: e})`, or reads the condition's fields through that row. A `$some` / `$none` with no condition (`{}`) has no child and holds for any element. `filter.ElementRow` takes decoded JSON or a `filter.Document`, and refuses an element of any other type, as `filter.Evaluate` refuses an array a `filter.Row` holds as anything but `[]any`, or a document held as anything but decoded JSON. `filter.PathValue` reads a column's document, decoding a string as JSON text, so it refuses an element path.

An element predicate, on either kind of array, costs 10 (1 when the array is tagged `indexed`) plus its condition. `filtertest.RunArrays(t, schema, backend)` proves a backend's scalar arrays.

## Evaluating outside SQL

A backend that holds rows in memory evaluates a parsed filter with `filter.Evaluate(ast.Root, row)`, the reference evaluator: it returns the row's `filter.Truth` (`True`, `False` or `Unknown`) under the rules the SQL dialects render, and the row is selected only on `True`. `filtertest` runs every conformance case on it beside SQLite and PostgreSQL.

```go
row := filter.ColumnRow(func(column string) (any, bool) {
	v, ok := columns[column] // the Go values stored under each column
	return v, ok
})
truth, err := filter.Evaluate(parsed.AST().Root, row)
if err != nil {
	return err
}
selected := truth == filter.True
```

| Rule | Meaning |
|---|---|
| NULL | a comparison with a NULL value or an absent path member is `Unknown`; `$and`, `$or` and `$not` are three-valued, so `$ne`, `$nin` and `$not` never select it |
| Empty sets | `$in: []` is `False` and `$nin: []` `True`, NULL or not |
| Order | numbers and decimals by exact value; strings, UUIDs, timestamps and dates by code point |
| Documents and arrays | jsonb's `=` and `@>` (`core.JSONEqual`, `JSONContains`, `JSONHasKey`); an array column `$contains` every listed element; `$some` / `$none` over the elements, never Unknown |
| Patterns | `$like` / `$ilike` as `MatchLike`; `$contains` on a string is a case-sensitive substring |
| Geo | `$near` by the WGS 84 geodesic distance, `$within` / `$intersects` a box or polygon with its boundary, as PostGIS decides them on a JSONB point; a NULL point is `Unknown`; native `geography` / `geometry` storage is E2115 |
| Unknown | SQL's unknown (`filter.UnknownConstraint`) is `Unknown` |
| Not evaluable | a membership or raw-SQL predicate is `Unknown`, so it never admits a row its SQL would refuse; resolve memberships and relation predicates first with `filter.ResolveMemberships`, since an unresolved relation predicate is E2083 |

A `filter.Row` returns each field's value in the canonical domain filter values normalize to (`filter.Row` documents it). `filter.ColumnRow` reads a row stored as Go values by column — under `table.column` for a field's `Table` and `Column` or a path's document column, and under the expression itself for an `Expr` — converting each with `filter.ColumnValue` (drivers' `Valuer`s, decimals, times and UUIDs as SQL compares them; JSON text decoded) and each path with `filter.PathValue`; a column the row lacks is an error, never NULL. JSON text is decoded on every read, once for each condition on its paths; a store that evaluates many filters over the same rows decodes each document once with `filter.DecodeDocument` and returns the `filter.Document` (or a pointer to it) for its column, which every reader of a document (`PathValue`, `ColumnValue`, `DocumentElements`, `KeyValue`) takes as it takes the text. `filter.RowFunc` adapts any other source. Run `filtertest.Run(t, "memory", backend)`, `filtertest.RunPaths`, `filtertest.RunGeo`, `filtertest.RunMembership` and `filtertest.RunPolicy` (after `ResolveMemberships`) to prove a row adapter.

## Lists and keyset pagination

A list endpoint declares its resource once — `parse.NewResource[T](table, PrimaryKey, TenantColumn, DeletedColumn, PageSize, AcceptParams, ParserOptions)` — and serves each request in three steps:

1. `ParseList(url.Values)` reads every `filter.Param*` parameter into a `*ListRequest`: the JSON filter ANDed with the flat field parameters and bounded as one expression, sort, cursor, page size (missing: the default; above the maximum: clamped; malformed or not positive: E2067), `include_total` and `include_deleted` (booleans; anything else is E2067), search (E2067 on a resource without search fields), facets (unknown or not facetable: E2071), includes (unknown: E2071; not an includable relation: E2083) and the [field mask](#field-mask) (a name that is no field of the response: E2071). A parameter that is none of these, not a filterable field and not accepted with `AcceptParams` is E2071, so a misspelt filter cannot return an unfiltered list. Off HTTP — an RPC, a message, a saved view — build a `ListInput` (the JSON filter, `FieldFilters` keyed `field` or `field_<op>`, sort, cursor, page size, flags and name lists) and call `ParseListInput`: `ParseList` is that call behind a query-string adapter, with the same limits and codes; a `ListInput` page size of zero is the default and a negative one E2067.
2. `BuildList(dialect, req, parse.Scope{Tenant, Governance, Policy, IncludeDeleted})` compiles it under the server's decisions. It compiles only a request the same resource parsed: a `ListRequest` holds its fields behind methods (`Filter()`, `Sort()`, `PageSize()`, `Fields()` and the rest) and the schema that parsed it, and another resource's request, a `WithPaths` view's or its base's included, is `parse.ErrForeign`. It applies the tenant floor (or governance in its place), soft-delete masking unless `IncludeDeleted`, and the policy, ANDed with the request; the key as the final keyset column; the count and facets when asked; [computed columns](#computed-columns) as the dialect's expressions. A tenant-scoped resource without `Scope.Tenant` or `Scope.Governance` is `ErrTenantRequired`; a governed list holds each row's relations to the row's own tenant instead ([Governed lists](relations.md#governed-lists)). `Scope.Tenant` is the tenant key's text, normalized once and bound as one value at every tenant column the resource reaches, its relations' at any depth included ([Tenant key](relations.md#tenant-key)): a string, a UUID's canonical text, or an integer from its decimal digits with an optional minus sign (`"44"`), range-checked against every integer column's Go kind. Text one of those columns cannot hold is `ErrTenantInvalid`, the server's mistake rather than the request's. The `*ListPlan` holds `Page` (the full `SELECT` of the resource's `Columns`), `Fragment` (its `WHERE … ORDER BY … LIMIT` alone, for a caller's own projection with `Page.Args`; a value the table holds in no column is usually better declared with `parse.ReadExpr`, which `Page` then selects), `Count`, `Facets`, `Include` and `Keyset`.
3. `Page(rows, plan)` (or `PagePointers`) builds the page from the fetched rows. `IncludeQueries(plan, keys)` returns the include statements for the page's keys (`Key(row)`), tenant-scoped as the list was; read each one's rows with its `Scan` into `Page.Relations` (and `Page.Truncated` for owners cut at the cap).

### Field mask

`fields` (`ListRequest.Fields()`) names the members each item of the page returns, by name under the field-name tag (`json` by default). `ParseList`, `ParseListInput` and `ParseListBody` refuse a name that is no field of the response with E2071 and `{"field": name}` in the details, as they refuse an unknown facet, so `?fields=nmae` fails instead of returning items without the member the client expects. A field of the response is a field whose column the resource reads, a [computed column](#computed-columns) included, or a field with no column (`db:"-"`, a relation field), which the service fills itself. A field the field-name tag hides (`json:"-"`) is no field of the response, and neither is a field whose column `ReadColumns` leaves out, query-only columns included: it is zero in every item. The error for either reads as it does for a name the type lacks, so it does not tell a client which fields the server withholds. A mask names top-level members; a path into a document is no field.

The mask does not narrow the query. The plan's `Page` statement selects every column the resource reads whatever the mask names, and `Page(rows, plan)` returns whole items: the service applies the mask, dropping the members it leaves out when it writes the response. An empty mask returns every field.

### Policy fields

A server policy (`Scope.Policy`, `Scope.Governance`, `Scope.Related`, `ListPolicy`) can name fields a request cannot. A constraint's `Field` that names a system field, the key, tenant or soft-delete column the resource declares, resolves to it. Any other name must designate exactly one of:

- a request field (`parse:"filter"`), by its name;
- a field without the filter capability, by its external name;
- any field with a column, by that column, hidden fields (`json:"-"`) and request fields included.

A relation field has no column, and neither has a field whose column tag is `-`. Requests still reach only request fields, and the schema document advertises nothing more. A policy's operator must apply to the field's type, but need not be one of its request operators.

A name that designates more than one field is refused with `filter.ErrAmbiguousPolicyField` whichever field is declared first, and the error names each field and column. Such a name is one field's name and another field's column (a visible `region` stored in `region_code` beside a hidden field stored in `region`), the name of two fields, or a column two fields hold that is neither's name. Each of those fields stays reachable by its other name (`region_code` above), if it has one. A field whose type a filter field could not take (a `storage` tag on an array, a malformed `geo` tag) does not stop the resource from building; a policy that names it returns that field's error. A name no field has is E2071. `ResourceSchema.PolicyField(name)` resolves a name as a policy does, so a server can check its policies' fields at startup, and `Relate` refuses a relation name a policy already resolves.

### Policy constraints

A `filter.PolicyConstraints` is a group: its `Constraints` and `And` groups are ANDed, its `Or` groups are ORed and ANDed with them, and `Not` negates the whole group. A group with nothing in it constrains nothing and is dropped. A negated group is never empty (`IsEmpty`), so one with nothing to negate is refused with E2072 wherever it stands, alone, merged (`Merge`) or composed (`ComposeConstraints`), instead of admitting every row. One constraint is one of:

| Constraint | Compiles to |
|---|---|
| `Field`, `Operator`, `Value` | The comparison the filter grammar writes, the value normalized as a request value is. `OpNull` and `OpNotNull` take no value or a boolean, which means what it means in `$null` and `$not_null` (`OpNull` with `false` is `IS NOT NULL`); `OpExists` takes a boolean; any other value is E2073 |
| … with `UnknownIfInvalid` | The same, except that a value the field's type or the operator cannot take is SQL's unknown instead of an error |
| `filter.UnknownConstraint()` | SQL's unknown (`filter.OpUnknown`, `NULL`): no row satisfies it, nor its negation. `BuildBaseConstraints` writes it for a tenant floor without a tenant |
| `Member` | `Field` among the values a `Lookup` projects ([evaluated outside SQL](#evaluating-outside-sql) through `ResolveMemberships`); every constraint sharing one lookup tests its own field, and `ResolveMemberships` asks the lookup once. A `WhereIn` list binds as one [set](#sets) of its values' Go type (`core.TypeOf`): one scalar type per list, anything else `ErrPolicyInvalid` |
| `RawSQL`, `RawArgs` | The fragment in parentheses, so `a OR b` stays one operand of the floor, the cursor, a search or a relation's correlation; its `$N` placeholders are renumbered. Only SQL evaluates it: the memory evaluator reads it as Unknown, and a target read under an alias (a relation back to its own table, a policy-scoped relation's include or facet) refuses it |

`Negate` negates one constraint.

Underneath, `NewListQuery(filterSchema, sortSchema, dialect)` returns a `*ListQuery` (or the [key](#configuration) error), whose `Build(opts...)` returns a `*ListResult`:

| Field | Content |
|---|---|
| `Fragment` | `WHERE … ORDER BY … LIMIT n` to append to `SELECT … FROM <table>` |
| `Where`, `OrderBy`, `Args` | The parts, for custom composition |
| `FetchLimit` / `Limit` | Rows to fetch (limit + 1) / the requested page size |
| `Cursor` | The keyset plan `NewPage` needs |
| `CountSQL`, `CountArgs` | `SELECT COUNT(*)` with the same WHERE (`ListWithCount`) |
| `Facets` | One `WITH base AS … UNION ALL` facet query (`ListFacets`); scan with `ScanFacetUnion` |
| `Sort` | The parsed sort |

`NewPage(rows, res.Limit, res.Cursor, value)` truncates the fetched rows, restores presentation order after a backward seek, sets `has_next_page` / `has_prev_page`, and mints `start_cursor` / `end_cursor` (only toward pages that exist): a flag is set exactly when its cursor is. An empty page sets neither, even after a seek: a cursor replayed after the rows on its side were deleted, or under another filter, has no row to mint a cursor from, so the client starts again from the first page. `value(row, column)` resolves a key column; `CursorValueByTag(row, "db", column)` does it by struct tag. Keys are the sort fields plus the primary key declared on the resource; NULL sorts greatest (`NULLS LAST` ascending, `NULLS FIRST` descending) and seeks are NULL-aware. Cursors carry a generation and are capped at 4 KiB; a token from another sort or generation fails with E2082.

A statement the list builder does not write, such as a search ordered by a computed rank, pages the same way from its own keys. Each `filter.CursorKey` names the key, what it orders by (`Column` of `Table`, which the dialect quotes, a computed `Expr`, or a document member's `Path`), the result column a token reads it from (`Column`), its type and direction, and whether it can be NULL. `filter.OrderByKeys(dialect, keys, reverse)` renders the `ORDER BY`; `filter.SeekPredicate(dialect, keys, token, bind)` checks the token against the keys (`ValidateCursor`, E2082 for a token minted under other keys), normalizes its values and renders the rows strictly after it, binding every value through `bind`; `CursorPlan{Keys: keys}.Token` mints the next token from a page's last row. Keys may mix directions; NULL placement and seeks follow the rules above.

Facets name facetable fields; any other name is E2071, like an unknown filter field. `NewResource` and `WithPaths` refuse a facet, as they refuse a sort key, over a column `parse.ReadColumns` leaves out: its buckets would carry the withheld values. A filter, search or relation correlation over such a column is refused too (by `Relate` for a declared relation), since which rows match discloses the values, unless `parse.QueryOnlyColumns` names the column. A relation facet counts distinct owners per target, and `BuildIncludeQuery` returns distinct `(owner, target)` pairs, at most `WithMaxIncludePerRelation` per owner, so two junction rows pairing the same rows (typed edges) count once. Search covers string fields; schema generation refuses `search` on any other type unless a `ColumnMapper` supplies the expression. On PostgreSQL an array argument binds as a typed Go slice (`[]string`, `[]int64`, `[]float64`, `[]bool`, `[]time.Time`; UUIDs as their canonical text), which pgx encodes in every query mode, including `QueryExecModeExec` and the simple protocol.

### Computed columns

A row often carries a value no column of its table holds: a count of related rows, a many-to-many's keys as an array, a nullable column read as text. Declare it with `parse.ReadExpr(column, expr)`, where `expr` is a `parse.ColumnExpr` — `func(filter.Dialect) (string, error)` — returning the column's trusted SQL for one dialect, or an error for a dialect it cannot serve. `parse.SQL(sql)` is the expression every dialect reads alike. A resource is declared once, before any dialect is known, and serves each dialect through that dialect's expression:

```go
var accounts = parse.MustResource[Account]("accounts",
	parse.TenantColumn("tenant_id"),
	parse.ReadExpr("member_count", parse.SQL("SELECT count(*) FROM memberships m WHERE m.account_id = accounts.id")),
	parse.ReadExpr("group_ids", func(d filter.Dialect) (string, error) {
		switch d.(type) {
		case filter.PostgresDialect:
			return "array(SELECT g.group_id FROM account_groups g WHERE g.account_id = accounts.id ORDER BY g.group_id)", nil
		case filter.SQLiteDialect:
			return "SELECT json_group_array(group_id) FROM (SELECT g.group_id FROM account_groups g WHERE g.account_id = accounts.id ORDER BY g.group_id)", nil
		}
		return "", filter.ErrUnsupportedDialect
	}),
)
```

- `Columns(d)` selects the column as `(sql) AS column`, composed with a declared `ColumnType` read cast, so `plan.Page`, an update's `RETURNING` and `parsepgx` scan it into the column's field by name.
- The column's `filter`, `sort`, `facet` and `search` tags compile against d's expression in `BuildList`, `BuildUpdate` and `BuildUpdateBatch`, and so does a server policy that names the column ([policy fields](#policy-fields)), on the resource's own statements and as a relation's target (`Scope.Related`), a hidden or unfilterable column included; a cursor reads a computed sort key's value from the column's field. Every other part of the statement, such as a sort's collation, follows the dialect the build is given.
- An update that changes relations runs `Owner` before the relation statements, so a computed column over a relation's pairs reads them as they were; `Reread(plan, keys)` reads the rows again once the statements have run, and `parsepgx.Update` runs it.
- A dialect an expression refuses, or answers with empty SQL, fails `Columns`, `BuildList` (and with it the page, count, facets and includes), `BuildUpdate` and `BuildUpdateBatch` before any statement exists. The error names the table, the column and the dialect, and wraps the expression's own error: `parse: accounts: computed column "group_ids" for dialect sqlite: unsupported SQL dialect`.
- `NewResource` refuses, whatever the dialect, a computed column that is not a column of T or not read (`ReadColumns`), that is the key, tenant or deleted column, that a patch path writes, or that declares document paths or element members, and a `relation` tag correlating on one; `Relate` refuses a relation correlating on one, `Resource.WithPaths` a request's path into one, and `BuildUpdate` and `BuildUpdateBatch` a `Stamp`, `StampSQL` or `Increment` of one.
- A relation to the resource filters by its computed columns too: the owner's build compiles the relation's filter, at any depth, the target's policy (`Scope.Related`) and the relation's facets, includes and membership changes against each target's expressions for the owner's dialect, and fails, naming the target's table, column and dialect, for a dialect a target it reaches refuses. Builds that reach no such relation are unaffected. Where the statement reads the target under an alias of its own — a relation back to a table already in the statement, as in a cycle, and a policy-scoped relation's includes, facets and membership changes — an expression naming the table cannot follow, and the build fails instead of reading a column the table lacks.
- `FilterSchema`, `SortSchema`, `SearchSchema`, the schema document and discovery stay dialect-neutral and name the column. The filter schema carries the expressions (`filter.ResourceSchema.Computed`), and every statement resolves them for the dialect it is built for, `ListQuery`'s included. `CustomizeSchemas` cannot declare one; `ReadExpr` does.

`expr` runs on every build, so it must be cheap and return the same SQL for the same dialect. A build calls the expressions of each schema it compiles: the resource's own, and those of every relation target its filter, policies, facets, includes or membership changes reach, following the request rather than the relation graph, so relations in a cycle cost no more than the filter's depth. The schemas hold no per-dialect state, so concurrent builds share them without a cache or lock, and each build compiles under its own dialect value: two `PostgresDialect`s that differ only in `Collation` read one expression and order it each by its own collation. Declare relations before serving.

## Search and facets

`filter.ListSearch` adds a predicate without changing sort order. PostgreSQL uses `to_tsvector` / `plainto_tsquery`; SQLite uses a LIKE phrase match. These have different tokenization and matching behavior. Tag string fields with `parse:"search"`; search never orders the page, and the PostgreSQL text search configuration is `english` unless `core.WithSearchLanguage` names another. A custom expression must produce text for the chosen dialect.

`filter.ListFacets` builds aggregations for fields tagged `parse:"facet"`. Use `facet:"terms:20"` for the top terms or `facet:"range:0-50,50-100,100+"` for ranges. `ListWithCount` and facets share the filter, policy, and search population, excluding the page cursor. Execute the returned statements separately and scan the facet union with `filter.ScanFacetUnion`. The base page does not automatically execute counts, facets, or includes.

`filter.ResolveInclude(ctx, field, ownerKeys, lookup, opts...)` resolves the same include projection on non-SQL stores. The `IncludeLookup` supplies a batched iterator of owner/target pairs matching the lookup predicate; parse resolves target visibility, orders distinct keys and reports truncated owners. The SQL and non-SQL paths share the include options.

## Configuration

A `parse.Resource` declares its primary key once with `parse.PrimaryKey` (default `id`). Every list, including `Resource.ListQuery`, appends that key automatically. A hand-built filter schema declares `ResourceSchema.PrimaryKey` before constructing a `ListQuery`, naming a typed column of the sort schema, its `Fields` or its `SystemFields`; `NewListQuery` returns an error for a schema without one, for a key that is no field, declares no type or is computed, and for a nil schema, since a keyset that does not end on a unique typed column skips or repeats rows. `GenerateFilterSchema` declares no key: the resource does. The key is schema metadata, never a request option.

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
| `ListPolicy(pc)` | `*PolicyConstraints` | none | Server-side constraints ANDed with the filter; each names a [policy field](#policy-fields) |
| `ListNow(t)` | `time.Time` | one reading of the config's clock | The instant the `ListFilter` JSON, the policy and the related policies resolve relative tokens against: the request's |
| `ListRelationScope(s)` | `RelationScope` | none | How the filter, count and facets reach related rows ([relation scope](#relation-scope)) |
| `ListStartParam(n)` | int | `0` | Offset the main fragment after `n` earlier arguments; standalone count/facet statements keep independent numbering. Negative is an error from `Build` |

### Relation scope

Every statement that reaches related rows takes one `filter.RelationScope` through its builder's one option (`WithRelationScope`, `ListRelationScope`, `WithFacetRelationScope`, `WithIncludeRelationScope`, `ResolveRelationScope`), so a page, its count, facets and includes reach the same rows. `parse.BuildList` builds it from `parse.Scope`: `Tenant` from `Scope.Tenant`, `RowTenant` from the resource's tenant column under `Scope.Governance`, `Related` from `Scope.Related`.

| Field | Type | Default | Meaning |
|---|---|---|---|
| `Tenant` | any | nil | The tenant every tenant-scoped relation is bound to, in its tenant column's type |
| `RowTenant` | string | "" | Hold each tenant-scoped related row to the tenant of the owner row it hangs from, this column of the owner's table, for rows of several tenants ([governed lists](relations.md#governed-lists)); excludes `Tenant` and `Unscoped`. `ResolveMemberships` and `ResolveInclude` refuse it: resolve each tenant's rows with its `Tenant` |
| `Unscoped` | bool | false | Reach tenant-scoped relations without a tenant predicate — platform tooling only |
| `Related` | `map[string]*PolicyConstraints` | nil | The server's policy for each policy-scoped relation's target table, by table; a predicate on one without it is `ErrRelatedPolicyRequired` |
| `Now` | `time.Time` | zero | The instant `Related`'s relative tokens resolve against, the request's (`parse.BuildList` sets it); zero is the list's own instant in `ListQuery.Build`, one wall-clock reading per statement in the other builders |

The zero value scopes no tenant: a relation declaring a tenant column fails closed.

`SQLBuilderOption`s for `NewSQLBuilder`:

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithStartParam(n)` | int | `0` | First placeholder is `n+1` (e.g. after an UPDATE's SET arguments); negative is an error from `Build` |
| `WithRelationScope(s)` | `RelationScope` | none | How the filter reaches related rows ([relation scope](#relation-scope)) |

Facet, include, and flat-query options:

| Option | Type | Default | Description |
|---|---|---|---|
| `WithFacetRelationScope(s)` | `RelationScope` | none | How relation facets reach related rows; a `RowTenant` is projected beside the owner's keys. Each relation facet joins its junction on its own relation's owner key (`RelationSchema.OwnerKey`), each distinct key projected once |
| `WithFacetFrom(from)` | string | the schema's table | The facets' base rows: an aliased table or a join whose columns the filter and the fields name. A relation facet still reads the owner's key as `Name.<pk>` |
| `WithIncludeRelationScope(s)` | `RelationScope` | none | How the include reads related rows; a `RowTenant` joins the owner's table, which `WithIncludeOwner` names. `ResolveInclude` refuses a `RowTenant` |
| `WithIncludeOwner(owner)` | `*ResourceSchema` | none | The owner's schema, whose table a `RowTenant` reads |
| `WithMaxIncludePerRelation(n)` | int | `100` | Targets per owner; distinct keys capped in SQL or non-SQL resolution. The cap must leave room for the truncation sentinel |
| `QueryParamAccept(names...)` | `...string` | none | Parameters the endpoint reads itself; left alone like the list parameters |
| `QueryParamLenient()` | — | off | Ignore a parameter naming no filterable field instead of refusing it (E2071) |
| `QueryParamConfig(cfg)` | `*core.ParserConfig` | `core.DefaultConfig()` | Limits and value configuration for flat filters |
| `ListQuery.WithFilterConfig(cfg)` | `*core.ParserConfig` | default parser config | Configure JSON filter parsing before using the builder |
| `ListQuery.WithSearchSchema(schema)` | `*SearchSchema` | nil | Supply searchable fields for `ListSearch`; `Resource.ListQuery` does this automatically |

A relation that declares a `tenant_col` fails the build unless a tenant is bound or an unscoped option is given — junction rows of other tenants never match.

A flat parameter that is neither a list parameter (`filter.Param*`), an accepted name nor a filterable field is E2071 on every entry point — `Resource.ParseList` (`AcceptParams`), `Resource.ParseRequestFilter` and `filter.NewQueryParamParser` (`QueryParamAccept`) — so a misspelt filter cannot return every row.

The dialects are `filter.PostgresDialect` and `filter.SQLiteDialect`, and the set is closed: `filter.Dialect` has unexported methods, so no type outside the module implements it, and every dialect-specific choice is made on which of the two a dialect is. `filter.DialectFromDSN` picks one from a connection string. Application code can still type-switch on the two types, as the computed-column example below does.

PostgreSQL dialect:

| Field | Type | Default | Description |
|---|---|---|---|
| `PostgresDialect.Collation` | `filter.PostgresCollation` | `CollationPgCUTF8` | Code-point collation for string sorts, keyset seeks and string ranges: `CollationPgCUTF8` (`pg_c_utf8`, PostgreSQL 17+) or `CollationC` (`"C"`, PostgreSQL 14–16). Both order by code point; index the sorted columns with the same collation |
| `PostgresDialect.ContainmentMaxValues` | int | `0` (`DefaultContainmentMaxValues`, 256) | Largest `$in` set a jsonb path compares by containment, one GIN probe per member; a larger set joins its members unnested. Negative compares no set by containment (`$eq` still does) |

## Request-to-SQL examples

The examples compile against this resource ([source and executable outputs](../examples_sql_test.go); below, long UUIDs and repeated predicates are shortened with …):

```go
type Product struct {
	ID          uuid.UUID      `json:"id"          db:"id"          parse:"filter,sort"`
	Name        string         `json:"name"        db:"name"        parse:"filter,sort,patch,search"`
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
postgres WHERE (products.category = $1 AND products.name = ANY($2)) ORDER BY products.created_at DESC [tools [saw drill]]

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
facets: WITH base AS MATERIALIZED (SELECT products.category AS facet__0 FROM products WHERE (…)) SELECT facet_field, facet_key, facet_count FROM (SELECT 'category' AS facet_field, k AS facet_key, c AS facet_count, 0 AS facet_ord, ROW_NUMBER() OVER (ORDER BY c DESC, k COLLATE pg_c_utf8) AS facet_rank FROM (SELECT facet__0 AS k, COUNT(*) AS c FROM base WHERE facet__0 IS NOT NULL GROUP BY facet__0 ORDER BY c DESC, facet__0 COLLATE pg_c_utf8 LIMIT 10) s0) facets__ ORDER BY facet_ord, facet_rank [200 cordless drill]
```

Facet rows come back in a fixed order: facets as requested, a terms facet's buckets by count with ties in the field's type order, numbers by value (a decimal exactly) and other keys in code-point order — the same order decides which buckets a terms limit keeps — and a range facet's buckets as declared. A terms facet on a list column (`[]string`, `[]int64`, …) counts each distinct element once per row: `["a","a"]` is one row for `a`. A list page carries the facets as `facets`, keyed by field (`filter.FacetMap`); a backend that counts in Go orders them with `filter.FacetBuckets`, given the field.

A bucket's key is the value's canonical text, the same on every backend: a string as itself, a boolean as `true` / `false`, a date as `YYYY-MM-DD`, a UUID lower-cased, a timestamp as its canonical text (`core.FormatTimestamp`), one bucket per instant whatever offset each row was written in ([timestamps](schema-configuration.md#timestamps)). A number is its canonical decimal text (`core.DecimalText`: plain notation, no trailing fractional zeros, `0` for zero), so `7`, `7.0` and `7e0` are one bucket, `7`. A path member's number is its value exactly as written (`numeric` on PostgreSQL, `parse_decimal` of the member's literal on SQLite); a float column's is its shortest form at the field's precision, a `float32` field's as a float32. PostgreSQL's float text is that form while `extra_float_digits` is positive, its default; SQLite keys a float column through `parse_decimal` / `parse_decimal_float32` ([`parsesqlite`](../parsesqlite/README.md)). A NaN or an infinity has no bucket, nor has an infinite PostgreSQL timestamp. `filter.FacetKeys(value, field)` gives a value's keys, each distinct element of a list once, for a backend that counts in Go.

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

Schema-backed cursor keys validate type, numeric range, and nullability before SQL construction (E2082). Manually constructed schemas need `CursorKey.NumberKind` / `SortFieldSchema.NumberKind` for numeric bounds. A `ListQuery`'s key is always typed; a keyset composed by hand (`Keyset`, `ValidateCursor`) with a key that declares no type validates canonical non-null scalars only. A decimal key carries its canonical text and orders by value on every backend ([decimals](schema-configuration.md#decimals)). Other pointer-receiver `driver.Valuer` / text-marshaler scalar keys can supply canonical values, but the package cannot infer a custom database scalar's ordering from its Go encoding: declare such a column with the scalar it holds (`storage:"scalar:decimal"`), or choose keys with a known consistent SQL/Go representation. Cursors carry sort metadata, not authorization: reapply server policy on every page.

[HTTP parameters](http-errors.md) · [JSON documents](patches.md#json-documents) · [Relations](relations.md)
