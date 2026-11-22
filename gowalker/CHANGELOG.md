# Changelog

## [0.7.0](https://github.com/ashtonian/pkg/compare/gowalker/v0.6.0...gowalker/v0.7.0) (2026-11-22)


### ⚠ BREAKING CHANGES

* gowalker names every member encoding/json writes and checks tag-key spelling; parse and config on gowalker v0.6.0 ([#298](https://github.com/ashtonian/pkg/issues/298))

### Features

* gowalker names every member encoding/json writes and checks tag-key spelling; parse and config on gowalker v0.6.0 ([#298](https://github.com/ashtonian/pkg/issues/298)) ([b505c00](https://github.com/ashtonian/pkg/commit/b505c0025d65e5fb7299f6faee7acbbd3bb26781))

## [0.6.0](https://github.com/ashtonian/pkg/compare/gowalker/v0.5.0...gowalker/v0.6.0) (2026-11-22)


### ⚠ BREAKING CHANGES

* **gowalker:** compiling a type fails with ErrSchema, wrapping a *TagError that names the field, when a struct tag of a field gowalker reads is not key:"value" pairs that reflect.StructTag reads to the end (such as `json:"a" parse: "filter"`, with a space after the colon) or repeats a key. reflect.StructTag.Lookup reported every key after such a mistake as absent; correct the tag.

### Features

* **gowalker:** encoding/json names, strict struct tags, unnamed fields and declarations, Accessor.Locate on static routes, unbounded cache with stats ([80f1b3c](https://github.com/ashtonian/pkg/commit/80f1b3cfea49f860e07c696d79b4d4532e590156))

## [0.5.0](https://github.com/ashtonian/pkg/compare/gowalker/v0.4.0...gowalker/v0.5.0) (2026-10-20)


### ⚠ BREAKING CHANGES

* **gowalker:** pass ResolveByDepth to New or MustNew instead of to WithTaggedFields.

### Features

* **gowalker:** ResolveByDepth is an engine option and applies to field resolvers ([31c2c0e](https://github.com/ashtonian/pkg/commit/31c2c0e7751f6bb00c509379e040695c61888a45))

## [0.4.0](https://github.com/ashtonian/pkg/compare/gowalker/v0.3.0...gowalker/v0.4.0) (2026-10-20)


### Features

* **gowalker:** ResolveByDepth, DashCommaName and ValidName tag options name the members encoding/json writes ([cf0d2b1](https://github.com/ashtonian/pkg/commit/cf0d2b19bac017a81b6e7a450b734f4a868def84))

## [0.3.0](https://github.com/ashtonian/pkg/compare/gowalker/v0.2.0...gowalker/v0.3.0) (2026-10-19)


### ⚠ BREAKING CHANGES

* **gowalker:** error text now names the operation's Go type and its cause, and NameConflict names both fields; access errors still omit path keys and values. Multiple WithAtomicType options accumulate instead of the last one replacing the others.

### Features

* **gowalker:** tag naming conventions, per-type metadata and a default engine ([451d527](https://github.com/ashtonian/pkg/commit/451d5273da8d9c92d342374ab33da7c240ebe4bb))

## [0.2.0](https://github.com/ashtonian/pkg/compare/gowalker/v0.1.1...gowalker/v0.2.0) (2026-10-18)


### Features

* **gowalker:** simplify struct tag discovery and named field access ([c7af6a5](https://github.com/ashtonian/pkg/commit/c7af6a5f646aa796689a48d249adb6813e5e2b9e))

## [0.1.1](https://github.com/ashtonian/pkg/compare/gowalker/v0.1.0...gowalker/v0.1.1) (2026-10-18)


### Bug Fixes

* **gowalker:** clarify reference write contracts ([961de4a](https://github.com/ashtonian/pkg/commit/961de4ae67a86161f43dc01c27ff4db45d02e16b))

## 0.1.0 (2026-10-18)


### Features

* **gowalker:** add standalone reflection module ([a5cad04](https://github.com/ashtonian/pkg/commit/a5cad047b883196c4ef43054fce242d63009f334))
