# config

[![Go Reference](https://pkg.go.dev/badge/github.com/ashtonian/pkg/config.svg)](https://pkg.go.dev/github.com/ashtonian/pkg/config)

Typed configuration for Go services, built on [koanf](https://github.com/knadh/koanf).
Declare one tagged struct, then load defaults, files, environment variables
and providers through one validated pipeline.

- **One schema.** Struct tags define keys, defaults, validation, environment
  names, help text and redaction; a root `DefaultProvider` supplies shared path-specific defaults.
- **Layered sources.** Combine files, environment variables, CLI flags and
  any koanf provider, with explicit precedence.
- **Validated reloads.** Watch files, poll or use signals; a failed reload
  leaves the previous config live.
- **Live changes.** Appliers update running subsystems and report changes
  that still require a restart.
- **Safe inspection.** Print redacted values, diffs, provenance, starter
  files and JSON Schema from the same struct.

Requires **Go 1.27 or later**. The module is pre-1.0; minor releases may
change its API. See [design and guarantees](docs/overview.md).

## Install

```bash
go get github.com/ashtonian/pkg/config
```

## Quick start

```go
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/ashtonian/pkg/config"
)

type DBConfig struct {
	Host     string `koanf:"host" default:"localhost" desc:"Database host"`
	MaxConns int    `koanf:"max_conns" default:"25" validate:"min=1" desc:"Connection pool size"`
	Password string `koanf:"password" validate:"required" desc:"Database password"`
}

type AppConfig struct {
	Port    int           `koanf:"port" default:"8080" validate:"min=1,max=65535" desc:"HTTP listen port"`
	Timeout time.Duration `koanf:"timeout" default:"30s" desc:"Request timeout"`
	DB      DBConfig      `koanf:"db"`
}

func main() {
	cfg, err := config.Load[AppConfig](context.Background(),
		config.WithFile("config.yaml"), // optional: skipped when absent
		config.WithEnvPrefix("APP"),    // APP_PORT, APP_DB_MAX_CONNS, APP_DB_PASSWORD, ...
	)
	if err != nil {
		log.Fatal(err) // names the failing key, never a secret's value
	}
	if err := config.PrintRedacted(cfg, config.WithWriter(os.Stderr)); err != nil {
		log.Fatal(err)
	}
}
```

```txt
$ go run .
2026/10/13 09:00:00 validating config: configuration validation failed: db.password(required)

$ APP_DB_PASSWORD=s3cret APP_DB_MAX_CONNS=50 go run .
FIELD         VALUE       TYPE
------------------------------------------
port          8080        int
timeout       30s         time.Duration
db.host       localhost   string
db.max_conns  50          int
db.password   [REDACTED]  string
```

The program is [`examples/quickstart`](examples/quickstart/main.go). The package's `Example` functions also run as tests with their output checked.

## Build integrations from the config fields

`FieldsChecked[T]()` returns each setting's key, declared type, struct tags,
redaction status, resolved reload class, environment-reference flag and field index. For `Port int` tagged `koanf:"port"
default:"8080"`, the catalog contains key `port`, type `int` and the unchanged
default tag. Reading metadata does not apply or decode that default.

Use `FieldFor[T](key)` for one key, `KeyOf(&cfg, &cfg.Port)` for one actual field,
or `MatchFields(&cfg, reflectedTargets)` for a group of CLI destinations. The
last returns fields in target order; an empty Path means no match. The
[inspection guide](docs/inspection.md#fields-and-struct-tags) explains returned
values, ownership and errors.

`Usage[T]("APP")` supplies a safe catalog for documentation generators, and
`PrintUsage` renders it as a table, JSON, Markdown or environment template.
`UsageEntry.Bounds` shares JSON Schema’s constraint translation, including
conditional `omitempty` rules and duration-bound annotations in exact
nanoseconds. `ParseKey` reads the same grammar used by field
lookup, diffs and appliers.
`Tree` exports resolved values, including secrets; `TreeFunc` lets a support
bundle apply its own per-leaf policy. See [inspection](docs/inspection.md).

## Configuration

The [tag table](docs/schema.md) defines the schema, and the
[options table](docs/reference.md#options) lists every load, reload and print
option with its default. Environment loading requires `WithEnvPrefix`.
`envref:"true"` declares variable names read by the application;
`WithKnownVariables` and `WithVariableFilter` claim names read elsewhere.
`WithReloadTags` opts into inherited `reload` classes, while
`WithReloadTagChecks` also checks applier coverage before loading.
`DefaultProvider.ConfigDefaults` supplies schema-owned defaults to loading,
usage, schema and templates. `CheckKey` validates a stored entry without
reading sources; `DecodeKey` also returns the typed config. Provider-level
`WithInvalidEntries` can withhold bad stored rows while preserving lower
sources and unknown-key diagnostics. See [sources](docs/sources.md).

## Build and test

From the repository root, `make test` runs module race tests and `make lint`
runs vet and staticcheck. `make release-check` verifies standalone module
builds; `make ci` also checks the examples and cookbook. The quick start runs
with `cd config/examples/quickstart && APP_DB_PASSWORD=test go run .`.
See the [contributor workflow](../CONTRIBUTING.md).

## Operations and security

The [operations guide](docs/reference.md#observability) lists log events,
error categories and metric examples. Failed loads keep the previous
snapshot; failed appliers leave the desired config published and report what
still needs applying. `Snapshot.UnknownKeys` records only the input accepted
by that published generation. Use `Stop(ctx)` with a shutdown deadline.

Diagnostic printers, diffs and usage metadata withhold sensitive values.
Credential-like names redact text-capable values; numeric counters, booleans
and durations keep ordinary diagnostics. Explicit `sensitive:"true"` and
`secret` tags remain authoritative. `RedactText` also removes recognizable
credential shapes from text and bytes while retaining non-secret context. `Tree` and `TreeFunc` expose raw values
for export and caller-defined policies; choose the redacted functions for
logs. Errors returned by application providers and hooks pass through, so
those components own their error-message redaction.

## Dependencies

The module uses koanf for merging/providers, mapstructure for decoding,
gowalker for field discovery, go-playground/validator for validation,
fsnotify for file watching, and YAML parsers for files/output. Versions are
in [go.mod](go.mod). No external service is required; remote providers and
secret stores are application-supplied. The module has no application or
metrics-library dependency.

## Code map

| File | Responsibility |
|---|---|
| `config.go`, `options.go` | `Load` pipeline and source options |
| `schema_defaults.go`, `decode_input.go`, `check_key.go`, `source_validation.go` | Shared defaults/decoding, key checks and provider entry validation |
| `schema.go`, `view.go`, `fields.go`, `key.go` | Canonical metadata, discovery and key grammar |
| `env_refs.go`, `env.go` | Environment references and variable matching |
| `reloader.go`, `reload_tags.go`, `applier.go`, `snapshot.go` | Publication, reload policy and applied-state accounting |
| `render.go`, `print.go`, `usage.go`, `jsonschema.go`, `constraints.go`, `redact.go` | Trees, diagnostic output and documentation metadata |
| `errors.go`, `provenance.go` | Error categories and value origins |

## Documentation

| Task | Guide |
|---|---|
| Define fields, defaults, validation and key migrations | [Schema and validation](docs/schema.md) |
| Combine files, variables and remote providers | [Sources and precedence](docs/sources.md) |
| Resolve credentials from a secret store | [Secrets](docs/secrets.md) |
| Watch config and apply changes to a running service | [Hot reload](docs/reloading.md) |
| Print, explain, diff or generate configuration | [Inspection and output](docs/inspection.md) |
| Add CLI flags or keep an existing koanf loader | [Integrations](docs/integrations.md), [Kong provider](kongprovider/README.md) |
| Find options, errors, metrics and test patterns | [Reference and operations](docs/reference.md) |
| Run a complete service or provider recipe | [Cookbook](examples/cookbook/README.md) |

See the [documentation index](docs/README.md),
[Go API reference](https://pkg.go.dev/github.com/ashtonian/pkg/config),
and [contributor workflow](../CONTRIBUTING.md).

## License

[MIT](../LICENSE).
