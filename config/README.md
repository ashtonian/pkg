# config

[![Go Reference](https://pkg.go.dev/badge/github.com/ashtonian/pkg/config.svg)](https://pkg.go.dev/github.com/ashtonian/pkg/config)

Typed configuration for Go services, built on [koanf](https://github.com/knadh/koanf).
Declare one tagged struct, then load defaults, files, environment variables
and providers through one validated pipeline.

- **One schema.** Struct tags define keys, defaults, validation, environment
  names, help text and redaction.
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
