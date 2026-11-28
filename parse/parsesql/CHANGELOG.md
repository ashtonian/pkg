# Changelog

## [0.19.0](https://github.com/ashtonian/pkg/compare/parse/parsesql/v0.18.0...parse/parsesql/v0.19.0) (2026-11-28)


### ⚠ BREAKING CHANGES

* **parse:** parse/parsesqlite is gone: its Register, RegisterOn, Functions and function names are parsesql's. parsesql and parsepgx are modules: require them beside parse, at parse's version. parseprovider's Config, SQLiteConfig and PostgresConfig are gone: hold the provider's name and each linked provider's Config (parsesql.Config, parsepgx.Config, whose dsn and settle_timeout are each its own), and call parseprovider.Open(ctx, name, parsesql.Choice(cfg.SQLite), parsepgx.Choice(cfg.Postgres)). It returns the parseprovider.Database interface; the handle is the opened provider's (*parsesql.Database).DB or (*parsepgx.Database).Pool.

### Bug Fixes

* **parse:** an in-memory provider over one executor per provider ([#467](https://github.com/ashtonian/pkg/issues/467)) ([786ddf8](https://github.com/ashtonian/pkg/commit/786ddf8d0fd4c9e3d01d39b966d32ff70fe88208))


### Code Refactoring

* **parse:** give each provider its own module, and require no driver in parse ([#465](https://github.com/ashtonian/pkg/issues/465)) ([abe3f93](https://github.com/ashtonian/pkg/commit/abe3f9363d6196c39728635deedecf3ae43ed132))
