# Changelog

## 0.1.0 (2026-11-28)


### ⚠ BREAKING CHANGES

* **parse:** ItemPath refuses a pointer to a TextMarshaler (such as *time.Time) that it accepted before; pass the value. A patch value encoding/json refuses is ErrOperand where the Operation is built.
* **parse/client:** parse/client is the module github.com/ashtonian/pkg/parse/client and the parse module no longer contains it. Importers add the module (go get github.com/ashtonian/pkg/parse/client, or go mod tidy) and require parse at this release or later if they also require parse; an earlier parse still holds the package, and the import is then ambiguous.

### Features

* **parse/client:** its own standard-library module, with the decode side of the wire ([707e3f2](https://github.com/ashtonian/pkg/commit/707e3f242ef0c7342920d405746d4d825c2ee7a1)), refs [#377](https://github.com/ashtonian/pkg/issues/377)


### Bug Fixes

* **parse:** wave 6, the review's bugs: update transactions and locks, request text and keys, policies, patches, filters and the clients ([#458](https://github.com/ashtonian/pkg/issues/458)) ([fb9cf6d](https://github.com/ashtonian/pkg/commit/fb9cf6da1d35a0d9bb104ddef6ce49f0233c4cef)), refs [#396](https://github.com/ashtonian/pkg/issues/396)
