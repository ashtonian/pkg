# Changelog

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
