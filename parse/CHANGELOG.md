# Changelog

## [0.13.0](https://github.com/ashtonian/pkg/compare/parse/v0.12.0...parse/v0.13.0) (2026-11-09)


### ⚠ BREAKING CHANGES

* **parse:** key numeric facet terms by canonical decimal text ([#157](https://github.com/ashtonian/pkg/issues/157))
* **parse:** order terms facet ties on numbers by value ([#154](https://github.com/ashtonian/pkg/issues/154))
* **parse:** numeric facet keys change text (10.0 is 10, 1e+21 is 1000000000000000000000). SQLite facets of float fields call parse_decimal_float32, which parsesqlite.Register now registers; a driver registering parse's functions itself must add DecimalFloat32.
* **parse:** FacetBuckets takes the facet's *FieldSchema instead of its *FacetSchema; pass the field (nil is a terms facet of strings).

### Features

* **parse:** export keyset ORDER BY and seek rendering ([4c31f44](https://github.com/ashtonian/pkg/commit/4c31f4424e6416cc1f997fede36480cfa8edcd23)), closes [#160](https://github.com/ashtonian/pkg/issues/160)
* **parse:** export keyset ORDER BY and seek rendering ([#161](https://github.com/ashtonian/pkg/issues/161)) ([276f48a](https://github.com/ashtonian/pkg/commit/276f48ae66e0fb4495474ac817617176147cfcbb))
* **parse:** facet the rows of a caller FROM clause (WithFacetFrom) ([#159](https://github.com/ashtonian/pkg/issues/159)) ([b091770](https://github.com/ashtonian/pkg/commit/b091770e7b1c1cd14435bc9dad01763f4d823c79))
* **parse:** facet the rows of a caller's FROM clause ([1c745c8](https://github.com/ashtonian/pkg/commit/1c745c829472f1cbe7355d687da83025f850087a))
* **parse:** read a document decoded once with filter.Document ([13e6521](https://github.com/ashtonian/pkg/commit/13e65218469957c356f37afaccb89582de1eeb0a))
* **parse:** read a document decoded once with filter.Document ([#164](https://github.com/ashtonian/pkg/issues/164)) ([098bb0d](https://github.com/ashtonian/pkg/commit/098bb0d04a741f7a90aa4caf10bd69f57b5fdfe0))


### Bug Fixes

* **parse:** bind an array $contains list as one set ([7c3feab](https://github.com/ashtonian/pkg/commit/7c3feab9305642a71d0aef34af74c82cac6b92d1)), closes [#151](https://github.com/ashtonian/pkg/issues/151)
* **parse:** bind an array $contains list as one set ([#152](https://github.com/ashtonian/pkg/issues/152)) ([e3093f7](https://github.com/ashtonian/pkg/commit/e3093f725d671d98d0d83cedd37b91a53b890675))
* **parse:** key numeric facet terms by canonical decimal text ([e3ec21a](https://github.com/ashtonian/pkg/commit/e3ec21a9af28657aeb3083fa0e4674b71f6a2e60))
* **parse:** key numeric facet terms by canonical decimal text ([#157](https://github.com/ashtonian/pkg/issues/157)) ([f957fef](https://github.com/ashtonian/pkg/commit/f957fef5fcc1c365053f6b84048b6b39a8eab112))
* **parse:** order terms facet ties on numbers by value ([a8c4c0f](https://github.com/ashtonian/pkg/commit/a8c4c0f36b930d6db181bf07de66d2c8467e5df4))
* **parse:** order terms facet ties on numbers by value ([#154](https://github.com/ashtonian/pkg/issues/154)) ([05f3d54](https://github.com/ashtonian/pkg/commit/05f3d540ba3947c9a80fa442c046146549b1f639))

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
