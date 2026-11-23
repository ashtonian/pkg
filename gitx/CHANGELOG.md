# Changelog

## [0.1.2](https://github.com/ashtonian/pkg/compare/gitx/v0.1.1...gitx/v0.1.2) (2026-11-17)


### Bug Fixes

* **gitx:** leave the conflict over a file both sides add ([#189](https://github.com/ashtonian/pkg/issues/189)) ([9e5caa0](https://github.com/ashtonian/pkg/commit/9e5caa024e7a7b77005c8d9eecc6b101babad5d3)): mergeinserts joined the two versions of such a file into one, with no conflict marker; a merge now stops on it as git does. Check markdown files added on both sides since v0.1.0 for a second copy of their text.

## [0.1.1](https://github.com/ashtonian/pkg/compare/gitx/v0.1.0...gitx/v0.1.1) (2026-11-03)


### Bug Fixes

* **gitx:** declare go 1.26.0, the oldest supported Go ([a9ba846](https://github.com/ashtonian/pkg/commit/a9ba84693063af32aec033b52a2d52f58a9bda9a))

## 0.1.0 (2026-11-03)


### Features

* **gitx:** tracked-file listing, a conflict-marker check and a keep-inserts merge driver ([3fbfb09](https://github.com/ashtonian/pkg/commit/3fbfb0956093d7a24821fc599c299c44a937a2b1))
