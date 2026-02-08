# config

Type-safe configuration loading using [koanf](https://github.com/knadh/koanf) with support for multiple sources (YAML files, environment variables, CLI flags), struct tag defaults, validation, sensitive field redaction, and secret injection.

## Installation

```bash
go get github.com/ashtonian/pkg/config
```

## Quick Start

```go
package main

import (
    "fmt"
    "time"
    "github.com/ashtonian/pkg/config"
)

type AppConfig struct {
    Host    string        `koanf:"host" default:"localhost" desc:"Server hostname"`
    Port    int           `koanf:"port" default:"8080" validate:"min=1,max=65535"`
    Debug   bool          `koanf:"debug" default:"false"`
    Timeout time.Duration `koanf:"timeout" default:"30s"`
}

func main() {
    cfg, err := config.GetConfig[AppConfig](
        config.WithEnvPrefix("APP"),
        config.WithConfigFile("./config.yaml"),
    )
    if err != nil {
        panic(err)
    }
    fmt.Printf("%s:%d\n", cfg.Host, cfg.Port)
}
```

## API Overview

### Core Functions

| Function | Description |
|----------|-------------|
| `GetConfig[T](opts...)` | Load config from all sources, unmarshal into T, inject secrets, validate |
| `DefaultsFromTags[T]()` | Extract `default:"value"` struct tags into a koanf-compatible map |
| `PrintRedacted(cfg, opts...)` | Print config with sensitive fields redacted |
| `ToMap(cfg, redact)` | Export config as `map[string]any` with optional redaction |

### Options

| Option | Description |
|--------|-------------|
| `WithEnvPrefix(prefix)` | Set prefix for env var lookups (e.g., `"APP"` maps `APP_HOST` to koanf key `host`) |
| `WithConfigFile(path)` | Add an optional YAML config file |
| `WithMustConfigFile(path)` | Add a required YAML config file (error if missing) |
| `WithConfigFiles(paths...)` | Add multiple optional config files, merged left-to-right |
| `WithMustConfigFiles(paths...)` | Add multiple required config files |
| `WithDefaults(map)` | Set programmatic default values (koanf dot-path keys) |
| `WithProvider(p)` | Add a generic koanf provider at high precedence |
| `WithSecretProvider(sp, ctx)` | Configure secret injection via `pkg/secrets` |
| `WithSkipValidation()` | Disable go-playground/validator validation |
| `WithValidator(v)` | Use a custom validator instance |
| `WithKoanf(k)` | Provide a pre-configured `*koanf.Koanf` instance |

### Struct Tags

| Tag | Description | Example |
|-----|-------------|---------|
| `koanf` | koanf key name (nesting via `.` delimiter) | `koanf:"host"` |
| `default` | Default value (read by `DefaultsFromTags`) | `default:"localhost"` |
| `validate` | Validation rules (go-playground/validator) | `validate:"required,min=1"` |
| `flag` | Maps to Kong CLI flag (used by `kongprovider`) | `flag:"port"` |
| `sensitive` | Redacted in PrintRedacted/ToMap output | `sensitive:"true"` |
| `desc` | Human-readable description | `desc:"Server hostname"` |
| `secret` | Key for secret provider lookup via `pkg/secrets` | `secret:"db/password"` |

### Source Precedence (highest wins)

1. Extra providers (CLI flags via `kongprovider`, custom providers)
2. Environment variables
3. Config files (merged left-to-right)
4. Programmatic defaults (`WithDefaults`)
5. Struct tag defaults (`default:"value"`)

### CLI Flag Overrides (kongprovider)

```go
import "github.com/ashtonian/pkg/config/kongprovider"

cfg, err := config.GetConfig[AppConfig](
    config.WithEnvPrefix("APP"),
    kongprovider.WithOverridesFromTags[AppConfig](kongCtx),
)
```

### Error Codes

| Code | Sentinel | Description |
|------|----------|-------------|
| E7002 | `ErrValidation` | Validation rule failed (includes `validate:"required"` checks) |
| E7003 | `ErrFileRead` | Required config file could not be read |
| E7005 | `ErrSecretProvider` | Secret provider returned an error |

### Print Options

| Option | Description |
|--------|-------------|
| `WithFormat(format)` | Output format: `"table"`, `"json"`, or `"env"` |
| `WithShowSensitive(bool)` | Include sensitive values instead of redacting |
| `WithWriter(w)` | Set output destination |
| `WithSortByName()` | Sort fields alphabetically |

