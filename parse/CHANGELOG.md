# Changelog

## [0.15.0](https://github.com/ashtonian/pkg/compare/parse/v0.14.0...parse/v0.15.0) (2026-11-21)


### ⚠ BREAKING CHANGES

* **parse:** hold a governed list's relations to each row's tenant ([#254](https://github.com/ashtonian/pkg/issues/254))
* **parse:** refuse CockroachDB DSNs and keep DSNs out of their errors ([#251](https://github.com/ashtonian/pkg/issues/251))
* **parse:** refuse unknown fields names and document the field mask ([#252](https://github.com/ashtonian/pkg/issues/252))
* **parse:** a governed list's relation predicates, counts, facets and includes read each row's own tenant's related rows instead of Scope.Tenant's, and Scope.Tenant is ignored when Governance is set on a resource with a tenant column.
* **parse:** a list request whose fields names no field of the response is E2071 instead of being accepted.
* **parse:** filter.DialectFromDSN returns ErrUnknownDSN for cockroach://, crdb:// and any non-SQLite scheme whose path ends in .db, where it returned PostgresDialect or SQLiteDialect. Construct the dialect directly to keep compiling for such a connection; CockroachDB is not supported.
* **parse:** hold facets, filters, search and relations to the read set ([#248](https://github.com/ashtonian/pkg/issues/248))
* **parse:** read RFC 9457 errors, scope schema aborts per caller and test the SDK's Node floor ([#245](https://github.com/ashtonian/pkg/issues/245))
* **parse:** a resource that facets, filters, searches or relates by a column ReadColumns leaves out, or filters by a db:"-" field, now fails at construction. Read the column, or name it in QueryOnlyColumns for filters, search and relations that only filter.
* **parse:** read computed columns through declared SQL expressions ([#200](https://github.com/ashtonian/pkg/issues/200))
* **parse:** let policy constraints name columns requests cannot filter ([#199](https://github.com/ashtonian/pkg/issues/199))
* **parse:** bind Scope.Tenant as the tenant column's type ([#198](https://github.com/ashtonian/pkg/issues/198))
* **parse:** @ashtonian/parse requires Node.js 22.13 or later.
* **parse:** Resource.Columns returns ([]string, error) instead of []string: a dialect a computed column's expression refuses is an error. ReadExpr takes a ColumnExpr, func(filter.Dialect) (string, error), instead of an SQL string; parse.SQL(sql) wraps a string every dialect reads alike.
* **parse:** SQLColumnMapper.MapColumn receives the field's column (display_name for `json:"displayName"`, or the db tag's name) instead of its external name, and the column tag and ColumnConvention apply before the mapper rather than being ignored.
* **parse:** resolve computed column expressions per dialect
* **parse:** a policy naming a request field whose name is another field's column is refused with filter.ErrAmbiguousPolicyField instead of resolving to the request field; name the request field's column instead. Resource.Relate refuses a relation named like a column of the owner or like a field without the filter capability.
* **parse:** filter.RepositoryScope.TenantID and the tenantID parameter of filter.BuildBaseConstraints are now any, the tenant key in its column's type. NewResource refuses a TenantColumn whose field is not a string, a UUID or an integer.

### Features

* **parse:** accept a list request as a POST JSON body ([cc1daa8](https://github.com/ashtonian/pkg/commit/cc1daa8a7e697acc9f4a7ac8911041666b98b158)), closes [#201](https://github.com/ashtonian/pkg/issues/201)
* **parse:** accept a list request as a POST JSON body ([#202](https://github.com/ashtonian/pkg/issues/202)) ([68a7ed3](https://github.com/ashtonian/pkg/commit/68a7ed3072ccd158f395cae4c3bfef63b6a5bae2))
* **parse:** bind Scope.Tenant as the tenant column's type ([7cf155b](https://github.com/ashtonian/pkg/commit/7cf155b2d03a54c82599c892481bb4f08b131529)), closes [#196](https://github.com/ashtonian/pkg/issues/196)
* **parse:** bind Scope.Tenant as the tenant column's type ([#198](https://github.com/ashtonian/pkg/issues/198)) ([f2fb1a1](https://github.com/ashtonian/pkg/commit/f2fb1a1281793143ccec45a229e842c40562de83))
* **parse:** compile policy fields over computed columns per dialect ([f203718](https://github.com/ashtonian/pkg/commit/f20371825b99a1dae2f602bc06ad677834fec60a))
* **parse:** filter relations by their targets' computed columns per dialect ([c746b2b](https://github.com/ashtonian/pkg/commit/c746b2bf8e4f402a62e576b3b3e6f24731eed2f3))
* **parse:** let policy constraints name columns requests cannot filter ([69ba4c7](https://github.com/ashtonian/pkg/commit/69ba4c75639a241fd38d5533eb65269606d82a90)), closes [#197](https://github.com/ashtonian/pkg/issues/197)
* **parse:** let policy constraints name columns requests cannot filter ([#199](https://github.com/ashtonian/pkg/issues/199)) ([008ebee](https://github.com/ashtonian/pkg/commit/008ebeea21d8acec748ec0f36f83b71fc345b6e3))
* **parse:** read computed columns through declared SQL expressions ([3a2d0ea](https://github.com/ashtonian/pkg/commit/3a2d0eaca9945dd6243cfaffdfa186213724d079)), closes [#195](https://github.com/ashtonian/pkg/issues/195)
* **parse:** read computed columns through declared SQL expressions ([#200](https://github.com/ashtonian/pkg/issues/200)) ([9217002](https://github.com/ashtonian/pkg/commit/92170020b5cbff41f8e7b31c0c71fdc381d5c049))
* **parse:** read RFC 9457 errors, scope schema aborts per caller and test the SDK's Node floor ([#245](https://github.com/ashtonian/pkg/issues/245)) ([43a50d7](https://github.com/ashtonian/pkg/commit/43a50d7966f0faabd53fdb4b2e0ba8892d451997))
* **parse:** read RFC 9457 problem details into the SDK's ApiError ([2c5139e](https://github.com/ashtonian/pkg/commit/2c5139ee999d41d49518da15506880e70257f3c3)), closes [#223](https://github.com/ashtonian/pkg/issues/223)
* **parse:** require Node.js 22.13 for the SDK and test it at that floor ([92cffee](https://github.com/ashtonian/pkg/commit/92cffee8ebadf03c49e97eaddf584c4d26394d34)), closes [#225](https://github.com/ashtonian/pkg/issues/225)
* **parse:** resolve computed column expressions per dialect ([1b079fa](https://github.com/ashtonian/pkg/commit/1b079fac7bd678247162024a17a07cb23796a561))
* **parse:** validate a POST list from its parts with ParseListPost ([db86064](https://github.com/ashtonian/pkg/commit/db8606439ea8bb5f65000d969533c5be3ee304cc))


### Bug Fixes

* **filter:** fail closed on a missing tenant in BuildBaseConstraints ([f041162](https://github.com/ashtonian/pkg/commit/f04116217f7ea2626c47129bf72df0711e59fae2))
* **parse:** bind an update's tenant once, at the entry point ([8b21034](https://github.com/ashtonian/pkg/commit/8b21034f676173955ccc625abc38a281b4655200))
* **parse:** bound a POST list body by its own limit, WithMaxListBodySize ([c99b270](https://github.com/ashtonian/pkg/commit/c99b27029e0b45fe4d65932947741af5d6bfd56d))
* **parse:** check Scope.Tenant against every tenant column a request reaches ([7050500](https://github.com/ashtonian/pkg/commit/705050005061368e59d1c7bb140d93daa84b679a))
* **parse:** end only the aborting caller's wait on a shared schema fetch ([6f13866](https://github.com/ashtonian/pkg/commit/6f138667f452c69594e8c6124ab1afe987174661)), closes [#222](https://github.com/ashtonian/pkg/issues/222)
* **parse:** fail an Unsupported conformance case on any error but its refusal ([eb474a5](https://github.com/ashtonian/pkg/commit/eb474a5d1c37dd85e479e58736a1e51255e5ce95)), closes [#211](https://github.com/ashtonian/pkg/issues/211)
* **parse:** fail an Unsupported conformance case on any error but its refusal ([#250](https://github.com/ashtonian/pkg/issues/250)) ([09b2507](https://github.com/ashtonian/pkg/commit/09b2507273e25314aae0a473f7705e16b81c0cc1))
* **parse:** find a computed column's fields by their column ([ebbae6b](https://github.com/ashtonian/pkg/commit/ebbae6b7a39f96e8053c61438bd12c649d7e4ba4))
* **parse:** hand a ColumnMapper each field's column ([cf2aa69](https://github.com/ashtonian/pkg/commit/cf2aa698fe9b6a00b0edae07c691bd8d349b4ffa))
* **parse:** hold a governed list's relations to each row's tenant ([f4ac575](https://github.com/ashtonian/pkg/commit/f4ac575e860a56e8b7bf3db3f6db84227b7708b1)), closes [#204](https://github.com/ashtonian/pkg/issues/204)
* **parse:** hold a governed list's relations to each row's tenant ([#254](https://github.com/ashtonian/pkg/issues/254)) ([9fe6cd0](https://github.com/ashtonian/pkg/commit/9fe6cd003fac3fc2c16844018ba48dda9038c048))
* **parse:** hold facets, filters, search and relations to the read set ([52ef1a7](https://github.com/ashtonian/pkg/commit/52ef1a7ffdab71e269bf52c398743df5cd49e8a4)), closes [#203](https://github.com/ashtonian/pkg/issues/203)
* **parse:** hold facets, filters, search and relations to the read set ([#248](https://github.com/ashtonian/pkg/issues/248)) ([cc1525e](https://github.com/ashtonian/pkg/commit/cc1525e11a1209949633785ab44362df9ba867a7))
* **parse:** make the SDK's POST list transport opt-in ([9c8790a](https://github.com/ashtonian/pkg/commit/9c8790acbffe6ece9157ff9bb27b73ad27cb80dd))
* **parse:** match DSN schemes case-insensitively and read SQLite names before a query ([5d60520](https://github.com/ashtonian/pkg/commit/5d60520ce94b976e31629ed0f3aeded1a1af822e))
* **parsepgx:** read computed columns after an update's relation changes ([9e0842a](https://github.com/ashtonian/pkg/commit/9e0842ae240e59d956efef99025848a2e51d9d85))
* **parse:** read only unmistakable problem documents and keep a settled schema ([f2be114](https://github.com/ashtonian/pkg/commit/f2be114a1833b2fdc308f187010f6c28545e0bfd))
* **parse:** refuse a relation tag correlating on a computed column ([5b46001](https://github.com/ashtonian/pkg/commit/5b46001b0a816db59a5c316c759a116e1ac1d5ed))
* **parse:** refuse CockroachDB DSNs and keep DSNs out of their errors ([0f6b2e5](https://github.com/ashtonian/pkg/commit/0f6b2e58b01e6b3bd0d09e771c2a6eb1956c99dd)), closes [#208](https://github.com/ashtonian/pkg/issues/208)
* **parse:** refuse CockroachDB DSNs and keep DSNs out of their errors ([#251](https://github.com/ashtonian/pkg/issues/251)) ([25e291b](https://github.com/ashtonian/pkg/commit/25e291b514279a582a37030256598a47915c74e6))
* **parse:** refuse request paths into a computed document column ([52a4414](https://github.com/ashtonian/pkg/commit/52a44144e41cbfe88b501e5f6e3c50cbdbeab8e1))
* **parse:** refuse stamping or incrementing a computed column ([7ff9ac5](https://github.com/ashtonian/pkg/commit/7ff9ac56f8490d28a9a72bdddb8da6612eee77d2))
* **parse:** refuse unknown fields names and document the field mask ([2f1a1f2](https://github.com/ashtonian/pkg/commit/2f1a1f28cbd082a3de11351c94f42cd85c38195c))
* **parse:** refuse unknown fields names and document the field mask ([#252](https://github.com/ashtonian/pkg/issues/252)) ([1142f78](https://github.com/ashtonian/pkg/commit/1142f78e64d4868f36cf5f5001ecc6b90920f28a))
* **parse:** resolve a policy field name to exactly one field ([02a0fd1](https://github.com/ashtonian/pkg/commit/02a0fd1adedd8e6cdc4fd552d11d05112630682c))
* **parse:** serve POST lists in the example server ([12110d2](https://github.com/ashtonian/pkg/commit/12110d27f8a87a4eefcaab837f145512f21571bf))
* **parse:** set Unsupported and UnsupportedErr together, share the id comparison ([e881fa9](https://github.com/ashtonian/pkg/commit/e881fa94dd8d192684493e033fea9e82ca883ef5))

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
