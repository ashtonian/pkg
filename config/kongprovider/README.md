# kongprovider

[![Go Reference](https://pkg.go.dev/badge/github.com/ashtonian/pkg/config/kongprovider.svg)](https://pkg.go.dev/github.com/ashtonian/pkg/config/kongprovider)

Brings [Kong](https://github.com/alecthomas/kong) CLI flags into [`config`](../README.md) as its highest-precedence source, and describes the config struct in Kong's help. Embed the config struct in the CLI struct and Kong makes a flag of every field, while the config struct's `koanf`, `default`, `desc` and `validate` tags stay the one schema. Only the flags a user typed are forwarded — in any spelling Kong accepts — so a flag left out never masks a config file or an environment variable. It is a module of its own, so `config` does not depend on Kong.

```bash
go get github.com/ashtonian/pkg/config/kongprovider
```

## Quick start

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
	kctx := kong.Parse(&cli, kongprovider.HelpFromConfig(&cli.AppConfig, "APP"))
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
		kongprovider.WithOverridesFromStruct(kctx, &cli.AppConfig),
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

## How it works

Kong records every flag it parses from argv as an element of `kong.Context.Path`, under the flag's canonical name whatever spelling was typed — long, short, alias, `--flag=value`, or negated `--no-flag` — with the value already applied. Values supplied by Kong resolvers are marked `Resolved`, defaults never appear, and tokens after `--` are positional. `New` forwards exactly the unresolved flag elements it has a mapping for. `Flag.Set` is not used: Kong sets it for applied defaults too.

`WithOverridesFromStruct(kctx, &cli.AppConfig)` takes the config struct inside the parsed CLI struct — embedded, named, or the CLI struct itself — and maps each typed flag whose target is a field of it to that field's key, through `config.KeyOfValue`: by the field's address, so a flag's name, prefix or alias never has to agree with anything, and a sibling command's flag with the same name cannot reach a config key. Flags outside it, such as `--config`, are the CLI's own and are not forwarded. The typed values are copied deeply when the option is built — through pointers, slices and maps — so a reloader built from it keeps the command line it started with, whatever later writes to the flags' targets. A pointer that no flag of the CLI writes into fails the load with `config.ErrSource`.

Mapped values unflatten into the key tree, so a flag for `db.host` overrides that nested key. The provider loads through `config.WithOverrideProvider`, above the environment. It is a `config.OriginProvider`: in a snapshot with provenance (`config.LoadSnapshot`, a reloader `WithProvenance`), each value it set is attributed to its flag by canonical name — `flag --db-host`, however it was typed — rather than to "override provider 0".

`HelpFromConfig(&cli.AppConfig, envPrefix)` is a `kong.PostBuild` option. Every flag that sets a key of the config struct takes the field's `desc` when it has no help of its own, and its help gains the key and the environment variable that set the same value. Kong prints a field's `default` itself; `HelpFromConfig` adds it to the help only for a flag Kong shows no default for, and only when diagnostics may print it (`config.Field.PrintableDefault`) — a sensitive default, or one that could hold a sensitive value, is also kept out of Kong's placeholder, which shows `VALUE` instead.

## Rules for the config struct

- **Kong reads some of its tags.** `default` gives Kong the value it shows, and `sep` splits a list flag as the config package splits a list's strings; `short`, `negatable`, `name`, `placeholder`, `embed` and `prefix` shape the flag. `koanf`, `desc`, `validate`, `sensitive` and `secret` are this package's and the config package's.
- **No Kong `required`.** Kong validates before the config pipeline runs, so a Kong `required` could not be satisfied by a file, the environment or a secret; use `validate:"required"`, which runs after every source.
- **`env` is read by both.** The config package reads a field's `env` variables in its environment layer, below the prefixed variable. Kong reads the same tag as the flag's default and lists the variables in its help; kongprovider forwards only flags typed on the command line, so the config package's precedence decides the value. A variable Kong cannot parse into the flag's type fails the command line before the config loads.
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

When the CLI exposes only a few settings under names of its own, keep the structs apart and pass `WithOverrides` the flag name → key mapping; the load fails when a mapping names a flag the CLI does not have.

## API

- `WithOverridesFromStruct(kctx, cfg *T) config.Option` — every typed flag that writes a field of `cfg`, a config struct inside the parsed CLI struct, as an override at that field's key.
- `WithOverrides(kctx, mapping) config.Option` — the same with an explicit flag name → key mapping.
- `New(kctx, mapping) koanf.Provider` — the provider itself, for `config.WithOverrideProvider(p, nil)`; it also implements `config.OriginProvider`, naming each value's flag.
- `HelpFromConfig(cfg *T, envPrefix) kong.Option` — help for every flag that writes a field of `cfg`: its `desc`, key, environment variable and printable default.

## License

[MIT](../../LICENSE)
