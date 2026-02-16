# config/kongprovider

Bridges [Kong CLI](https://github.com/alecthomas/kong) flags to the `pkg/config` koanf-based configuration system. Only flags explicitly provided on the command line are forwarded -- Kong default values are NOT included, avoiding accidental overrides of config file or environment variable values.

## Usage

```go
import (
    "github.com/alecthomas/kong"

    "github.com/ashtonian/pkg/config"
    "github.com/ashtonian/pkg/config/kongprovider"
)

// Option A: derive flag-to-koanf mapping from struct tags.
type AppConfig struct {
    Host string `koanf:"host" flag:"host"`
    Port int    `koanf:"port" flag:"port"`
    DB   struct {
        Host string `koanf:"host" flag:"db-host"`
    } `koanf:"db"`
}

cfg, err := config.GetConfig[AppConfig](
    config.WithEnvPrefix("APP"),
    kongprovider.WithOverridesFromTags[AppConfig](kongCtx),
)

// Option B: explicit flag-to-koanf-key mapping.
cfg, err := config.GetConfig[AppConfig](
    config.WithEnvPrefix("APP"),
    kongprovider.WithOverrides(kongCtx, map[string]string{
        "host":    "host",
        "port":    "port",
        "db-host": "db.host",
    }),
)
```

## How It Works

The provider scans `ctx.Args` for `--flag` and `-f` patterns to detect which flags the user actually typed on the command line. This is necessary because Kong sets `Flag.Set=true` even for applied defaults. Only explicitly-set flags are forwarded to koanf, preserving the config file and environment variable layering.

## API Reference

- `New(ctx, mapping)` -- creates a `koanf.Provider` from explicitly-set Kong flags with manual mapping
- `WithOverrides(ctx, mapping)` -- returns a `config.Option` with explicit flag-to-koanf-key mapping
- `WithOverridesFromTags[T](ctx)` -- returns a `config.Option` that derives the mapping from `flag:""` struct tags on `T`
