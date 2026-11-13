# Changelog

## [0.5.0](https://github.com/ashtonian/pkg/compare/config/kongprovider/v0.4.0...config/kongprovider/v0.5.0) (2026-11-13)


### Features

* **kongprovider:** show code defaults in flag help ([2fc322b](https://github.com/ashtonian/pkg/commit/2fc322beea3a35a9a3b41b96820f4e44101d12a5))
* **kongprovider:** show code defaults in flag help ([#183](https://github.com/ashtonian/pkg/issues/183)) ([73d03b1](https://github.com/ashtonian/pkg/commit/73d03b16c45e3f0fae66b472d61b8a4ee6eefaca))

## [0.4.0](https://github.com/ashtonian/pkg/compare/config/kongprovider/v0.3.0...config/kongprovider/v0.4.0) (2026-11-13)


### Features

* **kongprovider:** load the config before Kong validates ([#179](https://github.com/ashtonian/pkg/issues/179)) ([3fa1558](https://github.com/ashtonian/pkg/commit/3fa1558fc0f8e4d58ac7abf0bec0d35d495afb28)), closes [#176](https://github.com/ashtonian/pkg/issues/176)

## [0.3.0](https://github.com/ashtonian/pkg/compare/config/kongprovider/v0.2.1...config/kongprovider/v0.3.0) (2026-11-11)


### ⚠ BREAKING CHANGES

* **kongprovider:** name every prefix's variables in flag help ([#174](https://github.com/ashtonian/pkg/issues/174)): HelpFromConfig takes variadic prefixes. Calls with one prefix compile unchanged; function values of the former type do not.

### Features

* **kongprovider:** name every prefix's variables in flag help ([#174](https://github.com/ashtonian/pkg/issues/174)) ([cb59c85](https://github.com/ashtonian/pkg/commit/cb59c855df89b2ecf9a5456cc6b5bbd66d2b804d))

## [0.2.1](https://github.com/ashtonian/pkg/compare/config/kongprovider/v0.2.0...config/kongprovider/v0.2.1) (2026-10-18)


### Bug Fixes

* **kongprovider:** match flags once and reject ambiguous destinations ([ac8d760](https://github.com/ashtonian/pkg/commit/ac8d760075384f6c966ab4c52c07adf819ab6e2a))

## [0.2.0](https://github.com/ashtonian/pkg/compare/config/kongprovider/v0.1.2...config/kongprovider/v0.2.0) (2026-10-18)


### ⚠ BREAKING CHANGES

* **kongprovider:** kongprovider now requires Go 1.27 or later.

### Bug Fixes

* **kongprovider:** validate config schemas before mapping flags ([71c2b55](https://github.com/ashtonian/pkg/commit/71c2b55652f6ffec7c76076699c9b8a3f059a6c7))

## [0.1.2](https://github.com/ashtonian/pkg/compare/config/kongprovider/v0.1.1...config/kongprovider/v0.1.2) (2026-10-14)


### Bug Fixes

* **kongprovider:** HelpFromConfig leaves env tags to the config package ([2b7acdf](https://github.com/ashtonian/pkg/commit/2b7acdf011817fbe51616777f3ca132d61ae50b3))
* **kongprovider:** require config v0.3.0, which reads the env and sep tags ([35bae53](https://github.com/ashtonian/pkg/commit/35bae5314e194ef759fe0e10ea81ede9966b8828))

## [0.1.1](https://github.com/ashtonian/pkg/compare/config/kongprovider/v0.1.0...config/kongprovider/v0.1.1) (2026-10-14)


### Bug Fixes

* **kongprovider:** name no config type in the no-flag error, and document where Kong's tags differ ([87b0e9b](https://github.com/ashtonian/pkg/commit/87b0e9b4439b9e68051072e600fda14d752a5e58))

## 0.1.0 (2026-10-14)

First release as `github.com/ashtonian/pkg/config/kongprovider`: the Kong flags a user typed as `config`'s highest-precedence source, mapped to keys by the config field each flag writes, with flag help from the config struct.
