# Changelog

## [0.19.0](https://github.com/ashtonian/pkg/compare/parse/v0.18.0...parse/v0.19.0) (2026-11-28)


### ⚠ BREAKING CHANGES

* **parse:** parse/parsesqlite is gone: its Register, RegisterOn, Functions and function names are parsesql's. parsesql and parsepgx are modules: require them beside parse, at parse's version. parseprovider's Config, SQLiteConfig and PostgresConfig are gone: hold the provider's name and each linked provider's Config (parsesql.Config, parsepgx.Config, whose dsn and settle_timeout are each its own), and call parseprovider.Open(ctx, name, parsesql.Choice(cfg.SQLite), parsepgx.Choice(cfg.Postgres)). It returns the parseprovider.Database interface; the handle is the opened provider's (*parsesql.Database).DB or (*parsepgx.Database).Pool.
* **parse:** a resource is built on a provider and runs its own statements.
    - parse: NewResource and MustResource take a *Provider first.
    - parse: BuildList, BuildFacets, BuildUpdate and BuildUpdateBatch take no
      dialect; Columns() returns []string with no error; RowScanner and
      ListQuery take no dialect.
    - parse: removed CheckPlan, ErrDialect, Plan, Dialect, the plans' Dialect
      methods, UpdatePlan.ConflictError, UpdateBatchChunk.CheckCurrent,
      ListPage, ListRows, FollowUp, UpdateFollowUp, RelationStatements,
      Reread, UpdatedRows, Unchanged, CheckBatchCurrent, BatchResult,
      ExecConfig, ExecOption, NewExecConfig and WithSettleTimeout. Run plans
      through the resource's List, Facets, Query, Update and UpdateBatch.
    - parse: StatementFailed takes the table first, and StatementError has a
      Table field; its message is "parse: &lt;table&gt;: &lt;statement&gt;: &lt;cause&gt;".
    - parse: added Provider, Tx, ErrProviderMismatch, Resource.In,
      Resource.Provider and RelatedResource's Provider method.
    - parsesql: removed QueryPage, Query, Update, UpdateBatch, Queryer, DB
      and ErrNoTransaction; added New, Option, SettleTimeout, Tx and Name.
      MinSQLiteVersion, CheckVersion and VersionError moved here from
      parsesqlite.
    - parsepgx: removed QueryPage, Query, Update, UpdateBatch, Querier and
      DB; added New, Option, Collation, ContainmentMaxValues, SettleTimeout,
      Tx and Name.
    - filter: removed DialectFromDSN, ErrUnknownDSN and ComputedExpr;
      ComputedColumn holds resolved SQL, and ResourceSchema.ComputedSQL takes
      no dialect and returns no error.
    - filter: SQLiteDialect no longer refuses a JSON Patch member whose name
      holds a quote or a backslash.
    - parsesqlite: no longer checks the SQLite version on each connection.
* **parse:** ParseError.Kind, ParseErrorKind and its constants are removed; build a ParseError with core.NewError and its options. Error responses carry details.field wherever an error is about one field, and the facet E2071 no longer sets path.
* **parse:** a parse:"sort" field of a struct type now needs
* **parse:** parse's SQLite plans need SQLite 3.47.0 or later, and parsesqlite.Register and RegisterOn refuse a connection to an older library. On SQLite a JSON Patch operation on a document member whose name holds " or \ is E2070; apply it with ApplyValidatedPatch, or write it with a merge patch.
* **parse:** filter.PathValue returns an int or float path's member as a json.Number of its canonical decimal text, not an int64 or float64, and a JSON number beyond core.MaxDecimalDigits digits in plain notation is absent there, in cursors and in the SQL. Evaluate compares such members exactly. On SQLite a statement on an int or float path calls parse_decimal, so it needs the parsesqlite functions registered.
* **parse:** filter.Dialect, SQLiteDialect and PostgresDialect no longer have a NativeRowCompare method.
* **parse:** NewResource accepts a DeletedColumn field that is a slice, map or interface, and refuses an sql.Scanner with no Value method and one whose zero value writes a value rather than NULL, as it refuses uuid.UUID and the decimal types.
* **parse:** a field of a nullable wrapper type is its value's type, not an object document: it takes its value's filter operators (filter_ops naming document operators such as contains or has_key is refused), sorts with its value's numeric range, and defaults to the replace and remove patch operations. core.TypeOf no longer types a document member; use core.MemberTypeOf for one.
* **parse:** filter.NewQueryParamParser returns (*QueryParamParser, error); it refuses a nil schema and a QueryParamAccept name that is a list parameter or a flat filter on a field, suffix forms included, and Resource.ParseRequestFilter returns that error.
* **parse:** a boolean, interval, inet, cidr or macaddr key takes eq and in alone; a range on it is E2072, and the schema document lists only those two.
* **parse:** NewResource refuses a DeletedColumn whose type cannot hold NULL as its field sees it (uuid.UUID, decimal.Decimal, core's value types), and a patch relation refuses a key type that holds NULL, such as sql.NullString. A sort key or patch path of an sql.Null type is nullable: its cursor carries null, and its path takes remove and a null merge.
* **parse:** filter.KeyValue returns an int or float path key's member as its canonical decimal text (a string), not an int64 or float64, and a cursor carries that text.
* **parse:** filter.CompareKeyValues returns (int, error) and refuses values its key's type cannot order instead of ordering them by their text; core.KeyValueText is core.CanonicalText.
* **parse:** parsesql.Update and UpdateBatch refuse a DB that is neither a *sql.Tx nor has BeginTx (parsesql.ErrNoTransaction), and both executors' Update and UpdateBatch take trailing parse.ExecOption arguments.
* **parse:** a sort naming a field twice, such as sort=a,-a, is refused with E2079.
* **parse:** a patch body read from a request is refused with E2063 past 1 MiB when MaxInputSize is unlimited.
* **parse:** changing a PathSchema that PatchSchema.Path or Paths returns no longer changes the schema; use PatchSchema.UpdatePath.
* **parse:** parsepgx.ErrDialect and parsesql.ErrDialect are removed; both executors refuse a plan compiled for another dialect with parse.ErrDialect, so test for that instead.
* **parse:** NewResource, Relate and WithPaths refuse an AcceptParams name that is a list parameter or a flat filter on a field.
* **parse:** NewPatchSchema, AddPath, ForbidPaths and ConstrainValues refuse a key or pattern that does not begin with /.
* **parse:** these relation failures are no longer *core.ParseError E2083; match filter.ErrRelationUnresolvable with errors.Is.
* **parse:** Resource.ParseKey, and so parsehttp.ItemKey, return ErrNoSuchKey, not ErrKeyInvalid, for a text a key column cannot hold.
* **parse:** core.CheckNUL and core.ErrNUL are replaced by core.CheckText, core.CheckValueText and core.ErrInvalidText.
* **parse:** filter.VisibleRelated takes a RelationScope (its Tenant, Related and Now) in place of the tenant and related-policy arguments.
* **parse:** a payload holding an invalid Date, a toJSON that returns null, or undefined, a function or a symbol in an array throws UnencodableValueError where it was sent as null. A 2xx answer without the members a call reads is a ResponseError. A cached schema document is frozen. listAll throws ResponseError for a next page without a cursor.
* **parse/client:** parse/client is the module github.com/ashtonian/pkg/parse/client and the parse module no longer contains it. Importers add the module (go get github.com/ashtonian/pkg/parse/client, or go mod tidy) and require parse at this release or later if they also require parse; an earlier parse still holds the package, and the import is then ambiguous.
* **parse/filter:** a list page with no rows encodes "items":[] instead of "items":null. A client that tested items for null tests for an empty array.
* **parse:** NewResource fails for a search parameter that is a list parameter, an AcceptParams name or a flat filter on one of the resource's fields, and Relate and WithPaths fail for a relation or path such a parameter would filter. Rename the parameter with core.WithSearchParam.
* **parse/patch:** a field with parse:"patch", or with parse:"server" in the server patch schema, that is promoted through an unexported embedded pointer fails GeneratePatchSchema and GenerateServerPatchSchema, and so NewResource and Register; export the embedded type or embed it by value. NewResource also refuses a customized patch path that reaches such a field, and Applier.CheckSchema refuses one in a schema completed by hand. Apply refuses an unchecked one with an error that is not a core.ParseError, which the HTTP layer answers 500 rather than E2076 (422). Apply never writes the caller's row or a parent it embeds by pointer, on success or on a failure part-way.
* **parse:** NewResource refuses a column it reads, a computed column included, whose field is promoted through an unexported embedded pointer, and a read column holding a stored document whose type has, at any depth, a member promoted that way. RowScanner refuses ScanJSON on an sql.Scanner column whose type has such a member, and GeneratePatchSchema and GenerateServerPatchSchema refuse a patch or server field whose type has one. Export the embedded type or embed it by value, or leave the column out of ReadColumns.
* **parse/core:** the fields of core.StructFields and core.Field are now methods, and core.Tags no longer has Enum and Bounds. Write walk.Fields() to range over the fields, collect it with slices.Collect for a slice, and use walk.Len() for its length; walk.Type, f.GoName, f.Type, f.Route, f.External and f.Tags become calls, and the field's enum and bounds come from its Enum and Bounds methods. Route, Enum and Bounds return copies, so changing what they return no longer changes the walk.
* **parse/sdk:** a patch, patchMany, list, listAll or facets call whose payload holds NaN, Infinity or -Infinity now rejects with NonFiniteNumberError and sends nothing, where it used to send null (or, under validateBeforeSend, rejected a top-level float with SchemaValidationError E2077). Send null where a removal is meant, and catch NonFiniteNumberError, or RangeError, for the refusal.
* **parse/sdk:** PatchInput's value for a JSON object field is a MergeDocument of the field's type rather than the type itself, so code that reads a patch's field back as the row's type must take the merge document type; and a document literal holding null for a member its type always carries, a call the server refused with E2077, no longer compiles. Send that member's value, or leave it out.

### Features

* **parse/client:** its own standard-library module, with the decode side of the wire ([707e3f2](https://github.com/ashtonian/pkg/commit/707e3f242ef0c7342920d405746d4d825c2ee7a1)), refs [#377](https://github.com/ashtonian/pkg/issues/377)
* **parse/parsehttp:** WriteList and MaskPage answer a list page under its field mask ([19e5c03](https://github.com/ashtonian/pkg/commit/19e5c037e06e3627f58c812a0ef55d739802e7ab))
* **parse/sdk:** a request method for a server's other routes ([8171b87](https://github.com/ashtonian/pkg/commit/8171b876e8e8dec66b4a32fd630cb4628ec9fb87)), refs [#379](https://github.com/ashtonian/pkg/issues/379)
* **parse:** an in-memory provider over one executor per provider ([#467](https://github.com/ashtonian/pkg/issues/467)) ([786ddf8](https://github.com/ashtonian/pkg/commit/786ddf8d0fd4c9e3d01d39b966d32ff70fe88208))
* **parse:** each provider opens and runs its own resources ([#461](https://github.com/ashtonian/pkg/issues/461)) ([237414c](https://github.com/ashtonian/pkg/commit/237414c12c27f0b26a91d910467c8ec5076d5745))
* **parse:** wave 6, the review's bugs: update transactions and locks, request text and keys, policies, patches, filters and the clients ([#458](https://github.com/ashtonian/pkg/issues/458)) ([fb9cf6d](https://github.com/ashtonian/pkg/commit/fb9cf6da1d35a0d9bb104ddef6ce49f0233c4cef)), refs [#441](https://github.com/ashtonian/pkg/issues/441) [#440](https://github.com/ashtonian/pkg/issues/440) [#433](https://github.com/ashtonian/pkg/issues/433) [#431](https://github.com/ashtonian/pkg/issues/431) [#429](https://github.com/ashtonian/pkg/issues/429) [#438](https://github.com/ashtonian/pkg/issues/438) [#436](https://github.com/ashtonian/pkg/issues/436) [#435](https://github.com/ashtonian/pkg/issues/435) [#439](https://github.com/ashtonian/pkg/issues/439) [#437](https://github.com/ashtonian/pkg/issues/437) [#434](https://github.com/ashtonian/pkg/issues/434) [#432](https://github.com/ashtonian/pkg/issues/432) [#426](https://github.com/ashtonian/pkg/issues/426) [#430](https://github.com/ashtonian/pkg/issues/430) [#428](https://github.com/ashtonian/pkg/issues/428) [#425](https://github.com/ashtonian/pkg/issues/425) [#424](https://github.com/ashtonian/pkg/issues/424) [#423](https://github.com/ashtonian/pkg/issues/423) [#422](https://github.com/ashtonian/pkg/issues/422) [#421](https://github.com/ashtonian/pkg/issues/421) [#420](https://github.com/ashtonian/pkg/issues/420) [#419](https://github.com/ashtonian/pkg/issues/419) [#418](https://github.com/ashtonian/pkg/issues/418) [#417](https://github.com/ashtonian/pkg/issues/417) [#413](https://github.com/ashtonian/pkg/issues/413) [#412](https://github.com/ashtonian/pkg/issues/412) [#411](https://github.com/ashtonian/pkg/issues/411) [#410](https://github.com/ashtonian/pkg/issues/410) [#409](https://github.com/ashtonian/pkg/issues/409) [#408](https://github.com/ashtonian/pkg/issues/408) [#407](https://github.com/ashtonian/pkg/issues/407) [#406](https://github.com/ashtonian/pkg/issues/406) [#404](https://github.com/ashtonian/pkg/issues/404) [#403](https://github.com/ashtonian/pkg/issues/403) [#401](https://github.com/ashtonian/pkg/issues/401) [#402](https://github.com/ashtonian/pkg/issues/402) [#400](https://github.com/ashtonian/pkg/issues/400) [#399](https://github.com/ashtonian/pkg/issues/399) [#398](https://github.com/ashtonian/pkg/issues/398) [#397](https://github.com/ashtonian/pkg/issues/397)


### Bug Fixes

* **parse/core:** the shared field walks are read-only ([19e5c03](https://github.com/ashtonian/pkg/commit/19e5c037e06e3627f58c812a0ef55d739802e7ab))
* **parse/filter:** a cursor of another generation is E2082 "unsupported cursor version" whatever its payload holds ([19e5c03](https://github.com/ashtonian/pkg/commit/19e5c037e06e3627f58c812a0ef55d739802e7ab))
* **parse/filter:** a list that matches nothing answers an empty items array ([19e5c03](https://github.com/ashtonian/pkg/commit/19e5c037e06e3627f58c812a0ef55d739802e7ab))
* **parse/patch:** a patch never writes the caller's row, nor a parent it embeds by pointer ([19e5c03](https://github.com/ashtonian/pkg/commit/19e5c037e06e3627f58c812a0ef55d739802e7ab))
* **parse/sdk:** PatchInput types a document field as a merge document, any subset of its members at every depth ([19e5c03](https://github.com/ashtonian/pkg/commit/19e5c037e06e3627f58c812a0ef55d739802e7ab))
* **parse/sdk:** resource methods work destructured and as callbacks ([19e5c03](https://github.com/ashtonian/pkg/commit/19e5c037e06e3627f58c812a0ef55d739802e7ab))
* **parse/sdk:** the SDK refuses NaN and infinities in a payload rather than send null ([19e5c03](https://github.com/ashtonian/pkg/commit/19e5c037e06e3627f58c812a0ef55d739802e7ab))
* **parse:** a resource reads the search from the query parameter discovery advertises ([19e5c03](https://github.com/ashtonian/pkg/commit/19e5c037e06e3627f58c812a0ef55d739802e7ab))
* **parse:** a resource refuses a read column no row scan can fill ([19e5c03](https://github.com/ashtonian/pkg/commit/19e5c037e06e3627f58c812a0ef55d739802e7ab))
* **parse:** error text names a type without its struct tags ([19e5c03](https://github.com/ashtonian/pkg/commit/19e5c037e06e3627f58c812a0ef55d739802e7ab))
* **parse:** wave 7, the review's remaining bugs and the deduplication they needed ([#459](https://github.com/ashtonian/pkg/issues/459)) ([59aa60f](https://github.com/ashtonian/pkg/commit/59aa60f35076d38d428f4eca93d2b5dd87192aa7)), refs [#454](https://github.com/ashtonian/pkg/issues/454) [#446](https://github.com/ashtonian/pkg/issues/446) [#453](https://github.com/ashtonian/pkg/issues/453) [#455](https://github.com/ashtonian/pkg/issues/455) [#443](https://github.com/ashtonian/pkg/issues/443) [#448](https://github.com/ashtonian/pkg/issues/448) [#452](https://github.com/ashtonian/pkg/issues/452) [#445](https://github.com/ashtonian/pkg/issues/445) [#447](https://github.com/ashtonian/pkg/issues/447) [#449](https://github.com/ashtonian/pkg/issues/449) [#456](https://github.com/ashtonian/pkg/issues/456) [#444](https://github.com/ashtonian/pkg/issues/444) [#451](https://github.com/ashtonian/pkg/issues/451)


### Performance Improvements

* **parse:** give each provider its own module, and require no driver in parse ([#465](https://github.com/ashtonian/pkg/issues/465)) ([abe3f93](https://github.com/ashtonian/pkg/commit/abe3f9363d6196c39728635deedecf3ae43ed132)), refs [#450](https://github.com/ashtonian/pkg/issues/450)

## [0.18.0](https://github.com/ashtonian/pkg/compare/parse/v0.17.1...parse/v0.18.0) (2026-11-23)


### ⚠ BREAKING CHANGES

* **parse:** PrimaryKey takes one or more columns, and NewResource refuses a key column of no scalar type (a document, array, geometry or opaque column). Resource.Key returns parse.Key (core.Key, a value per key column); PrimaryKeyColumn is PrimaryKeyColumns; RowUpdate.Key, UpdateBatchChunk.Keys, CheckCurrent, IncludeQueries, RelationStatements and Reread take parse.Key values. filter.ResourceSchema.PrimaryKey is a []string, and filter.Keyset takes its tie-breakers variadically: Keyset(sort, keys...).
* **parse:** HasMany and BelongsTo are generic over KeyColumns, References is variadic, Junction.Source and Junction.Target are []string, and Through is generic. core.RelationSchema replaces SourceColumn, TargetColumn, PrimaryKey, TargetKey and OwnerKey() with SourceColumns, TargetColumns, OwnerKey, TargetKeys and OwnerColumns(), and its Check refuses a schema without SourceTypes and TargetTypes, one scalar type per column. filter.FieldSchema.RelatedBy is a list. BuildIncludeQuery, ResolveInclude and patch.RenderRelationOp take owner keys as []core.Key, and patch.RenderRelationOp takes a patch.RelationDialect. patch.RelationOwner.Key is a column list, and patch.RelationPath takes the target key's columns as RelationKey values. A relation:"…" tag needs each owner key column as a field of its struct, and the tagged field must be a slice of target keys.
* **parse:** core.IsKeyType reports every scalar type. Relation joins, key sets, include and facet grouping, membership lookups and update batches compare a key as a filter's $eq compares its type: a decimal by value on both backends, so a stored 1.50 matches 1.5.
* **parse/filter:** IncludeLookup.LookupPairs takes the owner's key columns, and a pair's Owner and Target are a core.Key when the key has several columns. A membership's lookup projects one column. A store answering MembershipLookup, RelationLookup or IncludeLookup answers ProjectKey lookups with a core.Key per row and evaluates LookupOpKeyIn and typed conditions, as filter.MatchLookupRow does.
* **parse/filter:** SQLite decimal $eq, $ne, $in and $nin compare parse_decimal_key, so an expression index on parse_decimal(c) no longer serves them. Index parse_decimal_key(c), followed by the keyset's tie-breaker; it serves ranges, sorts and seeks too.
* **parse/filter:** a terms facet on a json, object, geo, interval, inet or cidr field, a range facet on anything but a scalar number field, a facet tag without the facet capability, and a malformed facet configuration (no ranges, an empty or inverted or non-finite or overlapping range, a terms limit below 1) now fail schema generation, WithPaths or BuildFacetQuery.
* **parse:** an enum on a field or struct member whose values are not strings, an empty plain enum value, and a plain value beginning with a single quote now fail schema generation or read as the quoted form.
* **parse/filter:** a struct with a non-integer order, a sort_default other than asc or desc, more than one sort_default, or a sort_default on an unsortable field no longer generates a schema.
* **parse/filter:** an empty page after a seek reports has_next_page and has_prev_page false where one of them was true.
* **parse/filter:** a WhereIn list binds as one array parameter (PostgreSQL) or JSON array (SQLite), so statement text and arguments change, and a list mixing value types is refused.
* **parse/filter:** a key comparison on a relation two predicates deep is E2074 where it was accepted.
* **parse/filter:** a filter value, search term or cursor key holding U+0000 is E2073 (E2082 for a cursor) where it was accepted.
* **parse/filter:** filter.MatchLike and the memory evaluator no longer match a character with another its Unicode case folding equates but its lower-case does not (ſ and s, ς and σ, µ and μ), and match İ with i.
* **parse/filter:** PostgreSQL path SQL is doc -> 'a' ->> 'b' where it was doc #>> '{a,b}'. An expression index built on the old expression no longer matches the emitted one; rebuild it on filter.FieldExpr.
* **parse:** schema documents list the resource's key and the implied NULL tests; include=&lt;key&gt; is E2083.
* **parse:** SchemaValidator answers a filter, patch or sort on a schema document without that section with E2071, E2076 or E2079 instead of E2078. testdata/filter_conformance.json is an object, and testdata/sdk_wire.json records exchanges.
* **parse:** the example's facets route answers a page parameter with E2067.
* **parse:** a list body's read error is a *parse.BodyReadError. The example's patch routes refuse a body without a JSON patch media type.
* **parse:** a repeated list parameter (filter, sort, cursor, page_size, include_total, include_deleted, search, facets, fields, include) is E2067 instead of reading the first value.
* **parse:** E2078 answers 404. filter.ECodeSchemaNotFound is parse.ECodeSchemaNotFound and leaves filter.Codes(). E2069, core.CodeSQLColumnMissing and patch.ECodeSQLColumnMissing are removed; match patch.ErrSQLColumnMissing. parse.Codes() is sorted.
* **parse:** E2080 and filter.ECodeSQLGenerationFailed / core.CodeSQLGenerationFailed are removed. Invalid policy constraints and membership lookups are no longer *core.ParseError; match filter.ErrPolicyInvalid. filtertest.PolicyCase.Err is an error, not a core.Code.
* **parse:** SchemaRegistry.SchemaHandler, SchemaListHandler, parse.WithErrorHandler and parse.SchemaHandlerOption are removed; use parsehttp.SchemaHandler(reg, ...), parsehttp.SchemaListHandler(reg, ...) and parsehttp.WithErrorHandler. parsehttp.WriteError takes the request, WriteError(w, r, err, opts...), and its hook is func(*http.Request, error). parsehttp.Body is a struct, no longer an alias. A marshalled core.ParseError has no kind member.
* **parse:** a zero MaxDepth, MaxCost, MaxConditions or MaxArraySize selects the default instead of refusing everything (filters) or removing the depth bound (patches). NewResource refuses a negative limit. WithGeneratorOptions adds to earlier generator options instead of replacing them.
* **parse:** error text changes throughout the root package, and the sentinels' own text loses its "parse: " prefix; errors.Is holds for every sentinel. A target key of the wrong type is ErrKeyInvalid instead of a core.ParseError.
* **parse:** RegisterResource returns an error. Registering a name a second time, by type or by resource, is ErrRegistered instead of replacing the first registration. MustRegister panics with an error value.
* **parse:** Resource.Relate returns an error when the resource or the relation's target is a WithPaths view.
* **parse:** filter.ResourceSchema gains Computed; a schema that declares it reads those columns through their expressions in every builder. Resource.ListQuery compiles computed columns as their expressions instead of the stored columns they name.
* **parse:** ListRequest's fields and filter.ParsedFilter's AST, EstimatedCost and ConditionCount are methods, and core.SchemaGenerator.Opts is gone. Schema accessors return copies: customize with CustomizeSchemas. ApplyValidatedPatch, BuildUpdate and BuildUpdateBatch report a hand-built patch as parse.ErrForeign instead of patch.ErrUnparsed.
* **parse:** parsepgx.Update returns parse.UpdateResult[T] instead of []T; read Changed and Unchanged in place of the concatenated rows.

### Features

* **parse/examples/server:** serve schemas(), and log a startup entry at a runtime level ([8b12676](https://github.com/ashtonian/pkg/commit/8b12676a0a1c54c1d3f9c31ab8d6f7994b941098)), refs [#280](https://github.com/ashtonian/pkg/issues/280)
* **parse/filter:** lookups resolved outside SQL take keys of several columns ([93f6612](https://github.com/ashtonian/pkg/commit/93f6612344bd50a28f38e42f48ba83ed96f0f8b1))
* **parse/filter:** SQLite compares decimals by key, so one index serves every decimal predicate ([93f6612](https://github.com/ashtonian/pkg/commit/93f6612344bd50a28f38e42f48ba83ed96f0f8b1))
* **parse/patch:** JSON Patch add, replace and remove on a declared document member compile to SQL ([93f6612](https://github.com/ashtonian/pkg/commit/93f6612344bd50a28f38e42f48ba83ed96f0f8b1))
* **parse/patch:** merge patches into integer- and text-keyed maps ([93f6612](https://github.com/ashtonian/pkg/commit/93f6612344bd50a28f38e42f48ba83ed96f0f8b1))
* **parse:** a client addresses a row by one path segment per key column, each escaped on its own ([93f6612](https://github.com/ashtonian/pkg/commit/93f6612344bd50a28f38e42f48ba83ed96f0f8b1))
* **parse:** a facets request is parsed as one ([c8081c6](https://github.com/ashtonian/pkg/commit/c8081c62ac1047e366d63c4cd5797a176c77b06d))
* **parse:** a resource's key is one or more columns of any scalar type ([93f6612](https://github.com/ashtonian/pkg/commit/93f6612344bd50a28f38e42f48ba83ed96f0f8b1))
* **parse:** enum declares quoted values and holds only on strings, struct-member paths included ([d728270](https://github.com/ashtonian/pkg/commit/d728270359f0def57ef58d0a60688fd8920a3327))
* **parse:** executors for every plan on both backends; Update reports what changed ([1d023cf](https://github.com/ashtonian/pkg/commit/1d023cf1c5483eddd149563c9ab6adfd4df2a731))
* **parse:** keys compare as a filter compares their type ([93f6612](https://github.com/ashtonian/pkg/commit/93f6612344bd50a28f38e42f48ba83ed96f0f8b1))
* **parse:** patch bodies are read by the package, bounded and by media type ([19a7f4e](https://github.com/ashtonian/pkg/commit/19a7f4e7530692172747b84f5f9f4648ae0e3636))
* **parse:** relations correlate keys of several columns of any scalar type ([93f6612](https://github.com/ashtonian/pkg/commit/93f6612344bd50a28f38e42f48ba83ed96f0f8b1))
* **parse:** the wire contract is recorded from the handlers, and the corpora are complete ([0d87165](https://github.com/ashtonian/pkg/commit/0d871659708c6be07f3c4fa770c901601d36443e))


### Bug Fixes

* **parse/core:** stored-document members matched by gowalker's Shape.Match; on gowalker v0.9.0 ([#353](https://github.com/ashtonian/pkg/issues/353)) ([8b9b032](https://github.com/ashtonian/pkg/commit/8b9b032c8e23b3c71a377cd480368c45eb1536c1)), refs [#326](https://github.com/ashtonian/pkg/issues/326)
* **parse/examples/server:** a request that runs out of time is 504, one whose client left is not answered ([606a84f](https://github.com/ashtonian/pkg/commit/606a84f34dfc72c324f193cee03435fd1e42162b)), refs [#280](https://github.com/ashtonian/pkg/issues/280)
* **parse/filter:** $ilike's reference lower-cases, as PostgreSQL does ([6f346d1](https://github.com/ashtonian/pkg/commit/6f346d1d6a6ecb6e6e74c32ee44b58b930d55aaf))
* **parse/filter:** a facet is refused where no backend counts it alike ([2d71387](https://github.com/ashtonian/pkg/commit/2d71387f33fd6957d9d58060f1b6651cb854b847))
* **parse/filter:** a lookup's WhereIn list binds as one set parameter ([4988135](https://github.com/ashtonian/pkg/commit/4988135d4b4163fd7fa7c3df20462c985602168c))
* **parse/filter:** a negative start parameter is a build error, not a panic ([add84de](https://github.com/ashtonian/pkg/commit/add84de16b29e700dde0b36c3f2b914427464760)), refs [#281](https://github.com/ashtonian/pkg/issues/281)
* **parse/filter:** a path member's length cap is one constant the pattern, error and guide state ([2c373ab](https://github.com/ashtonian/pkg/commit/2c373ab35f4c9bf8ad1cd0a673e54606365954fb)), refs [#281](https://github.com/ashtonian/pkg/issues/281)
* **parse/filter:** a relation key comparison counts toward MaxRelationDepth ([b2eee29](https://github.com/ashtonian/pkg/commit/b2eee2934d1f304f65c4e73163c86e4e0e09d222))
* **parse/filter:** a request string holding U+0000 is refused at parse ([fa1fdef](https://github.com/ashtonian/pkg/commit/fa1fdef5868175cda033d34f9b569c8db9ec7211))
* **parse/filter:** an empty page reports no direction ([1efd9eb](https://github.com/ashtonian/pkg/commit/1efd9ebb5ca1832db6f4f12a26bda643b383d485))
* **parse/filter:** each relation facet joins on its own relation's owner key ([a8db96d](https://github.com/ashtonian/pkg/commit/a8db96daedbd1f7b9620a17f6681f213cc8d910d)), refs [#281](https://github.com/ashtonian/pkg/issues/281)
* **parse/filter:** every relative-time token of a request resolves at one instant ([9e3fdfc](https://github.com/ashtonian/pkg/commit/9e3fdfc46f2401a8e7cb936a6d2725251cc6c856)), refs [#281](https://github.com/ashtonian/pkg/issues/281)
* **parse/filter:** PostgreSQL reads every path segment as an object key ([115e06d](https://github.com/ashtonian/pkg/commit/115e06dcf40f2a27181457db79a7cb03aa1bb650))
* **parse/filter:** schema generation refuses an order or sort_default it would ignore ([ae275d6](https://github.com/ashtonian/pkg/commit/ae275d632d8d98cab9d36ad7f12cd3961c5e8dc3))
* **parse:** a ParseError is only ever the request's fault ([cebf6cf](https://github.com/ashtonian/pkg/commit/cebf6cf40324d76c9df1c1fdf22d8f1dafcedd7d))
* **parse:** a query string that does not decode, or a list parameter given twice, is refused ([c7ec7e8](https://github.com/ashtonian/pkg/commit/c7ec7e8e28db586da789c5bbd94690e2a4d1d08f))
* **parse:** a WithPaths view declares no relations and is no relation's target ([ec220fd](https://github.com/ashtonian/pkg/commit/ec220fd2b857df8a0ce5ea5ac95c06095bf1cd28))
* **parse:** lookups resolved outside SQL compare decimals by value, as SQL does ([93f6612](https://github.com/ashtonian/pkg/commit/93f6612344bd50a28f38e42f48ba83ed96f0f8b1))
* **parse:** one error prefix shape; a target key of the wrong type is a key error ([3eef892](https://github.com/ashtonian/pkg/commit/3eef8922319a96b33dfeb480be811a40efde6e1b))
* **parse:** parser limits have one rule, read alike by filters and patches ([8b313fe](https://github.com/ashtonian/pkg/commit/8b313fe479c0948cc5bf952573200fd2ab1b0a5e))
* **parse:** register each name once; Register[T] is for schema-only types ([ab55f2a](https://github.com/ashtonian/pkg/commit/ab55f2a0d5d451fac0f8911dd9a4347d41cf187e))
* **parse:** seal request values to the resource that parsed them; schemas are fixed at construction ([ad220ab](https://github.com/ashtonian/pkg/commit/ad220ab7d9986d8b69346c76f012c1bc289cb801))
* **parse:** the code table is the catalog, and every code answers its own status ([b56d2ec](https://github.com/ashtonian/pkg/commit/b56d2ec45885602fa5c5214def4e138f23f94bb3))
* **parse:** the parse module requires no CLI or config module; the example server is a module of its own ([93f6612](https://github.com/ashtonian/pkg/commit/93f6612344bd50a28f38e42f48ba83ed96f0f8b1))
* **parse:** the schema document lists exactly what the filter parser accepts ([bff37e2](https://github.com/ashtonian/pkg/commit/bff37e25fde537cc38482438311eac22afe137cc))


### Code Refactoring

* **parse:** parsehttp is the one net/http adapter ([3b6aa4a](https://github.com/ashtonian/pkg/commit/3b6aa4a0f5033e94aa1195bd04d35bd435d9752e))
* **parse:** resolve computed columns when SQL is built ([7333813](https://github.com/ashtonian/pkg/commit/7333813b5849d693baf3df8e4adcac01c31d8eeb))

## [0.17.1](https://github.com/ashtonian/pkg/compare/parse/v0.17.0...parse/v0.17.1) (2026-11-22)


### Bug Fixes

* **config, parse:** error text leaves tag values out; config compiles a type once and checks reload tags on gowalker's declared-view walk; on gowalker v0.8.0 ([#349](https://github.com/ashtonian/pkg/issues/349)) ([f6a39ca](https://github.com/ashtonian/pkg/commit/f6a39ca1c551cfeeeeb625851c58161e700fd48d)), refs [#302](https://github.com/ashtonian/pkg/issues/302) [#303](https://github.com/ashtonian/pkg/issues/303) [#304](https://github.com/ashtonian/pkg/issues/304) [#305](https://github.com/ashtonian/pkg/issues/305) [#315](https://github.com/ashtonian/pkg/issues/315)

## [0.17.0](https://github.com/ashtonian/pkg/compare/parse/v0.16.0...parse/v0.17.0) (2026-11-22)


### ⚠ BREAKING CHANGES

* **parse:** raw OR policies held to the tenant floor, patch policy judged by what is written, SDK or() disjoins and reaches the schema routes ([#345](https://github.com/ashtonian/pkg/issues/345))
* **parse:** schema generation, and so NewResource, refuses a struct whose walked fields carry such a key, such as filter_op, minItems or a router's path beside paths. Correct the key, or name another library's with core.WithForeignTagKeys.
* **parse:** a document type with a struct tag reflect cannot read whole, or one that repeats a key, is refused by JSONFieldsOf, and validating a stored document of that type returns the error, as a row type with such a tag already is refused.

### Features

* **parse:** document members on gowalker's WithJSONFields and misspelt tag keys refused; config on gowalker's matcher ([#300](https://github.com/ashtonian/pkg/issues/300)) ([35cc7f0](https://github.com/ashtonian/pkg/commit/35cc7f0911b70804f2db1960441780db1138cf63))


### Bug Fixes

* **parse:** raw OR policies held to the tenant floor, patch policy judged by what is written, SDK or() disjoins and reaches the schema routes ([#345](https://github.com/ashtonian/pkg/issues/345)) ([b479e85](https://github.com/ashtonian/pkg/commit/b479e85321d58fb619820dca3a56ee97ac2b1320))

## [0.16.0](https://github.com/ashtonian/pkg/compare/parse/v0.15.0...parse/v0.16.0) (2026-11-22)


### ⚠ BREAKING CHANGES

* two fields the field-name tag hides that share a column are refused as two fields with one column at any depth. Before, one declared alike deeper than the other was dropped from the walk without a word, and two at one depth failed with a message about hidden fields.
* **parse/core:** one number grammar, document naming as encoding/json does, core.go split, and the defects on those seams ([#296](https://github.com/ashtonian/pkg/issues/296))

### Features

* gowalker names every member encoding/json writes and checks tag-key spelling; parse and config on gowalker v0.6.0 ([#298](https://github.com/ashtonian/pkg/issues/298)) ([b505c00](https://github.com/ashtonian/pkg/commit/b505c0025d65e5fb7299f6faee7acbbd3bb26781)), refs [#288](https://github.com/ashtonian/pkg/issues/288)


### Bug Fixes

* **parse/core:** one number grammar, document naming as encoding/json does, core.go split, and the defects on those seams ([#296](https://github.com/ashtonian/pkg/issues/296)) ([27e34f3](https://github.com/ashtonian/pkg/commit/27e34f30d4082e32488244084fc9a8860a9f5141))

## [0.15.0](https://github.com/ashtonian/pkg/compare/parse/v0.14.0...parse/v0.15.0) (2026-11-22)


### ⚠ BREAKING CHANGES

* **parse:** parse_decimal, parse_decimal_key and parse_decimal_float32 read a BLOB as NULL, so a filter treats a decimal stored as a BLOB (bound from a Go []byte) as missing. Store decimals as TEXT. On SQLite, an index on any of the three over a column holding BLOBs answers with other rows than a scan until it is rebuilt: [convert the BLOBs and REINDEX](https://github.com/ashtonian/pkg/blob/main/parse/parsesqlite/README.md#upgrading-across-a-change-in-results).
* **parse:** parse requires pgx v5.13.0, whose pgx.BatchResults has a Tx() method; a test double of parsepgx.Querier must return a BatchResults that implements it.
* **parse:** a field tagged json:",string" takes its filter operands, merge-patch members and JSON Patch values as JSON strings holding the value. Clients that sent bare numbers or booleans for such a field are refused (E2073 for filters, E2077 for patches). A json:",string" member of a struct document can no longer be declared a filter, element or sort path; drop the option or declare the member a string.
* **parse:** a PostgreSQL timestamp facet bucket is keyed by the canonical text (2026-01-02T01:04:05.500000000Z) instead of the session's timestamptz text. SQLiteDialect.MarshalValue returns a timestamp's canonical text instead of the time.Time, and SQLite rows the application writes itself must store that text (core.FormatTimestamp).
* **parse:** filter.PathValue returns an error for an element path (a field of FieldSchema.Elements). Read it on a decoded element with filter.ElementRow{Element: e}.Value(field), or evaluate the condition with filter.Evaluate(condition, filter.ElementRow{Element: e}).
* **parse:** SQLite geo filters call parsesqlite's functions. A connection without them fails with "no such function: parse_geo_distance" (or parse_geo_in_box, parse_geo_covered_by). Register them with parsesqlite.Register, or with another driver through GeoDistance, GeoInBox and GeoCoveredBy. SQLite $near now measures the ellipsoid and no longer agrees with the spherical distance to within 0.5%.
* **parse:** filter.DialectFromDSN returns ErrUnknownDSN for a bare SQLite name (app.db, data/app.db), for //host/… and for a one-segment /name.db, where it returned SQLiteDialect. Write the path as ./app.db, an absolute path or a file: URI.
* **parse:** filter.DialectFromDSN returns ErrUnknownDSN for a DSN without a URL scheme whose name holds = or @, where it returned SQLiteDialect when the name ended in .db. Construct filter.SQLiteDialect{} for such a path.
* **parse:** a governed list's relation predicates, counts, facets and includes read each row's own tenant's related rows instead of Scope.Tenant's, and Scope.Tenant is ignored when Governance is set on a resource with a tenant column.
* **parse:** filter.DialectFromDSN returns ErrUnknownDSN for cockroach://, crdb:// and any non-SQLite scheme whose path ends in .db, where it returned PostgresDialect or SQLiteDialect. Construct the dialect directly to keep compiling for such a connection; CockroachDB is not supported.
* **parse:** a list request whose fields names no field of the response is E2071 instead of being accepted.
* **parse:** a resource that facets, filters, searches or relates by a column ReadColumns leaves out, or filters by a db:"-" field, now fails at construction. Read the column, or name it in QueryOnlyColumns for filters, search and relations that only filter.
* **parse:** @ashtonian/parse requires Node.js 22.13 or later.
* **parse:** Resource.Columns returns ([]string, error) instead of []string: a dialect a computed column's expression refuses is an error. ReadExpr takes a ColumnExpr, func(filter.Dialect) (string, error), instead of an SQL string; parse.SQL(sql) wraps a string every dialect reads alike.
* **parse:** SQLColumnMapper.MapColumn receives the field's column (display_name for `json:"displayName"`, or the db tag's name) instead of its external name, and the column tag and ColumnConvention apply before the mapper rather than being ignored.
* **parse:** a policy naming a request field whose name is another field's column is refused with filter.ErrAmbiguousPolicyField instead of resolving to the request field; name the request field's column instead. Resource.Relate refuses a relation named like a column of the owner or like a field without the filter capability.
* **parse:** filter.RepositoryScope.TenantID and the tenantID parameter of filter.BuildBaseConstraints are now any, the tenant key in its column's type. NewResource refuses a TenantColumn whose field is not a string, a UUID or an integer.

### Features

* **parse:** accept a list request as a POST JSON body ([#202](https://github.com/ashtonian/pkg/issues/202)) ([68a7ed3](https://github.com/ashtonian/pkg/commit/68a7ed3072ccd158f395cae4c3bfef63b6a5bae2)), refs [#201](https://github.com/ashtonian/pkg/issues/201)
* **parse:** answer parse errors whole, without details JSON cannot hold ([#265](https://github.com/ashtonian/pkg/issues/265)) ([a4f1e18](https://github.com/ashtonian/pkg/commit/a4f1e18d1b990202c64b26f57f6502823d2ded5f)), refs [#264](https://github.com/ashtonian/pkg/issues/264)
* **parse:** bind Scope.Tenant as the tenant column's type ([#198](https://github.com/ashtonian/pkg/issues/198)) ([f2fb1a1](https://github.com/ashtonian/pkg/commit/f2fb1a1281793143ccec45a229e842c40562de83)), refs [#196](https://github.com/ashtonian/pkg/issues/196)
* **parse:** evaluate geo filters in memory and on SQLite as PostGIS does ([#258](https://github.com/ashtonian/pkg/issues/258)) ([bd36d29](https://github.com/ashtonian/pkg/commit/bd36d290ba78fef1f1f20eccf9a745b16b2e748b)), refs [#219](https://github.com/ashtonian/pkg/issues/219) [#220](https://github.com/ashtonian/pkg/issues/220)
* **parse:** let policy constraints name columns requests cannot filter ([#199](https://github.com/ashtonian/pkg/issues/199)) ([008ebee](https://github.com/ashtonian/pkg/commit/008ebeea21d8acec748ec0f36f83b71fc345b6e3)), refs [#197](https://github.com/ashtonian/pkg/issues/197)
* **parse:** read computed columns through declared SQL expressions ([#200](https://github.com/ashtonian/pkg/issues/200)) ([9217002](https://github.com/ashtonian/pkg/commit/92170020b5cbff41f8e7b31c0c71fdc381d5c049)), refs [#195](https://github.com/ashtonian/pkg/issues/195)
* **parse:** read RFC 9457 errors, scope schema aborts per caller and test the SDK's Node floor ([#245](https://github.com/ashtonian/pkg/issues/245)) ([43a50d7](https://github.com/ashtonian/pkg/commit/43a50d7966f0faabd53fdb4b2e0ba8892d451997)), refs [#225](https://github.com/ashtonian/pkg/issues/225) [#223](https://github.com/ashtonian/pkg/issues/223) [#222](https://github.com/ashtonian/pkg/issues/222)
* **parse:** refuse repeated cursor members, require pgx v5.13.0, DSN-only PostgreSQL tests and parsesqlite's function table ([#287](https://github.com/ashtonian/pkg/issues/287)) ([d1953fb](https://github.com/ashtonian/pkg/commit/d1953fb8e24954116f83c855eef85ab18fd30251)), refs [#276](https://github.com/ashtonian/pkg/issues/276) [#213](https://github.com/ashtonian/pkg/issues/213)
* **parse:** report schema discovery encoding and write errors ([#255](https://github.com/ashtonian/pkg/issues/255)) ([ed9ebae](https://github.com/ashtonian/pkg/commit/ed9ebaee23bb54da92127e2c5d54e45a54aba16f)), refs [#215](https://github.com/ashtonian/pkg/issues/215)
* **parse:** take json:",string" fields in their wire form, with schema, Go client and TS SDK parity ([#262](https://github.com/ashtonian/pkg/issues/262)) ([c6b2466](https://github.com/ashtonian/pkg/commit/c6b246605a98bd12b36aad962e1194316d207a62)), refs [#205](https://github.com/ashtonian/pkg/issues/205)


### Bug Fixes

* **parse:** describe parse through this repository's code and refuse private references ([#257](https://github.com/ashtonian/pkg/issues/257)) ([f8e8ee7](https://github.com/ashtonian/pkg/commit/f8e8ee7513b96575b9d45a623578f5d9424bcf7b))
* **parse:** fail an Unsupported conformance case on any error but its refusal ([#250](https://github.com/ashtonian/pkg/issues/250)) ([09b2507](https://github.com/ashtonian/pkg/commit/09b2507273e25314aae0a473f7705e16b81c0cc1)), refs [#211](https://github.com/ashtonian/pkg/issues/211)
* **parse:** hold a governed list's relations to each row's tenant ([#254](https://github.com/ashtonian/pkg/issues/254)) ([9fe6cd0](https://github.com/ashtonian/pkg/commit/9fe6cd003fac3fc2c16844018ba48dda9038c048)), refs [#204](https://github.com/ashtonian/pkg/issues/204)
* **parse:** hold facets, filters, search and relations to the read set ([#248](https://github.com/ashtonian/pkg/issues/248)) ([cc1525e](https://github.com/ashtonian/pkg/commit/cc1525e11a1209949633785ab44362df9ba867a7)), refs [#203](https://github.com/ashtonian/pkg/issues/203)
* **parse:** key timestamp facets by the canonical instant on every backend ([#259](https://github.com/ashtonian/pkg/issues/259)) ([93aa206](https://github.com/ashtonian/pkg/commit/93aa2061b8727805ae876e16e8d0e4876c755d80))
* **parse:** one definition per repeated literal; mark element fields structurally ([#272](https://github.com/ashtonian/pkg/issues/272)) ([1bb94c1](https://github.com/ashtonian/pkg/commit/1bb94c1205df3285b76a10fed7735b84802c9f1b))
* **parse:** read a DSN as SQLite only in a form no other driver takes ([#256](https://github.com/ashtonian/pkg/issues/256)) ([6dd5d32](https://github.com/ashtonian/pkg/commit/6dd5d329ceffda3b1bfcf4a73ae4ee68855d1547))
* **parse:** read array elements through filter.ElementRow, not PathValue ([#253](https://github.com/ashtonian/pkg/issues/253)) ([bc94aa8](https://github.com/ashtonian/pkg/commit/bc94aa826e0ab4bf9d2a115ea12bb69d8149927c))
* **parse:** refuse CockroachDB DSNs and keep DSNs out of their errors ([#251](https://github.com/ashtonian/pkg/issues/251)) ([25e291b](https://github.com/ashtonian/pkg/commit/25e291b514279a582a37030256598a47915c74e6)), refs [#208](https://github.com/ashtonian/pkg/issues/208)
* **parse:** refuse unknown fields names and document the field mask ([#252](https://github.com/ashtonian/pkg/issues/252)) ([1142f78](https://github.com/ashtonian/pkg/commit/1142f78e64d4868f36cf5f5001ecc6b90920f28a))

## [0.14.0](https://github.com/ashtonian/pkg/compare/parse/v0.13.0...parse/v0.14.0) (2026-11-17)


### ⚠ BREAKING CHANGES

* **parse:** canonical value types for TIME, INTERVAL, INET, CIDR and MACADDR ([#187](https://github.com/ashtonian/pkg/issues/187)): a column declared core.SQLInterval or core.SQLIntervalArray reads as ISO 8601 text (P1DT2H) where it read the session's IntervalStyle text (1 day 02:00:00).

### Features

* **parse:** canonical value types for TIME, INTERVAL, INET, CIDR and MACADDR ([#187](https://github.com/ashtonian/pkg/issues/187)) ([35bda0a](https://github.com/ashtonian/pkg/commit/35bda0a620357e371b3d68c36844edef74506cd3)): core.LocalTime, core.Interval, core.Inet, core.CIDR and core.MACAddr, one canonical text each on SQLite and PostgreSQL; an interval filter compares canonical text, so P30D does not match P1M; interval, inet and cidr fields cannot be facets

## [0.13.0](https://github.com/ashtonian/pkg/compare/parse/v0.12.0...parse/v0.13.0) (2026-11-09)


### ⚠ BREAKING CHANGES

* **parse:** order terms facet ties on numbers by value ([#154](https://github.com/ashtonian/pkg/issues/154)): FacetBuckets takes the facet's *FieldSchema instead of its *FacetSchema; pass the field (nil is a terms facet of strings).
* **parse:** key numeric facet terms by canonical decimal text ([#157](https://github.com/ashtonian/pkg/issues/157)): numeric facet keys change text (10.0 is 10, 1e+21 is 1000000000000000000000). SQLite facets of float fields call parse_decimal_float32, which parsesqlite.Register now registers; a driver registering parse's functions itself must add DecimalFloat32.

### Features

* **parse:** export keyset ORDER BY and seek rendering ([#161](https://github.com/ashtonian/pkg/issues/161)) ([276f48a](https://github.com/ashtonian/pkg/commit/276f48ae66e0fb4495474ac817617176147cfcbb)), closes [#160](https://github.com/ashtonian/pkg/issues/160)
* **parse:** facet the rows of a caller FROM clause (WithFacetFrom) ([#159](https://github.com/ashtonian/pkg/issues/159)) ([b091770](https://github.com/ashtonian/pkg/commit/b091770e7b1c1cd14435bc9dad01763f4d823c79)), closes [#158](https://github.com/ashtonian/pkg/issues/158)
* **parse:** read a document decoded once with filter.Document ([#164](https://github.com/ashtonian/pkg/issues/164)) ([098bb0d](https://github.com/ashtonian/pkg/commit/098bb0d04a741f7a90aa4caf10bd69f57b5fdfe0)), closes [#163](https://github.com/ashtonian/pkg/issues/163)

### Bug Fixes

* **parse:** bind an array $contains list as one set ([#152](https://github.com/ashtonian/pkg/issues/152)) ([e3093f7](https://github.com/ashtonian/pkg/commit/e3093f725d671d98d0d83cedd37b91a53b890675)), closes [#151](https://github.com/ashtonian/pkg/issues/151)
* **parse:** key numeric facet terms by canonical decimal text ([#157](https://github.com/ashtonian/pkg/issues/157)) ([f957fef](https://github.com/ashtonian/pkg/commit/f957fef5fcc1c365053f6b84048b6b39a8eab112)), closes [#155](https://github.com/ashtonian/pkg/issues/155)
* **parse:** order terms facet ties on numbers by value ([#154](https://github.com/ashtonian/pkg/issues/154)) ([05f3d54](https://github.com/ashtonian/pkg/commit/05f3d540ba3947c9a80fa442c046146549b1f639)), closes [#153](https://github.com/ashtonian/pkg/issues/153)

## [0.12.0](https://github.com/ashtonian/pkg/compare/parse/v0.11.0...parse/v0.12.0) (2026-11-09)


### ⚠ BREAKING CHANGES

* **parse:** compare SQLite decimals exactly through registered functions ([#149](https://github.com/ashtonian/pkg/issues/149)): a SQLite database that filters, sorts or facets a decimal field must call parsesqlite.Register before it opens a connection; otherwise the statement fails with "no such function: parse_decimal". Declare decimal columns TEXT to hold every decimal exactly. Dialect has two new methods.

### Features

* **parse:** bind $in / $nin as one parameter and compare jsonb paths by containment where exact ([#148](https://github.com/ashtonian/pkg/issues/148)) ([89f7bdd](https://github.com/ashtonian/pkg/commit/89f7bddb844386c02ace9335e9ac401030d5e36d)), closes [#142](https://github.com/ashtonian/pkg/issues/142)
* **parse:** compare SQLite decimals exactly through registered functions ([#149](https://github.com/ashtonian/pkg/issues/149)) ([1392480](https://github.com/ashtonian/pkg/commit/13924801dcbbd9a1dfdf0459bddf339aa8add2ca)), closes [#144](https://github.com/ashtonian/pkg/issues/144)
* **parse:** evaluate filters outside SQL with the reference evaluator ([#146](https://github.com/ashtonian/pkg/issues/146)) ([6e99df1](https://github.com/ashtonian/pkg/commit/6e99df191e950094d717ade9bd09a7aad8835723)), closes [#145](https://github.com/ashtonian/pkg/issues/145)
* **parse:** filter scalar arrays by element conditions and declare array paths ([#150](https://github.com/ashtonian/pkg/issues/150)) ([bf7e70b](https://github.com/ashtonian/pkg/commit/bf7e70b67debb3f011571f8076b6fb74e6509b70)), closes [#143](https://github.com/ashtonian/pkg/issues/143)

## [0.11.0](https://github.com/ashtonian/pkg/compare/parse/v0.10.1...parse/v0.11.0) (2026-11-08)


### Features

* **parse:** compile bounded varied row update batches ([#139](https://github.com/ashtonian/pkg/issues/139)) ([eb1f4eb](https://github.com/ashtonian/pkg/commit/eb1f4eb7c65ffb8e265c475b98a45fe6094a5f32))

## [0.10.1](https://github.com/ashtonian/pkg/compare/parse/v0.10.0...parse/v0.10.1) (2026-11-06)


### Bug Fixes

* Publish SDK artifacts using an explicit local path so npm 12 does not interpret them as GitHub package references ([#116](https://github.com/ashtonian/pkg/issues/116)).

## [0.10.0](https://github.com/ashtonian/pkg/compare/parse/v0.9.0...parse/v0.10.0) (2026-11-06)

### Features

* Name declared constraints and aggregate independent patch failures in Go and TypeScript while retaining first-error access ([#109](https://github.com/ashtonian/pkg/pull/109)).
* Publish verified SDK packages from immutable releases through GitHub OIDC, with package integrity and fresh registry-consumer checks ([#111](https://github.com/ashtonian/pkg/pull/111)).

## [0.9.0](https://github.com/ashtonian/pkg/compare/parse/v0.8.0...parse/v0.9.0) (2026-11-04)


### Features

* **parse:** expose the default patch operation bound ([dda8ca6](https://github.com/ashtonian/pkg/commit/dda8ca621696dc1955cbe41702a5325e78bb52d0))

## [0.8.0](https://github.com/ashtonian/pkg/compare/parse/v0.7.0...parse/v0.8.0) (2026-11-02)


### Features

* **parse:** SkipUnchanged updates only the rows a patch changes ([2fc0ed9](https://github.com/ashtonian/pkg/commit/2fc0ed98e51c8c9d328a2d805af6df83b9756d0d))

## [0.7.0](https://github.com/ashtonian/pkg/compare/parse/v0.6.0...parse/v0.7.0) (2026-10-29)


### Features

* **parse:** minitems and maxitems bound an array's element count ([646cc80](https://github.com/ashtonian/pkg/commit/646cc804dfb4dc05939c558186a67ed30b3e9305))

## [0.6.0](https://github.com/ashtonian/pkg/compare/parse/v0.5.0...parse/v0.6.0) (2026-10-29)


### Features

* **parse:** Increment moves a version column in the update itself ([8b1187d](https://github.com/ashtonian/pkg/commit/8b1187d601a2ed6c3a53b9a19c5210008e361250))

## [0.5.0](https://github.com/ashtonian/pkg/compare/parse/v0.4.0...parse/v0.5.0) (2026-10-29)


### Features

* **parse:** minlen, maxlen, min and max bound patch values ([0afef95](https://github.com/ashtonian/pkg/commit/0afef9508f532a105c35c3c745006d6b53e215cc))


### Bug Fixes

* **parse:** a null clears a nullable enum field ([59c2f9c](https://github.com/ashtonian/pkg/commit/59c2f9ce0a45847bf0f97c42e0c27e2d4abe4df1))

## [0.4.0](https://github.com/ashtonian/pkg/compare/parse/v0.3.0...parse/v0.4.0) (2026-10-29)


### Features

* **parse:** decimal scalar type ([b9978c7](https://github.com/ashtonian/pkg/commit/b9978c74e95e5ed690e2d58de669f11b20134402))

## [0.3.0](https://github.com/ashtonian/pkg/compare/parse/v0.2.0...parse/v0.3.0) (2026-10-27)


### Features

* **parse:** name each projected column with Resource.ColumnNames ([cc35af3](https://github.com/ashtonian/pkg/commit/cc35af3008ff9437d4708783dfac478a7bfc7fbc))

## [0.2.0](https://github.com/ashtonian/pkg/compare/parse/v0.1.0...parse/v0.2.0) (2026-10-26)


### Features

* **parse:** preserve civil dates across query and SQL boundaries ([f898f6a](https://github.com/ashtonian/pkg/commit/f898f6af9bbccada60c45c2382d15157cd948c90))


### Bug Fixes

* **parse:** preserve nullable civil-date storage and legacy SQLite reads ([e1d6b4b](https://github.com/ashtonian/pkg/commit/e1d6b4b3a3e92fb284d6ab25dc925c32917c2030))

## 0.1.0 (2026-10-25)

### Added

- Strict Resource-based filters, sorting, keyset pages, facets and includes with tenant and policy constraints, typed JSON paths and per-call filter cost budgets.
- Validated merge patches and JSON Patch, explicit JSON/native-array storage, and PostgreSQL/SQLite query and update plans.
- Optional pgx execution with native field mapping, fresh typed JSON decoding, canonical-key validation, SQL NULL handling and transactional relation updates.
- Native error codes and HTTP mapping, versioned schema documents, standard-library-only Go request builders and TypeScript builders with shared conformance corpora.
- PostgreSQL 17/18 and SQLite acceptance, standalone Go module/archive consumers and packed TypeScript package checks.

Go requires 1.27 or later. Pre-1.0 minor releases may change APIs. npm publication is tracked separately from the Go module release.
