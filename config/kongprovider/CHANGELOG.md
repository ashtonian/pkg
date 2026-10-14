# Changelog

## [0.1.1](https://github.com/ashtonian/pkg/compare/config/kongprovider/v0.1.0...config/kongprovider/v0.1.1) (2026-10-14)


### Bug Fixes

* **kongprovider:** name no config type in the no-flag error, and document where Kong's tags differ ([87b0e9b](https://github.com/ashtonian/pkg/commit/87b0e9b4439b9e68051072e600fda14d752a5e58))

## 0.1.0 (2026-10-14)

First release as `github.com/ashtonian/pkg/config/kongprovider`: the Kong flags a user typed as `config`'s highest-precedence source, mapped to keys by the config field each flag writes, with flag help from the config struct.
