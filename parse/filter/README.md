# parse/filter

Validated filter ASTs and parameterized PostgreSQL/SQLite query plans, including sort, keyset pagination, search, facets, and relation memberships. Use `parse.Resource` for the common struct-tag path; use these lower-level builders to integrate with existing SQL.

## Usage and configuration

| Guide | Contents |
|---|---|
| [Operators, list queries, examples, and every builder option](../docs/queries.md) | Consumer guide |
| [Relation filters and includes](../docs/relations.md) | Consumer guide |
| [HTTP parameters and errors](../docs/http-errors.md) | Consumer guide |

Start with the [runnable package example](../README.md#quick-start). The focused [filter example](example_test.go) is compiled and output-checked in the unit suite.

## API reference

- Schemas: `GenerateFilterSchema[T]`, `GenerateSortSchema[T]`, `GenerateSearchSchema[T]`, `OperatorsFor`, `DefaultFilterOps`, `ResourceSchema` (`PrimaryKey`: the declared final keyset column), `FieldSchema`, `SortSchema`, `SearchSchema`, `FilterMetaFromSchema`
- Parsing: `NewFilterParser` (`Parse`, `ParseBytes`, `ParseMap`, `And` — filters parsed apart, combined and bounded as one), `ParseSort`, `NewQueryParamParser`, `NewParsedFilter`, `ResolveRelativeTime`
- SQL: `Dialect` (`Condition`, `ReadColumn`: how a statement reads a column of a declared `core.SQLType`, `DecimalParam`: a bound decimal's canonical text as the dialect's exact number), `PostgresDialect`, `SQLiteDialect`, `DialectFromDSN`, `NewSQLBuilder`, `AndSearch`
- Lists: `NewListQuery` (`Build`, `WithFilterConfig`, `WithSearchSchema`), `ListResult`, `NewPage`, `Page[T]`, `CursorValueByTag`, `CursorValue`, `Keyset`, `CursorKey`, `CursorPlan`, `EncodeCursor`, `DecodeCursor`, `ValidateCursor`, `ReverseDirection`
- Facets / includes: `BuildFacetQuery` (rows in a fixed order: request order; terms by count then key; ranges as declared; declared relations count visible related keys, `WithFacetRelatedPolicies`), `ScanFacetUnion`, `FacetMap` (results keyed by field, as `Page.Facets` carries them), `FacetBuckets` (that order for a backend counting in Go; a list facet counts each distinct element once per row), `ParseFacetTag`, `ParseIncludes`, `BuildIncludeQuery` (declared relations read visible related rows; text keys use code-point order before deduplication/capping; `WithIncludeRelatedPolicies`), `IncludeQuery.Scan` → `IncludeRows` (IDs, owners `Truncated` at the cap), `ResolveInclude` / `IncludeLookup` (the same projection outside SQL through batched owner/target pairs), `Page.SetInclude` (canonical page projection; empty results omitted), `MaxIncludes`, `Rows`
- Policy: `PolicyConstraints` (`Merge`), `PolicyConstraint` (`UnknownIfInvalid`: a value that does not apply to the field compiles to SQL's unknown — no row satisfies it, nor its negation — instead of an error), `UnknownConstraint` (that unknown, stated), `ApplyPolicyConstraints` (values normalized as request values, without request limits), `BuildBaseConstraints`
- Memberships: `PolicyConstraint.Member` (`*Lookup`: the field is among the values a lookup table or view projects — a closure's descendants, granted ids, a pair relation's resources), `LookupCond` (`WhereEq`, `WhereIn`, `WhereNull`, `WhereAll`, `WhereAny`), `ResolveMemberships` (a memory backend's once-per-request resolution through its `MembershipLookup` into the `$in` SQL's subquery equals; no lookup is SQL's unknown), `MatchLookupRow` (a lookup row's condition with SQL equality)
- Relations: `FieldSchema.Related` (the target's schema), `FieldSchema.RelatedBy` (the owner's correlation field), `OpSome` / `OpNone` (`$some`, `$none`; `$is`, `$is_not` parse to them), `MaxRelationDepth`, `MaxRelationPredicates`, `WithRelatedPolicies` / `ListRelatedPolicies`, `ErrRelatedPolicyRequired`
- Array elements: `FieldSchema.Elements` (a JSON array's element paths, from slice-of-struct members or the `elements` tag), `$some` / `$none` on it, `DocumentElements`
- Memberships outside SQL: `Lookup` (`Where`, `Filter`), `WhereEq` / `WhereIn` / `WhereNull` / `WhereNotNull` / `WhereMember` / `WhereAll` / `WhereAny`, `MembershipLookup`, `RelationLookup` (lookups with a filter), `ResolveMemberships` (policy memberships and relation predicates) with `ResolveTenant`, `ResolveRelatedPolicies`, `ResolveUnscopedRelations`, `MatchLookupRow`
- JSON paths: `JSONPath`, `JSONStorage` (`StorageJSONB`, `StorageJSON`, `StorageText`), `PathDecl`, `ResourceSchema.WithPaths` (a request's copy-on-write view), `SortSchema.WithPaths` (its sort keys), `FieldExpr` (a field's value expression in a dialect), `Dialect.PathExpr`, `PathValue` (the reference value rule a memory backend evaluates), `SortFieldSchema.Path` / `CursorKey.Path`, `KeyValue` (a row's canonical value for any key, paths included), `PathDecl.Facet`, `MaxPathDepth`; `SortResult.ToSQL(dialect)` renders ORDER BY as the keyset orders
- Reference semantics: `MatchLike`, `EscapeLike` (a literal as a LIKE pattern; append `%` for a prefix), `JSONEqual`, `JSONContains`, `JSONHasKey`, `PathValue`
- Geo: `GeoPoint`, `GeoDistance`, `GeoBoundingBox`, `GeoPolygon`, `GeoFieldConfig`, `ParseDistance`, `ValidateGeoPoint`, `ValidateGeoBoundingBox`, `ValidateGeoPolygon`
- Errors: `ECode*` (`core.Code` constants), `Codes`

## Code map

| File | Responsibility |
|------|----------------|
| `filter.go` | `FilterParser` — parse, normalize, depth / condition / cost limits |
| `schema.go` | Operators, `ResourceSchema`, `FieldSchema`, sort schema types, `SQLResult` |
| `operators.go` | `OperatorsFor` — the operator × type table |
| `generate.go` | Schema generation from struct tags; default operators |
| `sql.go` | `Dialect`, `SQLBuilder`, relation `EXISTS`, junction tenancy |
| `sql_postgres.go` / `sql_sqlite.go` | Dialect predicates (strings, decimals, documents, arrays, geo) |
| `like.go` | LIKE patterns: escaping, GLOB translation, `MatchLike` |
| `jsondoc.go` | `JSONEqual`, `JSONContains`, `JSONHasKey` |
| `path.go` | `JSONPath`, `FieldExpr`, `PathValue`, both dialects' `PathExpr` |
| `path_declare.go` | Path declaration: `paths` / `storage` tags, struct members, `PathDecl`, `WithPaths` |
| `list.go` | `ListQuery`, `ListOption`s, `ListResult` |
| `page.go` | Resource `Cursor` and `PageInfo` wire types |
| `keyset.go` | Keyset plan, cursors, `NewPage`, `Page[T]` (items, page info, `Relations`, `Truncated`, `Facets`) |
| `facet.go` / `facet_union.go` / `facet_buckets.go` | Facet schema and the single facet query |
| `relation_parse.go` / `relation_sql.go` | Relation predicates: target filters and key shorthand; one correlated EXISTS per predicate with per-depth aliases |
| `elements.go` | Element paths of JSON arrays; one EXISTS over the elements per `$some` / `$none` |
| `membership.go` / `relation_resolve.go` | Lookups and their SQL; `ResolveMemberships` for backends outside SQL, relation predicates included |
| `search.go` | Search schema and condition |
| `include_resolve.go` | `ResolveInclude`, `IncludeLookup`, `IncludePair` — tenant- and policy-scoped projections outside SQL, distinct keys ordered before the per-owner cap |
| `include.go` | `include` relation projections — `ParseIncludes` / `ParseIncludeNames`, per-owner-capped `BuildIncludeQuery`, `IncludeQuery.Scan`, `Page.SetInclude` |
| `policy.go` | Policy constraints |
| `queryparam.go` | Flat query parameters — `QueryParamParser` (`QueryParamAccept`, `QueryParamLenient`, `QueryParamFieldsOnly`), `IsListParam` |
| `params.go` | `Param*` list request parameters |
| `geo.go` | Geo types, validation, tag parsing, WKT |
| `time_token.go` | Relative-time tokens |
| `rows.go` | `Rows` |
| `errors.go` | E-codes |
| `filtertest/` | Conformance fixture: the operator × type cases (`Run`), the membership cases with their lookup tables and reference lookup (`RunMembership`, `LookupTables`, `Lookups`), and the JSON path cases (`RunPaths`, `PathRecords`, `PathSchema`) |

## Dependencies

- *Imports*: `parse/core`, `parse/internal/dialect`, `parse/internal/cursor`, stdlib `uuid`
- *Imported by*: `parse`, `forge/dal/*`, `forge/feature`, `forge/fibermw/httputil`
- *Package boundary*: standalone Parse module

## References

[Package overview](../README.md) · [Integration recipes](../docs/integrations.md)
