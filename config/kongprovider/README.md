# config/kongprovider

Brings [Kong](https://github.com/alecthomas/kong) CLI flags into [`config`](https://github.com/ashtonian/pkg/blob/main/config/README.md) as its highest-precedence source, and describes the config struct in Kong's help. Embed the config struct in the CLI struct and Kong makes a flag of every field, while the config struct's keys, `default`, `desc` and `validate` tags stay the one schema; only flags the user typed are forwarded, in any spelling Kong accepts, so a flag left out never masks a config file or an environment variable. A separate module, so the core package does not depend on Kong.

## Quick Start

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"
)

// DBConfig becomes the --db-* flags through the prefix on its field below.
type DBConfig struct {
	Host     string `koanf:"host" default:"localhost" desc:"Database host"`
	MaxConns int    `koanf:"max_conns" default:"25" validate:"min=1" desc:"Connection pool size"`
	Password string `koanf:"password" validate:"required" kong:"-"` // file or APP_DB_PASSWORD only
}

// AppConfig is the whole schema: keys, defaults, descriptions, validation and
// the flags Kong makes of it.
type AppConfig struct {
	Port     int               `koanf:"port" default:"8080" validate:"min=1,max=65535" short:"p" desc:"HTTP listen port"`
	LogLevel string            `koanf:"log_level" default:"info" validate:"oneof=debug info warn error" desc:"Log level"`
	Timeout  time.Duration     `koanf:"timeout" default:"30s" desc:"Request timeout"`
	Metrics  bool              `koanf:"metrics" default:"true" negatable:"" desc:"Serve /metrics"`
	DB       DBConfig          `koanf:"db" embed:"" prefix:"db-"`
	Limits   map[string]int    `koanf:"limits" kong:"-"` // per-route limits, from the file
	Labels   map[string]string `koanf:"labels" kong:"-"`
}

// CLI embeds the config and adds what is the CLI's own: the config file's
// path and the commands.
type CLI struct {
	Config    string `type:"path" default:"config.yaml" help:"Config file."`
	AppConfig `embed:""`

	Serve    struct{} `cmd:"" default:"1" help:"Run the server."`
	Print    struct{} `cmd:"" help:"Print the effective configuration as YAML, secrets redacted."`
	Usage    struct{} `cmd:"" help:"List every setting with its environment variable."`
	Template struct{} `cmd:"" help:"Write a starter config file: every key with its default."`
}

func main() {
	var cli CLI
	kctx := kong.Parse(&cli, kongprovider.HelpFromConfig[AppConfig]("APP"))
	switch kctx.Command() {
	case "usage":
		if err := config.PrintUsage[AppConfig]("APP"); err != nil {
			log.Fatal(err)
		}
		return
	case "template":
		if err := config.PrintTemplate[AppConfig]("APP"); err != nil {
			log.Fatal(err)
		}
		return
	}

	cfg, err := config.Load[AppConfig](context.Background(),
		config.WithFile(cli.Config),
		config.WithEnvPrefix("APP"),
		kongprovider.WithOverridesFromStruct[AppConfig](kctx),
	)
	if err != nil {
		log.Fatal(err)
	}

	switch kctx.Command() {
	case "print":
		if err := config.PrintRedacted(cfg, config.WithFormat(config.FormatYAML), config.WithWriter(os.Stdout)); err != nil {
			log.Fatal(err)
		}
	default:
		fmt.Printf("serving on :%d (log level %s, db %s, metrics %v)\n", cfg.Port, cfg.LogLevel, cfg.DB.Host, cfg.Metrics)
	}
}
```

```txt
$ APP_DB_PASSWORD=s3cret APP_PORT=9000 app --no-metrics --db-host db.internal
serving on :9000 (log level info, db db.internal, metrics false)

$ app --help
...
      --config="config.yaml"    Config file.
  -p, --port=8080               HTTP listen port (config: port; env: APP_PORT)
      --log-level="info"        Log level (config: log_level; env:
                                APP_LOG_LEVEL)
      --timeout=30s             Request timeout (config: timeout; env:
                                APP_TIMEOUT)
      --[no-]metrics            Serve /metrics (config: metrics; env:
                                APP_METRICS)
      --db-host="localhost"     Database host (config: db.host; env:
                                APP_DB_HOST)
      --db-max-conns=25         Connection pool size (config: db.max_conns; env:
                                APP_DB_MAX_CONNS)
```

`--port 9000`, `-p 9000`, `--port=9000`, `--db-host x` and `--no-metrics` override `config.yaml` and `APP_*`; a flag left out does not, although Kong shows the field's default in the help. `db.password` and the maps have no flag (`kong:"-"`): they come from the file or the environment. `app template > config.yaml` writes a starter file with every key and its default (`config.PrintTemplate`), and `app print` the loaded config as YAML, secrets redacted. The program is `examples/cli/main.go`; `ExampleWithOverridesFromStruct` in `example_test.go` runs the same wiring with its output checked.

## How It Works

Kong records every flag it parses from argv as an element of `kong.Context.Path`, under the flag's canonical name whatever spelling was typed — long, short, alias, `--flag=value`, or negated `--no-flag` — with the value already applied. Values supplied by Kong resolvers are marked `Resolved`, defaults never appear, and tokens after `--` are positional. `New` forwards exactly the unresolved flag elements it has a mapping for. `Flag.Set` is not used: Kong sets it for applied defaults too.

`WithOverridesFromStruct[T]` builds the mapping from the parsed CLI itself: it finds the `T` the CLI struct holds (embedded, named, or the CLI struct itself), and maps each Kong flag whose target is a field of that `T` to the field's key from `config.Fields[T]` — by the field's address, so a flag's name, prefix or alias never has to agree with anything. Flags outside `T`, such as `--config`, are the CLI's own and are not forwarded. A CLI struct holding no `T` fails the load with `config.ErrSource`.

Mapped values unflatten into the key tree, so a flag for `db.host` overrides that nested key. The provider loads through `config.WithOverrideProvider`, above the environment.

`HelpFromConfig[T]` is a `kong.PostBuild` option. Every flag that sets a key of `T` takes the field's `desc` when it has no help of its own, and its help gains the key and the environment variable that set the same value. Kong prints a field's `default` itself; `HelpFromConfig` adds it to the help only for a flag Kong shows no default for, and only when diagnostics may print it (`config.Field.PrintableDefault`) — a sensitive default, or one that could hold a sensitive value, is also kept out of Kong's placeholder, which shows `VALUE` instead.

## Rules for the config struct

- **Kong reads some of its tags.** `default` gives Kong the value it shows; `short`, `negatable`, `name`, `placeholder`, `embed` and `prefix` shape the flag. `koanf`, `desc`, `validate`, `sensitive` and `secret` are this package's and the config package's.
- **No Kong `required`, no Kong `env`.** Kong validates before the config pipeline runs, so a Kong `required` could not be satisfied by a file, the environment or a secret; use `validate:"required"`, which runs after every source. Kong's `env` tag would read the same variable a second time, outside the config pipeline; the config package's environment layer already reads it.
- **Fields with no flag.** Tag maps, slices of structs, interfaces and secrets `kong:"-"`: Kong cannot parse them from one flag, and a secret on the command line ends up in shell history and the process list. Nested structs become flag groups with `embed:"" prefix:"<section>-"`.
- **One flag name, one key.** Subcommands share the embedded flags. When two subcommands give one flag name different meanings (`serve --port`, `admin --port`), build the mapping for the selected command and pass it to `WithOverrides`:

```go
mapping := map[string]string{"port": "server.port"}
if kctx.Command() == "admin" {
	mapping = map[string]string{"port": "admin.port"}
}
cfg, err := config.Load[AppConfig](ctx, kongprovider.WithOverrides(kctx, mapping))
```

### A CLI struct separate from the config struct

When the CLI should expose only a few settings under names of its own, keep the CLI struct separate and tie each config field to its flag with a `flag:"name"` tag, then use `WithOverridesFromTags[T]`. `HelpFromConfig` describes those flags the same way, and Kong refuses to build the CLI when a `flag` tag names no flag or two fields share one.

For a CLI tool whose config file only supplies flag defaults, Kong's own `kong.Configuration` with a YAML resolver is the simpler fit. This package is for a service whose configuration tree is larger than its CLI.

## API Reference

- `WithOverridesFromStruct[T](kctx) config.Option` — every typed flag that writes a field of the `T` the CLI struct holds, as an override at that field's key.
- `WithOverridesFromTags[T](kctx) config.Option` — typed flags as overrides, mapped by `flag` tags over `config.Fields[T]`, for a CLI struct separate from the config struct.
- `WithOverrides(kctx, mapping) config.Option` — the same with an explicit Kong flag name → koanf dot-path mapping.
- `New(kctx, mapping) koanf.Provider` — the provider itself, for `config.WithOverrideProvider(p, nil)`.
- `HelpFromConfig[T](envPrefix) kong.Option` — flag help from the config struct's `desc`, key, environment variable and printable default.

## Key Files

| File | Responsibility |
|------|----------------|
| `kongprovider.go` | `New`, `WithOverrides`, `WithOverridesFromStruct`, `WithOverridesFromTags`, `HelpFromConfig`; flag mapping by field address and by tag, and its validation |
| `examples/cli/main.go` | The Quick Start program: an embedded config struct, a flag group, subcommands, `print` and `usage` commands |
| `go.mod`, `LICENSE` | The module (requires the parent at its release version) and its Apache-2.0 license |

## Dependencies

- *Imports*: `github.com/alecthomas/kong`, `github.com/knadh/koanf/v2`, `github.com/knadh/koanf/providers/confmap`, `config`.

## References

- Parent: [`config`](https://github.com/ashtonian/pkg/blob/main/config/README.md) § Kong integration
- [Kong](https://github.com/alecthomas/kong) — the CLI library
