# Changelog

## [0.1.2](https://github.com/ashtonian/pkg/compare/config/kongprovider/v0.1.1...config/kongprovider/v0.1.2) (2026-10-14)


### Bug Fixes

* **kongprovider:** HelpFromConfig leaves env tags to the config package ([2b7acdf](https://github.com/ashtonian/pkg/commit/2b7acdf011817fbe51616777f3ca132d61ae50b3))
* **kongprovider:** require config v0.3.0, which reads the env and sep tags ([35bae53](https://github.com/ashtonian/pkg/commit/35bae5314e194ef759fe0e10ea81ede9966b8828))

## [0.1.1](https://github.com/ashtonian/pkg/compare/config/kongprovider/v0.1.0...config/kongprovider/v0.1.1) (2026-10-14)


### Bug Fixes

* **kongprovider:** name no config type in the no-flag error, and document where Kong's tags differ ([87b0e9b](https://github.com/ashtonian/pkg/commit/87b0e9b4439b9e68051072e600fda14d752a5e58))

## 0.1.0 (2026-10-14)

First release as `github.com/ashtonian/pkg/config/kongprovider`: the Kong flags a user typed as `config`'s highest-precedence source, mapped to keys by the config field each flag writes, with flag help from the config struct.
