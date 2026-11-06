# Changelog

## [0.4.0](https://github.com/ashtonian/pkg/compare/httpx/v0.3.0...httpx/v0.4.0) (2026-11-06)

### Features

- `WithDeferredAcquisition` lets a token source wait for a 401 before acquiring its first token, preserving eager acquisition by default.
- `TokenBinding.InitialAuth` supports origin-bound authentication while the token is empty. Bearer replay omits the initial provider's credentials. See [#119](https://github.com/ashtonian/pkg/pull/119); published consumer adoption remains tracked in [#58](https://github.com/ashtonian/pkg/issues/58).

## [0.3.0](https://github.com/ashtonian/pkg/compare/httpx/v0.2.0...httpx/v0.3.0) (2026-11-06)

### Breaking changes

* `CacheStore` methods accept context and return errors. `BaseOption` is opaque; use `WithTransportSettings` for supported transport customization.

### Features

* Add dial-time egress policy with explicit trusted-prefix exceptions and proxy-bypass protection.
* Add context-aware token acquisition, shared cancellable refresh and one origin-bound replay of a replayable request after 401. The optional OAuth2 module is released separately.
* Preserve cancellation, invalidation ordering and acknowledged HTTP delivery with persistent cache errors and bounded failure observations.
* Decode bounded RFC 9457 problem details and raw extensions, with opt-in typed request errors.
* Apply a fail-closed URL logging policy to private paths, URL-valued headers and wrapped errors without changing live requests or caller errors ([#114](https://github.com/ashtonian/pkg/pull/114)).

## [0.2.0](https://github.com/ashtonian/pkg/compare/httpx/v0.1.0...httpx/v0.2.0) (2026-10-24)


### Features

* **httpx:** expose bounded response cleanup observations ([a044233](https://github.com/ashtonian/pkg/commit/a04423332e0519a27e33c3269dc17a398a3f2b94)), closes [#69](https://github.com/ashtonian/pkg/issues/69)

## 0.1.0 (2026-10-15)


### Features

* **httpx:** promote hardened HTTP client and optional modules ([#14](https://github.com/ashtonian/pkg/issues/14)) ([1e95674](https://github.com/ashtonian/pkg/commit/1e95674d053b6904ba1459f07e44815028da0d9f))


### Bug Fixes

* **httpx:** center standard-client transport composition ([#17](https://github.com/ashtonian/pkg/issues/17)) ([931941d](https://github.com/ashtonian/pkg/commit/931941d8f59592e0b011a8d931401102cb141b6d))
* **httpx:** preserve cache cancellation and correct capability claims ([#19](https://github.com/ashtonian/pkg/issues/19)) ([fdfe4d0](https://github.com/ashtonian/pkg/commit/fdfe4d0d54cced179d6de3fe67966def8192b9e9))
