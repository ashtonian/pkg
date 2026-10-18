# Changelog

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
