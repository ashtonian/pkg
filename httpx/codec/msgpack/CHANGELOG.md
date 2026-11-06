# Changelog

## [0.2.0](https://github.com/ashtonian/pkg/compare/httpx/codec/msgpack/v0.1.0...httpx/codec/msgpack/v0.2.0) (2026-11-06)

### Breaking changes

* The default `Codec` follows JSON field selection and value semantics, including custom JSON encoders. Select `NativeCodec` explicitly for native MessagePack tags, binary values and extensions.

### Features and fixes

* Preserve JSON names, omitted/private fields, string UUID/time representations and ordered duplicate members. Reject unrepresentable integers and excessive nesting ([#114](https://github.com/ashtonian/pkg/pull/114)).

## 0.1.0 (2026-10-15)


### Features

* **httpx:** promote hardened HTTP client and optional modules ([#14](https://github.com/ashtonian/pkg/issues/14)) ([1e95674](https://github.com/ashtonian/pkg/commit/1e95674d053b6904ba1459f07e44815028da0d9f))


### Bug Fixes

* **httpx:** preserve cache cancellation and correct capability claims ([#19](https://github.com/ashtonian/pkg/issues/19)) ([fdfe4d0](https://github.com/ashtonian/pkg/commit/fdfe4d0d54cced179d6de3fe67966def8192b9e9))
