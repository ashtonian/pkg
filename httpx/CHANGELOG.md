# Changelog

## [0.3.0](https://github.com/ashtonian/pkg/compare/httpx/v0.2.0...httpx/v0.3.0) (2026-11-06)


### ⚠ BREAKING CHANGES

* **httpx:** the default MessagePack codec now follows JSON field and value semantics. Select NativeCodec explicitly for native MessagePack peers.
* **httpx:** CacheStore methods accept context and return errors; BaseOption is opaque and transport customization uses WithTransportSettings.
* **httpx:** CacheStore methods now accept context and return errors. BaseOption is opaque; use WithTransportSettings for custom transport fields.

### Features

* **httpx:** add egress policy and context-aware auth and cache ([3363556](https://github.com/ashtonian/pkg/commit/3363556519accc5e36433dba7d9392e61e56bad9))
* **httpx:** add egress policy and context-aware auth/cache ([b23bc0f](https://github.com/ashtonian/pkg/commit/b23bc0f2a01a2528ea338e624f18eec16fee381d))
* **httpx:** add JSON representations and URL privacy policies ([939f5a9](https://github.com/ashtonian/pkg/commit/939f5a961cd6ac8e4972a60a35ef933bcb2d1cb9))
* **httpx:** preserve JSON representations, problem details and private URLs ([caf019f](https://github.com/ashtonian/pkg/commit/caf019fbc76990e0b4ff7136ca8034e6393c7ef9))

## [0.2.0](https://github.com/ashtonian/pkg/compare/httpx/v0.1.0...httpx/v0.2.0) (2026-10-24)


### Features

* **httpx:** expose bounded response cleanup observations ([a044233](https://github.com/ashtonian/pkg/commit/a04423332e0519a27e33c3269dc17a398a3f2b94)), closes [#69](https://github.com/ashtonian/pkg/issues/69)

## 0.1.0 (2026-10-15)


### Features

* **httpx:** promote hardened HTTP client and optional modules ([#14](https://github.com/ashtonian/pkg/issues/14)) ([1e95674](https://github.com/ashtonian/pkg/commit/1e95674d053b6904ba1459f07e44815028da0d9f))


### Bug Fixes

* **httpx:** center standard-client transport composition ([#17](https://github.com/ashtonian/pkg/issues/17)) ([931941d](https://github.com/ashtonian/pkg/commit/931941d8f59592e0b011a8d931401102cb141b6d))
* **httpx:** preserve cache cancellation and correct capability claims ([#19](https://github.com/ashtonian/pkg/issues/19)) ([fdfe4d0](https://github.com/ashtonian/pkg/commit/fdfe4d0d54cced179d6de3fe67966def8192b9e9))
