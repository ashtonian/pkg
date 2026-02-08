# config

Type-safe configuration loading via struct tags with support for multiple sources (YAML files, environment variables), validation, sensitive field handling, and secret providers.

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
    Host    string        `config:"HOST,localhost" desc:"Server hostname"`
    Port    int           `config:"PORT,8080" validate:"min=1,max=65535"`
    Debug   bool          `config:"DEBUG,false"`
    Timeout time.Duration `config:"TIMEOUT,30s"`
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
| `GetConfig[T](opts...)` | Shorthand: create loader, load, and return config in one call |
| `NewLoader[T](opts...)` | Create a new config loader with given options |
| `Loader.Load()` | Resolve configuration from all sources and return populated config |
| `Loader.Print(opts...)` | Print loaded config (sensitive fields redacted by default) |
| `Loader.ToMap(redact)` | Export config as a map with optional redaction |
| `Loader.Fields()` | Return metadata about all config fields |

### Options

| Option | Description |
|--------|-------------|
| `WithEnvPrefix(prefix)` | Set prefix for env var lookups (e.g., `"APP"` maps `Host` to `APP_HOST`) |
| `WithConfigFile(path)` | Add an optional YAML config file |
| `WithMustConfigFile(path)` | Add a required YAML config file (error if missing) |
| `WithConfigFiles(paths...)` | Add multiple optional config files, merged left-to-right |
| `WithMustConfigFiles(paths...)` | Add multiple required config files |
| `WithDefaults(map)` | Set programmatic default values |
| `WithSecretProvider(sp, ctx)` | Configure a secret provider for `secret:` tagged fields |
| `WithSkipValidation()` | Disable go-playground/validator validation |
| `WithValidator(v)` | Use a custom validator instance |

### Struct Tags

| Tag | Description | Example |
|-----|-------------|---------|
| `config` | Env var name and optional default | `config:"DB_HOST,localhost"` |
| `required` | Field must be set (non-zero after loading) | `required:"true"` |
| `sensitive` | Redacted in Print/ToMap output | `sensitive:"true"` |
| `validate` | go-playground/validator rules | `validate:"min=1,max=100"` |
| `desc` | Human-readable description | `desc:"Database hostname"` |
| `secret` | Key for secret provider lookup | `secret:"db/password"` |

### Source Precedence (highest wins)

1. Environment variables
2. Config files (merged left-to-right)
3. Programmatic defaults (`WithDefaults`)
4. Struct tag defaults

### Secret Providers

| Provider | Description |
|----------|-------------|
| `EnvSecretProvider` | Looks up secrets as prefixed env vars |
| `MapSecretProvider` | In-memory map (useful for testing) |
| `SecretProvider` | Interface for custom implementations |

### Error Codes

| Code | Sentinel | Description |
|------|----------|-------------|
| E7001 | `ErrRequired` | Required field not set |
| E7002 | `ErrValidation` | Validation rule failed |
| E7003 | `ErrFileRead` | Config file could not be read |
| E7004 | `ErrFileParse` | Config file could not be parsed |
| E7005 | `ErrSecretProvider` | Secret provider returned an error |
| E7006 | `ErrUnsupportedType` | Unsupported field type for conversion |
| E7007 | `ErrDuplicateKey` | Duplicate config key from multiple sources |
| E7008 | `ErrStrictUnknown` | Unknown key in strict mode |
| E7009 | `ErrSliceParse` | Slice element could not be parsed |
| E7010 | `ErrMapParse` | Map entry could not be parsed |

### Print Options

| Option | Description |
|--------|-------------|
| `WithFormat(format)` | Output format: `"table"`, `"json"`, or `"env"` |
| `WithShowSensitive(bool)` | Include sensitive values instead of redacting |
| `WithWriter(w)` | Set output destination |
| `WithSortByName()` | Sort fields alphabetically |

