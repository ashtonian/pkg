# Changelog

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
* **parse:** SkipUnchanged updates only the rows a patch changes ([c6c8005](https://github.com/ashtonian/pkg/commit/c6c80059fae92e9fc9e2228e357b5dc6a16b3e87))

## [0.7.0](https://github.com/ashtonian/pkg/compare/parse/v0.6.0...parse/v0.7.0) (2026-10-29)


### Features

* **parse:** minitems and maxitems bound an array's element count ([646cc80](https://github.com/ashtonian/pkg/commit/646cc804dfb4dc05939c558186a67ed30b3e9305))
* **parse:** minitems and maxitems bound an array's element count ([df9f4f3](https://github.com/ashtonian/pkg/commit/df9f4f3f598f3c4ed5257eb221da0a2d69906eb3))

## [0.6.0](https://github.com/ashtonian/pkg/compare/parse/v0.5.0...parse/v0.6.0) (2026-10-29)


### Features

* **parse:** Increment moves a version column in the update itself ([8b1187d](https://github.com/ashtonian/pkg/commit/8b1187d601a2ed6c3a53b9a19c5210008e361250))
* **parse:** Increment moves a version column in the update itself ([302fa6e](https://github.com/ashtonian/pkg/commit/302fa6ec868639feb98b384298fb9a85c57a136e))

## [0.5.0](https://github.com/ashtonian/pkg/compare/parse/v0.4.0...parse/v0.5.0) (2026-10-29)


### Features

* **parse:** minlen, maxlen, min and max bound patch values ([0afef95](https://github.com/ashtonian/pkg/commit/0afef9508f532a105c35c3c745006d6b53e215cc))
* **parse:** minlen, maxlen, min and max bound patch values ([bf85e3a](https://github.com/ashtonian/pkg/commit/bf85e3a7d053e258371d27642b4f721b61ec364c))


### Bug Fixes

* **parse:** a null clears a nullable enum field ([59c2f9c](https://github.com/ashtonian/pkg/commit/59c2f9ce0a45847bf0f97c42e0c27e2d4abe4df1))

## [0.4.0](https://github.com/ashtonian/pkg/compare/parse/v0.3.0...parse/v0.4.0) (2026-10-29)


### Features

* **parse:** decimal scalar type ([b9978c7](https://github.com/ashtonian/pkg/commit/b9978c74e95e5ed690e2d58de669f11b20134402))
* **parse:** decimal scalar type ([2b27496](https://github.com/ashtonian/pkg/commit/2b27496460c948289c4fb0f1619259ee5f15f87f))

## [0.3.0](https://github.com/ashtonian/pkg/compare/parse/v0.2.0...parse/v0.3.0) (2026-10-27)


### Features

* **parse:** name each projected column with Resource.ColumnNames ([cc35af3](https://github.com/ashtonian/pkg/commit/cc35af3008ff9437d4708783dfac478a7bfc7fbc))
* **parse:** name each projected column with Resource.ColumnNames ([350c38e](https://github.com/ashtonian/pkg/commit/350c38ea399d5821228e9c7da77e632e4b105eda))

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
