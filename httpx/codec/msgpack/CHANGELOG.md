# Changelog

## [0.2.0](https://github.com/ashtonian/pkg/compare/httpx/codec/msgpack/v0.1.0...httpx/codec/msgpack/v0.2.0) (2026-11-06)


### ⚠ BREAKING CHANGES

* **httpx:** the default MessagePack codec now follows JSON field and value semantics. Select NativeCodec explicitly for native MessagePack peers.

### Features

* **httpx:** add JSON representations and URL privacy policies ([939f5a9](https://github.com/ashtonian/pkg/commit/939f5a961cd6ac8e4972a60a35ef933bcb2d1cb9))
* **httpx:** preserve JSON representations, problem details and private URLs ([caf019f](https://github.com/ashtonian/pkg/commit/caf019fbc76990e0b4ff7136ca8034e6393c7ef9))

## 0.1.0 (2026-10-15)


### Features

* **httpx:** promote hardened HTTP client and optional modules ([#14](https://github.com/ashtonian/pkg/issues/14)) ([1e95674](https://github.com/ashtonian/pkg/commit/1e95674d053b6904ba1459f07e44815028da0d9f))


### Bug Fixes

* **httpx:** preserve cache cancellation and correct capability claims ([#19](https://github.com/ashtonian/pkg/issues/19)) ([fdfe4d0](https://github.com/ashtonian/pkg/commit/fdfe4d0d54cced179d6de3fe67966def8192b9e9))
