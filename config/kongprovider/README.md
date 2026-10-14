# config/kongprovider

Brings [Kong](https://github.com/alecthomas/kong) CLI flags into [`config`](https://github.com/ashtonian/pkg/blob/main/config/README.md) as its highest-precedence source, and describes the config struct in Kong's help. Only flags the user typed are forwarded — in any spelling Kong accepts — so Kong defaults never mask a config file or an environment variable; the config struct's `default` tags, `desc` tags and validation stay the single schema. A separate module, so the core package does not depend on Kong.

## Quick Start

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"
)

// AppConfig owns every default, description and validation rule; a flag tag
// ties a field to the CLI flag that overrides it.
type AppConfig struct {
	Port     int    `koanf:"port" default:"8080" validate:"min=1,max=65535" flag:"port" desc:"HTTP listen port"`
	LogLevel string `koanf:"log_level" default:"info" validate:"oneof=debug info warn error" flag:"log-level" desc:"Log level"`
	Metrics  bool   `koanf:"metrics" default:"true" flag:"metrics" desc:"Serve /metrics"`
}

// CLI declares the flags' names and shapes only.
type CLI struct {
	Config   string `type:"path" default:"config.yaml" help:"Config file."`
	Port     int    `short:"p"`
	LogLevel string
	Metrics  bool `negatable:""`

	Serve struct{} `cmd:"" default:"1" help:"Run the server."`
	Print struct{} `cmd:"" help:"Print the effective configuration, secrets redacted."`
	Usage struct{} `cmd:"" help:"List every setting with its environment variable."`
}

func main() {
	var cli CLI
	kctx := kong.Parse(&cli, kongprovider.HelpFromTags[AppConfig]("APP"))
	if kctx.Command() == "usage" {
		if err := config.PrintUsage[AppConfig]("APP"); err != nil {
			log.Fatal(err)
		}
		return
	}

	cfg, err := config.Load[AppConfig](context.Background(),
		config.WithFile(cli.Config),
		config.WithEnvPrefix("APP"),
		kongprovider.WithOverridesFromTags[AppConfig](kctx),
	)
	if err != nil {
		log.Fatal(err)
	}

	switch kctx.Command() {
	case "print":
		if err := config.PrintRedacted(cfg, config.WithWriter(os.Stdout)); err != nil {
			log.Fatal(err)
		}
	default:
		fmt.Printf("serving on :%d (log level %s, metrics %v)\n", cfg.Port, cfg.LogLevel, cfg.Metrics)
	}
}
```

```txt
$ APP_PORT=9000 app --no-metrics
serving on :9000 (log level info, metrics false)

$ app --help
...
  -p, --port=INT                HTTP listen port (config: port; env: APP_PORT;
                                default: 8080)
      --log-level=STRING        Log level (config: log_level; env:
                                APP_LOG_LEVEL; default: info)
      --[no-]metrics            Serve /metrics (config: metrics; env:
                                APP_METRICS; default: true)
```

`--port 9000`, `-p 9000`, `--port=9000` and `--no-metrics` override `config.yaml` and `APP_*`; a flag left out does not. The program is `examples/cli/main.go`; `ExampleWithOverridesFromTags` in `example_test.go` runs the same wiring with its output checked.

## How It Works

Kong records every flag it parses from argv as an element of `kong.Context.Path`, under the flag's canonical name whatever spelling was typed — long, short, alias, `--flag=value`, or negated `--no-flag` — with the value already applied. Values supplied by Kong resolvers are marked `Resolved`, defaults never appear, and tokens after `--` are positional. `New` forwards exactly the unresolved flag elements it has a mapping for. `Flag.Set` is not used: Kong sets it for applied defaults too.

Mapped values unflatten into the key tree, so a flag mapped to `listeners.tcp` overrides that nested key instead of adding a literal dotted top-level key. The provider loads through `config.WithOverrideProvider`, above the environment.

A mapping that names a flag the CLI does not have — a typo, or a flag since renamed — fails the load with `config.ErrSource` naming it, instead of silently never applying; two fields tagged with one flag name fail it too.

`HelpFromTags` is a `kong.PostBuild` option. For every flag a `flag` tag names, a flag with no help of its own takes the field's `desc`, and the help gains `(config: <key>; env: <PREFIX_KEY>; default: <default>)`, the default left out when it is, or could hold, a sensitive value (`config.Field.PrintableDefault`). Kong refuses to build the CLI when a `flag` tag names no flag or two fields share one.

## Rules for the CLI struct

- **No defaults, no `required`.** A flag the user did not type is not forwarded, so a Kong default would only mislead the help; and Kong validates before the config pipeline runs, so a CLI `required` could not be satisfied by a file, the environment or a secret. Put both on the config struct.
- **One flag name, one key.** When two subcommands give a flag different meanings (`serve --port`, `admin --port`), build the mapping for the selected command and pass it to `WithOverrides`:

```go
mapping := map[string]string{"port": "server.port"}
if kctx.Command() == "admin" {
	mapping = map[string]string{"port": "admin.port"}
}
cfg, err := config.Load[AppConfig](ctx, kongprovider.WithOverrides(kctx, mapping))
```

- **CLI-only flags stay unmapped.** A flag that is not configuration (`--config` naming the file to load) has no `flag` tag and is read from the CLI struct directly.

For a CLI tool whose config file only supplies flag defaults, Kong's own `kong.Configuration` with a YAML resolver is the simpler fit. This package is for a service whose configuration tree is larger than its CLI.

## API Reference

- `WithOverridesFromTags[T](kctx) config.Option` — typed flags as overrides, mapped by `flag` tags over `config.Fields[T]`, so flag keys match the paths defaults and environment variables use.
- `WithOverrides(kctx, mapping) config.Option` — the same with an explicit Kong flag name → koanf dot-path mapping.
- `New(kctx, mapping) koanf.Provider` — the provider itself, for `config.WithOverrideProvider(p, nil)`.
- `HelpFromTags[T](envPrefix) kong.Option` — flag help from the config struct's `desc`, key, environment variable and default.

## Key Files

| File | Responsibility |
|------|----------------|
| `kongprovider.go` | `New`, `WithOverrides`, `WithOverridesFromTags`, `HelpFromTags`, flag-tag mapping and validation |
| `examples/cli/main.go` | The Quick Start program: subcommands, help from tags, `print` and `usage` commands |
| `go.mod`, `LICENSE` | The module (requires the parent at its release version) and its Apache-2.0 license |

## Dependencies

- *Imports*: `github.com/alecthomas/kong`, `github.com/knadh/koanf/v2`, `github.com/knadh/koanf/providers/confmap`, `config`.

## References

- Parent: [`config`](https://github.com/ashtonian/pkg/blob/main/config/README.md) § Kong integration
- [Kong](https://github.com/alecthomas/kong) — the CLI library
