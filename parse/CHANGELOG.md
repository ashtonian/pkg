# Changelog

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
