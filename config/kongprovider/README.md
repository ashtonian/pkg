# config/kongprovider

Bridges [Kong](https://github.com/alecthomas/kong) CLI flags into `config` as its highest-precedence source. Only flags the user typed are forwarded — in any spelling Kong accepts — so Kong defaults never override config files or environment variables. A separate sub-package so the root package does not depend on Kong.

## Quick Start

```go
package main

import (
	"context"
	"log"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"
)

type CLI struct {
	Port    int  `kong:"name='port',short='p',default='8080'"`
	Metrics bool `kong:"name='metrics',negatable,default='true'"`
}

type AppConfig struct {
	Port    int  `koanf:"port" default:"8080" flag:"port"`
	Metrics bool `koanf:"metrics" default:"true" flag:"metrics"`
}

func main() {
	var cli CLI
	kongCtx := kong.Parse(&cli)

	// --port 9000, -p 9000, --port=9000 and --no-metrics all override
	// config.yaml and APP_*; flags left unset do not.
	cfg, err := config.GetConfig[AppConfig](context.Background(),
		config.WithConfigFile("config.yaml"),
		config.WithEnvPrefix("APP"),
		kongprovider.WithOverridesFromTags[AppConfig](kongCtx),
	)
	if err != nil {
		log.Fatal(err)
	}
	_ = cfg
}
```

## How It Works

Kong records every flag it parses from argv as an element of `kong.Context.Path`, under the flag's canonical name whatever spelling was typed — long, short, alias, `--flag=value`, or negated `--no-flag` — with the value already applied. Values supplied by Kong resolvers are marked `Resolved`, defaults never appear, and tokens after `--` are positional. `New` forwards exactly the unresolved flag elements it has a mapping for. `Flag.Set` is not used: Kong sets it for applied defaults too.

Mapped values unflatten into the nested key tree, so a flag mapped to `listeners.tcp` overrides that nested key instead of adding a literal dotted top-level key.

## API Reference

- `New(ctx, mapping) koanf.Provider` — provider over the typed flags; `mapping` is Kong flag name → koanf dot-path.
- `WithOverrides(ctx, mapping) config.Option` — `New` wrapped as `config.WithProvider`.
- `WithOverridesFromTags[T](ctx) config.Option` — mapping built from `flag` tags over `config.Fields[T]`, so flag keys match the paths defaults and env use.

## Key Files

| File | Responsibility |
|------|----------------|
| `kongprovider.go` | `New`, `WithOverrides`, `WithOverridesFromTags`, flag-tag mapping |

## Dependencies

- *Imports*: `github.com/alecthomas/kong`, `github.com/knadh/koanf/v2`, `github.com/knadh/koanf/providers/confmap`, `config`.

## References

- Parent: [`config`](../README.md)
