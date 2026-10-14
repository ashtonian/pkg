# Changelog

## [0.3.0](https://github.com/ashtonian/pkg/compare/config/v0.2.0...config/v0.3.0) (2026-10-14)


### Features

* **config:** sep tags split a list's strings on a separator of the field's ([c490f1e](https://github.com/ashtonian/pkg/commit/c490f1edcecb283ee51ecc58f3c7ed6be23dd57f))


### Bug Fixes

* **config:** keep secrets out of diagnostics, and make the tags work together ([b6bffa7](https://github.com/ashtonian/pkg/commit/b6bffa7f93e194a5b18e59935f49d76025eb3257))

## [0.2.0](https://github.com/ashtonian/pkg/compare/config/v0.1.0...config/v0.2.0) (2026-10-14)


### ⚠ BREAKING CHANGES

* **config:** WithFileParser takes the suffix it applies to: WithFileParser(p) becomes WithFileParser("", p) to keep one parser for every file. A .json file is parsed as JSON, not YAML, so a .json file holding YAML now fails; WithFileParser(".json", nil) restores YAML for it.

### Features

* **config:** alias tags keep a renamed key reading deployed files ([e87e616](https://github.com/ashtonian/pkg/commit/e87e616e80e635cbfb98963761e87cc44cdd173f))
* **config:** default tags and strings fill maps as key=value entries ([62802aa](https://github.com/ashtonian/pkg/commit/62802aa7709684f94439e862af675f74ec6a6858))
* **config:** env tags name more variables for a field ([c2ebcaf](https://github.com/ashtonian/pkg/commit/c2ebcaf9a05d7c4b3d656f8796ca4000fbcbba1b))
* **config:** example tags show a sample value where there is no default ([71bd728](https://github.com/ashtonian/pkg/commit/71bd728301fe99e7cdab6133c5f1a0f0aa6f13a7))
* **config:** parse each config file by the suffix of its name ([#2](https://github.com/ashtonian/pkg/issues/2)) ([b446990](https://github.com/ashtonian/pkg/commit/b4469906fef00ea6353e0f1de4008634bae49d12))
* **config:** report unknown keys as an UnknownKeysError, each with the field it likely misspells ([#1](https://github.com/ashtonian/pkg/issues/1)) ([100b7ff](https://github.com/ashtonian/pkg/commit/100b7ffa246ec6bf76226e1d281b9ca22942e786))
* **config:** sensitive:"false" exempts a field its name wrongly redacts ([6e19aa2](https://github.com/ashtonian/pkg/commit/6e19aa2c5b5a1f81eabee038de0a38d385ee3727))
* **config:** WithUnknownKeys decides what input no field reads does ([909fee4](https://github.com/ashtonian/pkg/commit/909fee40bb4fde6d735be1edd6471a2931ead916))


### Bug Fixes

* **config:** split comma-separated strings into slices of any element type ([a911638](https://github.com/ashtonian/pkg/commit/a9116383d18b9eb59664852ce33ce6cbb0d7c772))

## 0.1.0 (2026-10-14)

First release as `github.com/ashtonian/pkg/config`: typed loading from defaults, files, the environment and any koanf provider through one validated pipeline; hot reload that publishes only a config that passed; appliers that push changes into the running process and report what waits for a restart; `Diff`, provenance, redacted printing, config file templates and an advisory JSON Schema.
