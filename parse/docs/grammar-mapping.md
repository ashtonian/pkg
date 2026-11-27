# Mapping from PostgREST, django-filter and ent

[Package overview](../README.md)

This page maps parse's request grammar onto three tools a reader may already know: its filter operators, flat parameters, list parameters, sorting, keyset paging, relation predicates and patches. The three are different kinds of tool. PostgREST serves a PostgreSQL schema over HTTP with a URL grammar of its own. django-filter turns query parameters into Django ORM lookups, and Django REST framework (DRF), its usual companion, supplies the ordering, search and pagination django-filter leaves out, so this page cites DRF where django-filter has no counterpart. ent is a Go ORM: its predicates and builders are Go code, not a wire format.

Every cell about another tool comes from the [sources](#sources) at the end: the PostgREST 16 documentation, django-filter 26.2 over Django 6.1's lookups, DRF 3.18 and ent v0.14.6. A dash means those sources show no counterpart, not that custom code (a PostgREST function, a django-filter `method`, an ent `sql.P` predicate) cannot build one. The parse side is the code's: [filter grammar](queries.md#filter-grammar), [HTTP contract](http-errors.md#request-parameters), [relations](relations.md) and [patches](patches.md) hold the full rules.

## Filter operators

A parse operator is a JSON key in the `filter` document (`{"price":{"$gte":10}}`) or, for most comparisons, a flat query parameter (`price_gte=10`); both compile to the same filter. In the flat column `f` stands for any filterable field. PostgREST writes the operator in the value (`price=gte.10`), django-filter in the parameter name after a double underscore (`price__gte=10`, one filter per lookup the `FilterSet` declares), and ent as a generated Go predicate (`user.AgeGTE(10)`). The ent cells use the names ent generates: for its `start` example's `User`, with `Name` and `Age` fields and a `Cars` edge, and for the predicates guide's `Pet` and its `Owner` edge.

A row is every operator the grammar reads: those a field may declare (`filter.Operators`, by type `filter.OperatorsFor`) and the relation predicates and logical operators. `TestDocTables` renders the first two columns from the code, so an operator added to the grammar adds a row whose other cells must be written before the test passes.

<!-- BEGIN rendered mapping-operators: edit the code, then go test -tags=unit -run TestDocTables -update -->

| Operator | Flat parameter | PostgREST | django-filter | ent |
|---|---|---|---|---|
| `$eq` | `f` | `f=eq.v` | `exact`, the default: `f=v` (`exact` is never added to the name) | `user.NameEQ(v)`, or `user.Name(v)` |
| `$ne` | `f_ne` | `f=neq.v` | `exact` with `exclude=True` | `user.NameNEQ(v)` |
| `$gt` | `f_gt` | `f=gt.v` | `gt`: `f__gt=v` | `user.AgeGT(v)` |
| `$gte` | `f_gte` | `f=gte.v` | `gte`: `f__gte=v`; `range` for both bounds, inclusive | `user.AgeGTE(v)` |
| `$lt` | `f_lt` | `f=lt.v` | `lt`: `f__lt=v` | `user.AgeLT(v)` |
| `$lte` | `f_lte` | `f=lte.v` | `lte`: `f__lte=v` | `user.AgeLTE(v)` |
| `$in` | `f_in` | `f=in.(a,b)` | `in` through a `BaseInFilter`: `f__in=a,b` | `user.NameIn(a, b)` |
| `$nin` | `f_nin` | `f=not.in.(a,b)` | `in` with `exclude=True` | `user.NameNotIn(a, b)` |
| `$like` | `f_like` | `f=like.a*`, `*` standing for `%` | — (no pattern lookup): `startswith`, `endswith`, `contains`, or `regex` | `sql.Like` in a custom predicate; `user.NameHasPrefix`, `user.NameHasSuffix`, `user.NameContains` |
| `$ilike` | `f_ilike` | `f=ilike.a*` | `istartswith`, `iendswith`, `icontains`, `iexact`, or `iregex` | `user.NameEqualFold`, `user.NameContainsFold` |
| `$contains` | `f_contains` | string: `f=like.*x*`; otherwise `cs` (`@>`): `f=cs.{a,b}` on an array | string: `contains`; `JSONField` and PostgreSQL's `ArrayField`: `contains` (`@>`) | string: `user.NameContains(x)`; JSON: `sqljson.ValueContains` |
| `$has_key` | — | — (the operator table has no `?`) | `JSONField`'s `has_key`; also `has_keys`, `has_any_keys` | `sqljson.HasKey` |
| `$null` | `f_null` | `f=is.null` | `isnull` through a `BooleanFilter`: `f__isnull=true` | `user.NameIsNil()`, on an optional field |
| `$not_null` | `f_not_null` | `f=is.not_null`, or `f=not.is.null` | `f__isnull=false` | `user.NameNotNil()` |
| `$exists` | — | `$exists: true` is `is.not_null`, `false` is `is.null` | `isnull`, reversed | `NotNil` / `IsNil` |
| `$near` | — | — (no geo operator) | GeoDjango `dwithin` or `distance_lte` | — |
| `$within` | — | — | GeoDjango `coveredby`, which counts the boundary as parse does; `within` | — |
| `$intersects` | — | — | GeoDjango `intersects` | — |
| `$some` | — | array: `f=ov.{a,b}` is `{"$some":{"$in":["a","b"]}}`; relation: an embed with filters, made `!inner` or tested `r=not.is.null` | relation: a lookup through it, `r__field=v`; array: `ArrayField`'s `overlap` | relation: `user.HasCarsWith(…)`; array elements: — |
| `$none` | — | relation: `r=is.null` on an embed with filters, an anti-join | relation: `exclude=True` on a lookup through it | `user.Not(user.HasCarsWith(…))` |
| `$is` | — | to-one: an `!inner` embed with filters | a lookup through the foreign key: `fk__field=v` | `pet.HasOwnerWith(user.Name(v))` |
| `$is_not` | — | to-one: `r=is.null` on an embed | `exclude=True` on that lookup | `pet.Not(pet.HasOwnerWith(…))` |
| `$and` | — | separate parameters, or `and=(…)` | separate filters, applied one after another | `user.And(…)` |
| `$or` | — | `or=(age.lt.18,age.gt.21)`; across relations, `not.is.null` / `is.null` of filtered embeds inside it, an aliased embed per set of conditions | — on the wire: OR-ed `Q` objects in a filter's `method`; a `MultipleChoiceFilter` ORs its choices unless `conjoined=True` | `user.Or(…)` |
| `$not` | — | `not.` before any operator, `not.and=(…)` | `exclude=True`; a negated `Q` in a `method` | `user.Not(…)` |

<!-- END rendered mapping-operators -->

### Flat parameters

A flat parameter is a field's name, alone for `$eq` or followed by the operator's suffix, ANDed with the `filter` document:

- `status=a&status=b`, a repeated equality, is the set `{"status":{"$in":["a","b"]}}`; `status_in=a,b` splits one value at its commas; `stock_null=true` takes a boolean.
- Any other repeated parameter (`price_gte=1&price_gte=2`) is E2072, and so are two parameters that set one operator on one field (`status=a&status=b` beside `status_in=c`).
- A parameter that is no filterable field, no list parameter and no parameter the endpoint reads itself (`parse.AcceptParams`) is E2071: `offset=10`, or a misspelt field, fails instead of widening the list.
- A field whose own name ends like a suffix (`email_like`) is that field; the exact name wins.
- The flat form has no `$or`, `$not`, `$some`, `$none`, `$is`, `$is_not`, `$has_key`, `$exists` or geo operator; those take the `filter` document. A relation field takes its key comparisons flat (`commands=c1`).

PostgREST's form is the nearest: one parameter per condition. django-filter names each lookup in the parameter (`price__gte`) and reads only the filters its `FilterSet` declares; it treats an empty value as no filter. ent has no wire form.

## Semantics that differ

The same spelling can select different rows. These are the differences a port has to check:

- **NULL.** parse follows SQL's three-valued logic everywhere: `$ne`, `$nin` and `$not` never select a row whose field is NULL, and `{"f":null}` (or `$eq: null`) is E2073; ask with `$null`, `$not_null` or `$exists`. PostgREST's `isdistinct` treats NULL as a comparable value; parse has no such operator. Django reads `exact` with `None` as `IS NULL`, but django-filter skips an empty value rather than passing it, so a query string reaches NULL through `isnull`.
- **Documents and NULL.** A parse JSON path's member is absent, and so SQL NULL, when the key is missing, holds JSON `null`, or holds another JSON kind than the path's type; `$null` on the whole document tests the column. Django distinguishes SQL `NULL` (`isnull=True`) from JSON `null` (`JSONNull()`), and a key's `isnull` finds a missing key. ent's `sqljson.ValueIsNull` is true for JSON `null`, not for a database NULL.
- **Empty sets.** `$in: []` matches no row and `$nin: []` every row, NULL or not; the set binds as one parameter whatever its size ([sets](queries.md#sets)).
- **Patterns and case.** `$like` is case-sensitive on both backends (SQLite runs it as GLOB); `%` is any run, `_` one character, `\` escapes, and `*` is a literal asterisk, where PostgREST reads `*` as `%`. `$ilike` lower-cases each character as PostgreSQL's ILIKE does under `pg_c_utf8`, and ASCII letters only on SQLite and under `filter.CollationC`. `$contains` on a string is a literal, case-sensitive substring on every backend. Django's `contains`, `startswith` and `endswith` act like their case-insensitive forms on SQLite, and its `iexact` and `icontains` do not fold non-ASCII letters there.
- **String order.** Ranges, sorts and keyset seeks compare strings by code point (`pg_c_utf8` on PostgreSQL 17+, `"C"` with `filter.CollationC`, `BINARY` on SQLite), not by the database's default collation, so the database, `filter.Evaluate` and a cursor's seek order strings alike. PostgREST's operators are the SQL operators of its table, and Django's lookups compile to SQL, so both compare with the column's collation.
- **Numbers and decimals.** Values are type-checked before any SQL exists: a float or int field takes a JSON number (`"1.5"` is E2073) unless it is tagged `json:",string"` ([quoted values](schema-configuration.md#quoted-values)), integers stay exact (a float beyond ±2^53 is refused), and a decimal field takes a number or a numeric string and compares by exact value on every backend, SQLite included, through `parsesql`'s functions ([decimals](schema-configuration.md#decimals)).
- **Arrays.** `$contains` holds when the array holds every listed element (`@>`, PostgREST's `cs`, `ArrayField`'s `contains`). `$some` and `$none` test one element at a time against a condition, every predicate on the same element (`{"scores":{"$some":{"$gte":5,"$lt":10}}}`); overlap is `$some` with `$in`. parse has no contained-by (`cd`, `contained_by`) and no length test (`ArrayField`'s `len`, `sqljson.LenEQ`).
- **JSON documents.** A plain object on a document field is whole-document equality, not a partial match; `$contains` is containment (`@>`) and `$has_key` jsonb's `?`. A member filters only once a path declares it ([JSON paths](queries.md#json-paths)): PostgREST's `->` / `->>`, Django's key lookups (`data__owner__name`) and ent's `sqljson.Path` reach any key.
- **Relations.** Every predicate inside one `$some` holds for the same related row, at any position: under `$or`, under `$not`, and two levels deep (`devices → commands → receipts`). Django's own rule gives that only to conditions inside one `filter()` call, and a django-filter `FilterSet` calls `filter()` once per filter, so two filters on one multi-valued relation may each match a different related row; an `exclude()` call's conditions need not hold for one row either. PostgREST's embedded filters narrow the embedded rows only, until the embed is `!inner` or tested with `not.is.null`, and each embed holds one set of conditions, so a second set on one relation takes an aliased embed.
- **Geo.** `$near` takes a distance with a unit (`"5km"`) and measures it on the WGS 84 ellipsoid, as PostGIS measures geography. GeoDjango's `dwithin` takes a `Distance` only on projected geometries, and the field's own units, degrees for WGS 84, on geographic ones.
- **Time.** Timestamps normalize to RFC 3339 UTC, and relative tokens (`now-7d`, `now+1h`) resolve against one reading of the parser clock for the whole request. Django's date-part transforms (`year`, `month`, `date`) have no counterpart; write the range.

## List parameters

A list endpoint reads the `filter.Param*` parameters ([request parameters](http-errors.md#request-parameters)). Each is given once: a repeat is E2067 naming it, where `q.Get` would read the first and drop the rest. `facets`, `fields` and `include` take their names comma-separated in one value. The search parameter is `search`, or the name `core.WithSearchParam` gives the resource, `search` staying an alias; both in one request is E2067 ([search parameter](http-errors.md#search-parameter)). The same request may be a JSON body on `POST` when a URL would be too long ([the same request as a POST body](http-errors.md#the-same-request-as-a-post-body)). `TestDocTables` renders a row per `filter.Param*` constant.

<!-- BEGIN rendered mapping-list-parameters: edit the code, then go test -tags=unit -run TestDocTables -update -->

| Parameter | PostgREST | django-filter and DRF | ent |
|---|---|---|---|
| `filter` | — : a parameter per condition, with `or=(…)` and `and=(…)` for logic | — : a parameter per declared filter | predicates in Go: `Query().Where(…)` |
| `sort` | `order=f.desc,g`, with `nullsfirst` / `nullslast` | DRF's `OrderingFilter`: `ordering=-f,g`; django-filter's `OrderingFilter`, CSV, named by the attribute it is declared as | `Order(ent.Asc(user.FieldName))`, `Order(user.ByName(sql.OrderDesc()))` |
| `cursor` | — : `limit` and `offset`, or a `Range` header | DRF's `CursorPagination` (`cursor_query_param`) | — : `Limit` and `Offset`; the entgql extension's Relay `after` / `before` |
| `page_size` | `limit=n`, or `Range: 0-19` with `Range-Unit: items` | DRF's `page_size_query_param` (off unless set), or `limit` under `LimitOffsetPagination` | `Limit(n)`; entgql `first` / `last` |
| `include_total` | `Prefer: count=exact`, `planned` or `estimated`; the total follows the `/` of `Content-Range` | DRF's page-number and limit-offset pages carry `count` | `Count(ctx)`, a query of its own |
| `include_deleted` | — | — | — |
| `search` | `fts`, `plfts` (`plainto_tsquery`, parse's PostgreSQL search), `phfts` or `wfts` on a `tsvector` column, with a language: `fts(french)` | DRF's `SearchFilter`: `search=` (renamed by `SEARCH_PARAM`), `icontains` by default, every term required | — : a custom predicate |
| `facets` | aggregates, disabled by default (`db-aggregates-enabled`): `select=count(),category` groups by `category` | — | `GroupBy(user.FieldName).Aggregate(ent.Count())` |
| `fields` | `select=first_name,age`, which narrows the query and can rename and cast | — | `Select(pet.FieldName)` |
| `include` | embedding, `select=*,actors(*)`: the related rows themselves | — | eager loading, `With<Edge>(…)`: one more query per edge |

<!-- END rendered mapping-list-parameters -->

What parse does with each, where it differs:

- `include_total` runs `SELECT COUNT(*)` over the same filter, policy and search, ignoring the cursor; there is no planned or estimated count.
- `include_deleted` is the request asking; the server grants it through `Scope.IncludeDeleted`, and it is E2067 on a resource without a `parse.DeletedColumn`.
- `search` is one predicate that never orders the page: `to_tsvector` / `plainto_tsquery` in the configured language (`english` by default) on PostgreSQL, a `LIKE` phrase on SQLite ([search and facets](queries.md#search-and-facets)). The search fields are the schema's, not the request's.
- `facets` counts the matched rows per term or per declared range of a `parse:"facet"` field, or per related key of a `Facetable()` relation ([facet by related keys](relations.md#facet-by-related-keys)); no sum, average, minimum or maximum.
- `fields` is checked (a name that is no field of the response is E2071) but does not narrow the query: the service drops the members the mask leaves out ([field mask](queries.md#field-mask)).
- `include` returns related keys, not rows: each page row's visible related keys in `_relations`, at most `filter.MaxIncludes` (3) relations and 100 keys per owner (`filter.WithMaxIncludePerRelation`) ([include related keys](relations.md#include-related-keys)).

## Sorting

`sort=-price,name`: comma-separated fields, `-` for descending, `+` or nothing for ascending. A field must be tagged `parse:"sort"` (E2079 otherwise) and named once (E2079 for a second mention), and a sort names at most `SortSchema.MaxFields` fields, 3 by default (E2081); blank items (`sort=a,,b`) name nothing and count toward no limit. Without `sort`, or with one naming no field (`sort=,`), the field tagged `sort_default` orders the list. The key's columns the sort does not name are appended, so every sort is a total order a cursor can resume.

| | parse | PostgREST | DRF / django-filter | ent |
|---|---|---|---|---|
| Syntax | `sort=-price,name` | `order=price.desc,name` | `ordering=-price,name` | `Order(user.ByAge(sql.OrderDesc()), user.ByName())` |
| NULLs | Fixed: greatest, so last ascending and first descending | `nullsfirst` / `nullslast` per column | — | `sql.OrderNullsFirst()` |
| Strings | By code point on every backend | The column's collation | The column's collation | The column's collation |
| Related rows | A relation field is not sortable | A to-one embed's column: `order=directors(last_name).desc` | — | An edge's field or count: `post.ByAuthorField(user.FieldName)`, `user.ByPostsCount()` |
| JSON members | A declared path tagged `sort` | `order=json_data->>age.desc` | — | — |

## Paging

parse pages by keyset only. `page_size` sets the page (the resource's default, 50 unless `parse.PageSize` says otherwise; a size above its maximum, 1000 by default, is clamped; zero, a negative or a non-integer is E2067). Each page's `page_info` carries `start_cursor` and `end_cursor` toward the pages that exist, with `has_prev_page` and `has_next_page`, and `cursor` takes either one back. The next page seeks past the last row's key rather than counting rows, so with an index on the sort's columns it costs the same at any depth, and rows inserted or deleted before the cursor neither repeat nor skip a row. A cursor is opaque, capped at 4 KiB, and refused with E2082 under another sort, key or release generation ([lists and keyset pagination](queries.md#lists-and-keyset-pagination)). There is no offset, page number or `Range`: `offset` and `limit` are E2071 like any unknown parameter.

| | parse | PostgREST | DRF | ent |
|---|---|---|---|---|
| Offset | — | `limit` / `offset`, `Range: 10-` | `LimitOffsetPagination`, `PageNumberPagination` | `Limit`, `Offset` |
| Cursor | Keyset under any allowed sort, both directions | — | `CursorPagination`: an opaque cursor, forward and reverse only, needing a unique, unchanging ordering (`-created` by default) | entgql's Relay connections |
| Page size | `page_size`, clamped to the maximum | `limit`, or the `Range` header; the server may return fewer | `page_size_query_param`, `max_page_size` | `Limit` |
| Total | `include_total` | `Prefer: count=…` in `Content-Range` | `count` on page-number and limit-offset pages | `Count` |

## Relations

parse filters by related rows through relations declared on the resource (`Resource.Relate` with `HasMany`, `BelongsTo` or `ManyToMany`), or pair tables a `relation:"…"` tag declares ([relations](relations.md), [pair tables](pair-tables.md)). Each predicate compiles to one correlated `EXISTS` / `NOT EXISTS`, so a row matches once however many related rows it has, and related rows are held to the request's tenant and live rows.

| Request | parse | PostgREST | django-filter | ent |
|---|---|---|---|---|
| A related row matches every condition | `{"commands":{"$some":{"status":"pending","attempts":{"$gt":2}}}}` | `select=*,commands()&commands.status=eq.pending&commands.attempts=gt.2&commands=not.is.null`, or the embed `!inner` | Both lookups in one filter's `filter()` call, through a `method` | `HasCommandsWith(command.Status("pending"), command.AttemptsGT(2))` |
| No related row matches | `{"commands":{"$none":{"status":"failed"}}}` | The same filtered embed tested `commands=is.null` | `exclude=True`, whose conditions need not hold for one row | `Not(HasCommandsWith(…))` |
| Has any / has none | `{"commands":{"$null":false}}` / `{"commands":{"$null":true}}` | `commands=not.is.null` / `commands=is.null` | `commands__isnull=false` / `true` | `HasCommands()` / `Not(HasCommands())` |
| A related key | `{"commands":"c1"}`, flat `commands=c1`, `$in`, `$ne`, `$nin` | `commands.id=eq.c1` on an `!inner` embed | `commands=c1` (`ModelMultipleChoiceFilter`) | `HasCommandsWith(command.ID(c1))` |
| OR across relations | `{"$or":[{"tags":"red"},{"commands":{"$some":{…}}}]}` | `or=(tags.not.is.null,commands.not.is.null)` over filtered empty embeds | A `method` | `Or(HasTagsWith(…), HasCommandsWith(…))` |

parse allows two levels (`devices → commands → receipts`, E2074 beyond) and 8 relation predicates per filter (E2089 beyond). A relation predicate's cost (8 indexed, 80 not) counts against `MaxCost`. Related rows come back as keys (`include`), not as embedded rows: read the rows through the target's own list.

## Patches

A parse patch is RFC 7396 JSON Merge Patch (`application/merge-patch+json`, or `application/json` as the SDK sends it) or RFC 6902 JSON Patch (`application/json-patch+json`) with the `array_append` extension, validated against the resource's patch paths and compiled to one `UPDATE … RETURNING` scoped to the tenant, live rows and policy ([patches](patches.md), [resource updates](patches.md#resource-updates)). `TestDocTables` renders a row per operation `patch.Ops` recognizes.

<!-- BEGIN rendered mapping-patch-operations: edit the code, then go test -tags=unit -run TestDocTables -update -->

| Operation | parse | PostgREST | DRF | ent |
|---|---|---|---|---|
| `add` | A field: sets it, as `replace` does. A document member under a declared pattern (`/attrs/*`): sets the member, siblings kept; its parent must be an object. A `Writable()` relation: adds keys | — : a `PATCH` body sets whole columns | — : a partial update sets whole fields | An edge: `AddGroups(g1, g2)` |
| `remove` | A field: NULL, or a slice's or map's empty value; a nullable field only. A document member: deletes it, and it must exist. A relation: removes keys | — : a `PATCH` body sets whole columns | — : a partial update sets whole fields | A field: `ClearPhone()`; an edge: `RemoveGroup(g2)` |
| `replace` | Sets a field, an existing document member, or a relation's whole set | A `PATCH` body | `partial=True`: the fields given | `SetAge(30)` |
| `move` | Applies to a Go value (`ApplyValidatedPatch`); E2070 in SQL | — | — | — |
| `copy` | Applies to a Go value; E2070 in SQL | — | — | — |
| `test` | Applies to a Go value; E2068 in SQL, where the update's target or policy is the condition (`parse.ByFilter`, `RowUpdate.Policy`) | The `PATCH`'s filters | — | `.Where(todo.Version(v))` on an update of one row; a failed condition is `ent.IsNotFound` |
| `array_append` | Appends one element to a native array in one statement, a missing or NULL array starting empty | — | — | `AppendInts([]int{1})`, on a JSON array field |

<!-- END rendered mapping-patch-operations -->

| Form | parse | PostgREST | DRF | ent |
|---|---|---|---|---|
| Merge patch | Members set, `null` clears; an object merges into a stored document in SQL, a nested `null` deleting its key, siblings kept | A `PATCH` body sets the columns it names | `partial=True` | A `Set…` or `Clear…` per field |
| One patch, many rows | `BuildUpdate` with `parse.ByIDs`, `parse.ByKeys` or `parse.ByFilter`; over HTTP the SDK's `patchMany` sends `PATCH /{resource}?id[]=…` | `PATCH` with filters: every matching row | — : `ListSerializer` supports no multiple update by default | `Update().Where(…)` |
| Different patches per row, all or nothing | `BuildUpdateBatch`: a `RowUpdate` per key, each with an optional narrowing `Policy` such as a revision; one missing or stale target writes nothing (`ErrUpdateBatchTargets`) ([varied native update batches](operations.md#varied-native-update-batches)) | — | — : a custom `ListSerializer` update | — |
| Relation membership | `{"tags":["red","blue"]}` replaces the set, JSON Patch `add` / `remove` on `/tags` change it ([change membership](relations.md#change-membership)) | — | — | `AddGroups`, `RemoveGroup` |
| A counter or stamp the server moves | `parse.Increment`, `parse.Stamp`, `parse.StampSQL` | — | — | `AddVersion(1)` |
| Insert, upsert, delete | — : parse compiles updates only | `POST` (`Prefer: resolution=merge-duplicates`, `on_conflict`), `PUT` for one row, `DELETE` with filters | — | `OnConflict()` upserts; `Delete().Where(…)` |

## Gaps

What parse refuses that one of the others accepts:

- **Offset paging.** `offset`, `limit`, a `Range` header or a page number: keyset cursors only, so a client cannot jump to page N.
- **Request-chosen NULL placement, sorts on related rows or counts, more than 3 sort fields** (by default): PostgREST's `nullsfirst` and to-one embed columns, ent's `ByPostsCount` and `OrderNullsFirst`.
- **Operators outside the table** (E2072): regular expressions (`match`, `imatch`, `regex`, `iregex`), `isdistinct`, contained-by (`cd`, `contained_by`), the range operators (`ov` on a range, `sl`, `sr`, `nxr`, `nxl`, `adj`), array length (`len`, `sqljson.LenEQ`) and Django's date-part transforms. PostgREST's `any` / `all` modifiers are `$or` / `$and` of the operator, and `has_keys` / `has_any_keys` `$and` / `$or` of `$has_key`.
- **Undeclared fields and members** (E2071): PostgREST filters any column and any JSON key it can read, Django any key through a key lookup; parse only the fields tagged `parse:"filter"`, the key, and declared paths.
- **A null equality** (`{"f":null}`, E2073), where Django's `exact=None` is `IS NULL`.
- **Repeated list parameters** (E2067) and repeated non-equality flat parameters (E2072), each of which another server might read as first or last.
- **Search variants.** One search parameter per request, `plainto_tsquery` in the resource's language; no per-request language, no phrase or web-search syntax, no DRF prefix (`^`, `=`, `$`, `@`).
- **Rows of related resources and other aggregates.** `include` returns related keys, `facets` counts; no embedded rows, sums or averages.
- **Writes other than updates.** No insert, upsert or delete; `move`, `copy`, `test` and edits of an array element below a field apply to a Go value only (E2068 / E2070 in SQL).

What parse offers that the cited documentation of the others does not:

- **One grammar on every transport.** A JSON filter document, flat parameters and a `POST` body compile to one validated filter, held to a schema the server publishes (`parsehttp.SchemaHandler`), with a stable error code per failure; a misspelt field is an error, never an unfiltered list.
- **Relation predicates written where they apply.** `$some` holds every predicate on one related row wherever it stands under `$and`, `$or` and `$not`, two relations deep, without the embed per set of conditions PostgREST declares in `select` (aliased for a second set on one relation), and unlike django-filter, which applies each filter in a `filter()` call of its own.
- **Element conditions** on scalar arrays and arrays of objects (`{"items":{"$some":{"sku":"a","qty":{"$gt":2}}}}`, [array elements](queries.md#array-elements)).
- **Keyset cursors under any allowed sort**, both directions, opaque and bound to their sort, where DRF's cursor pagination needs a unique, unchanging ordering and PostgREST and ent's core page by offset.
- **One result on PostgreSQL, SQLite and in memory** (`filter.Evaluate`): code-point string order and exact decimals on every backend, and relative time tokens resolved once per request.
- **Patches beyond setting columns.** Merge patches that merge into stored documents in SQL, document member operations with RFC 6902's preconditions, membership writes by key, `SkipUnchanged` to write and report only the rows that change, and varied all-or-nothing batches with per-row preimage checks.

## Sources

Fetched 2026-11-23.

- PostgREST 16: [tables and views](https://docs.postgrest.org/en/stable/references/api/tables_views.html) (operators, logical operators, ordering, JSON columns, update, upsert, delete), [pagination and count](https://docs.postgrest.org/en/stable/references/api/pagination_count.html), [resource embedding](https://docs.postgrest.org/en/stable/references/api/resource_embedding.html), [aggregate functions](https://docs.postgrest.org/en/stable/references/api/aggregate_functions.html).
- django-filter 26.2: [filter reference](https://django-filter.readthedocs.io/en/stable/ref/filters.html), [FilterSet reference](https://django-filter.readthedocs.io/en/stable/ref/filterset.html), [usage](https://django-filter.readthedocs.io/en/stable/guide/usage.html), [tips](https://django-filter.readthedocs.io/en/stable/guide/tips.html), [DRF integration](https://django-filter.readthedocs.io/en/stable/guide/rest_framework.html); its source at the 26.2 tag for how a `FilterSet` applies its filters: [`filterset.py`](https://github.com/carltongibson/django-filter/blob/26.2/django_filters/filterset.py) (`filter_queryset`) and [`filters.py`](https://github.com/carltongibson/django-filter/blob/26.2/django_filters/filters.py) (`Filter.filter`).
- Django 6.1, whose lookups django-filter's `lookup_expr` names: [field lookups](https://docs.djangoproject.com/en/stable/ref/models/querysets/#field-lookups), [making queries](https://docs.djangoproject.com/en/stable/topics/db/queries/) (relations, multi-valued relations, `Q` objects, `JSONField`), [PostgreSQL fields](https://docs.djangoproject.com/en/stable/ref/contrib/postgres/fields/) (`ArrayField`), [GeoDjango lookups](https://docs.djangoproject.com/en/stable/ref/contrib/gis/geoquerysets/).
- Django REST framework 3.18: [filtering](https://www.django-rest-framework.org/api-guide/filtering/) (`SearchFilter`, `OrderingFilter`), [pagination](https://www.django-rest-framework.org/api-guide/pagination/), [serializers](https://www.django-rest-framework.org/api-guide/serializers/) (partial and multiple updates).
- ent v0.14.6: [predicates](https://entgo.io/docs/predicates), [paging and ordering](https://entgo.io/docs/paging), [CRUD](https://entgo.io/docs/crud) (update, upsert, delete, field selection), [eager loading](https://entgo.io/docs/eager-load), [aggregation](https://entgo.io/docs/aggregate), [entgql pagination](https://entgo.io/docs/tutorial-todo-gql-paginate); the generated predicates and order options of the [`start` example](https://github.com/ent/ent/blob/v0.14.6/examples/start/ent/user/where.go) and, for `IsNil` / `NotNil`, [`entcpkg`](https://github.com/ent/ent/blob/v0.14.6/examples/entcpkg/ent/user/where.go).
