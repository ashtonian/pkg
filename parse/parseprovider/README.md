# parse/parseprovider

## Overview

Opens the one parse provider an application's configuration names, among the providers its binary links, once, at startup. Each provider is a module of its own that owns its settings and how it opens: SQLite in [`parsesql`](../parsesql/README.md) (`parsesql.Config`, `parsesql.Open`) and PostgreSQL in [`parsepgx`](../parsepgx/README.md) (`parsepgx.Config`, `parsepgx.Open`). `main` hands `Open` a `Choice` for each provider it links, built from that provider's settings (`parsesql.Choice`, `parsepgx.Choice`), and the configured name; `Open` opens that one and returns it as a `Database`. This package imports no provider and no driver, so a binary carries only the drivers of the providers it passes: one that runs on SQLite alone requires `parsesql` and never links pgx.

The providers are alternatives with one API, for different runtime requirements: nothing here moves data between them, migrates from one to the other, or runs one beside another. An application that runs on one provider only needs no selector: it calls that provider's `Open`.

```go
type Settings struct {
	DB struct {
		Provider string          `koanf:"provider" default:"sqlite" validate:"oneof=sqlite postgres"`
		SQLite   parsesql.Config `koanf:"sqlite"`
		Postgres parsepgx.Config `koanf:"postgres"`
	} `koanf:"db"`
	// …
}

settings, err := config.Load[Settings](ctx, config.WithEnvPrefix("APP"))
db, err := parseprovider.Open(ctx, settings.DB.Provider,
	parsesql.Choice(settings.DB.SQLite), parsepgx.Choice(settings.DB.Postgres))
defer db.Close() // after the HTTP server has drained
devices, err := parse.NewResource[Device](db.Provider(), "devices", parse.TenantColumn("tenant_id"))
page, err := devices.List(ctx, req, scope)
```

With no setting given the defaults open an in-memory SQLite database; `APP_DB_PROVIDER=postgres APP_DB_POSTGRES_DSN=postgres://…` runs the same binary on PostgreSQL. `parsepgx.Config`'s DSN has no default and no `validate` tag, so the settings load on SQLite with it unset; `parsepgx.Open` refuses it empty.

## Configuration

This package has no settings of its own. The application holds the provider's name and each linked provider's `Config` where it chooses (`db.` above):

| Key | Type | Default | Description |
|---|---|---|---|
| `provider` (the application's) | string | the application's | The provider to open: a `Choice`'s name, `parsesql.Name` (`sqlite`) or `parsepgx.Name` (`postgres`). `Open` refuses one no choice has, listing those the binary links. |
| `sqlite.*` | `parsesql.Config` | in-memory SQLite | [parsesql's settings](../parsesql/README.md#configuration): `dsn`, `max_open_conns`, `busy_timeout`, `settle_timeout`. |
| `postgres.*` | `parsepgx.Config` | no DSN | [parsepgx's settings](../parsepgx/README.md#configuration): `dsn`, `settle_timeout`, `collation`, `containment_max_values`, the pool's limits and `connect_timeout`. |

Only the named provider's settings are read: the others' `Open` never runs, so an unset PostgreSQL DSN beside a SQLite configuration is not an error. Each provider checks its own settings in `Open`, naming the key it refuses (`parsesql: max_open_conns: 2: …`).

## External Dependencies

None of its own: the root module (`github.com/ashtonian/pkg/parse`), which requires no database driver. The drivers come with the provider modules the application requires: `modernc.org/sqlite` through `parsesql`, `github.com/jackc/pgx/v5` through `parsepgx`.

## API

- `Choice{Name, Open}`: a provider the binary links, by its name in configuration (`parse.Provider.Name`), and how it opens from the settings it was built with. `parsesql.Choice(cfg)` and `parsepgx.Choice(cfg)` build them; a `Choice` of another provider module, or one the application wraps (to seed a database it opens, say), is any `Open` that returns a `Database`.
- `Open(ctx, name, choices...) (Database, error)`: opens the choice named `name` under `ctx` and returns what its `Open` returned, or its error as it is. It refuses, before opening anything, no choices, a choice with no name or no `Open`, a name given twice, and a name no choice has.
- `Database`: what every provider's opened database is: `Provider() *parse.Provider`, to build every resource on (`parse.NewResource`); `Ping(ctx) error`, for a readiness probe; `Close() error`, once the work on it is done. Each provider's own type (`*parsesql.Database`, `*parsepgx.Database`) also hands out its handle for the application's own SQL (`DB() *sql.DB`, `Pool() *pgxpool.Pool`): its schema, its own statements, a transaction it shares with parse through `parsesql.Tx` or `parsepgx.Tx` and `Resource.In`. Code that needs the handle asserts the type the configuration chose (the [example server](../examples/server/main.go)'s `seed`).

## Build and test

From the parse module directory:

```sh
go test -tags unit ./parseprovider/
```

The tests run the selector over compile-only providers (`internal/providertest`): the named choice opens, its error returns as it is, and every refusal. Each provider's `Open` and `Choice` are tested in its own module (`parsesql`'s and `parsepgx`'s `open_test.go`, and `parsepgx`'s `open_integration_test.go` on a PostgreSQL server).

## Operations

- *Health*: the opened `Database`'s `Ping` for readiness. The selector does nothing after `Open` returns.
- *Metrics and logs*: none. Log the opened provider's name (`db.Provider().Name()`) at startup.
- *Shutdown*: close the `Database` after the HTTP server has drained, so no request is still using a connection; a PostgreSQL pool's `Close` blocks until every connection is released.
- *Failure modes*:
  - `parseprovider: provider: "mysql": this binary links postgres, sqlite`: the configured name is no linked provider's. Check the setting, or require and pass the provider's module. CockroachDB and MySQL are not providers.
  - `parseprovider: no provider to choose from`, `parseprovider: choice 0: no name`, `parseprovider: choice "sqlite": no Open`, `parseprovider: choice "sqlite": given twice`: `main` built its choices wrong; the configuration is not at fault.
  - Any other error is the chosen provider's `Open`, returned as it is: `parsesql: …` ([parsesql operations](../parsesql/README.md#operations)) or `parsepgx: …` ([parsepgx operations](../parsepgx/README.md#operations)).

## Security

The selector reads no credentials and quotes no setting: the name it refuses is the configured provider name, never a DSN. Each provider's DSN is `sensitive` in its `Config`, redacted from config printouts, and never quoted in its errors.

## Code Map

| File | Responsibility |
|------|----------------|
| [`parseprovider.go`](parseprovider.go) | `Database`, `Choice`, `Open` and its refusals |
| [`parseprovider_test.go`](parseprovider_test.go) | The named choice opens, its error returns as it is, every refusal |
