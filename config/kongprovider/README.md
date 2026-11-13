# kongprovider

[![Go Reference](https://pkg.go.dev/badge/github.com/ashtonian/pkg/config/kongprovider.svg)](https://pkg.go.dev/github.com/ashtonian/pkg/config/kongprovider)

Wires a [Kong](https://github.com/alecthomas/kong) CLI to [`config`](../README.md). Embed the config struct in the command that uses it and Kong makes a flag of every field, while the config struct's `koanf`, `default`, `desc` and `validate` tags stay the one schema. `Load` loads the config while Kong parses — before Kong validates — and writes it into that struct, so the command's fields, a `Run` method's parameter and every `Validate` method Kong calls see the file, the environment, secrets and the typed flags, never Kong's defaults alone. Typed flags are the highest-precedence source; only the flags a user typed are forwarded, in any spelling Kong accepts, so a flag left out never masks a config file or an environment variable. `LoadReloader` does the same for a service that reloads. It is a module of its own, so `config` does not depend on Kong.

Requires Go 1.27+.

```bash
go get github.com/ashtonian/pkg/config/kongprovider
```

## Quick start

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"
)

const envPrefix = "APP"

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

// Validate holds the rule no tag states. It sees the loaded config: the
// file, APP_* and the typed flags, never Kong's defaults alone.
func (c *AppConfig) Validate() error {
	if c.DB.MaxConns > 100 && c.Timeout < time.Second {
		return errors.New("a pool of more than 100 connections needs a timeout of at least 1s")
	}
	return nil
}

// ServeCmd and PrintCmd each hold the config; Load fills it for the one that
// runs.
type ServeCmd struct {
	AppConfig `embed:""`
}

func (c *ServeCmd) Run() error {
	fmt.Printf("serving on :%d (log level %s, db %s, metrics %v)\n", c.Port, c.LogLevel, c.DB.Host, c.Metrics)
	return nil
}

type PrintCmd struct {
	AppConfig `embed:""`
}

func (c *PrintCmd) Run() error {
	return config.PrintRedacted(&c.AppConfig, config.WithFormat(config.FormatYAML), config.WithWriter(os.Stdout))
}

// UsageCmd and TemplateCmd hold no config, so they run without one.
type UsageCmd struct{}

func (UsageCmd) Run() error { return config.PrintUsage[AppConfig](envPrefix) }

type TemplateCmd struct{}

func (TemplateCmd) Run() error { return config.PrintTemplate[AppConfig](envPrefix) }

// CLI adds what is the CLI's own: the config file's path and the commands.
type CLI struct {
	Config string `type:"path" default:"config.yaml" help:"Config file."`

	Serve    ServeCmd    `cmd:"" default:"withargs" help:"Run the server."`
	Print    PrintCmd    `cmd:"" help:"Print the effective configuration as YAML, secrets redacted."`
	Usage    UsageCmd    `cmd:"" help:"List every setting with its environment variable."`
	Template TemplateCmd `cmd:"" help:"Write a starter config file: every key with its default."`
}

func main() {
	ctx := context.Background()
	var cli CLI
	// Called during Parse, once the command line is parsed: cli.Config holds
	// --config.
	files := func(*kong.Context) ([]config.Option, error) {
		return []config.Option{config.WithFile(cli.Config)}, nil
	}
	kctx := kong.Parse(&cli,
		kongprovider.Load(ctx, &cli.Serve.AppConfig, []string{envPrefix}, files),
		kongprovider.Load(ctx, &cli.Print.AppConfig, []string{envPrefix}, files),
	)
	kctx.FatalIfErrorf(kctx.Run())
}
```

```txt
$ APP_DB_PASSWORD=s3cret APP_PORT=9000 app --no-metrics --db-host db.internal
serving on :9000 (log level info, db db.internal, metrics false)

$ app serve
app: error: validating config: configuration validation failed: db.password(required)

$ app serve --help
Usage: app serve [flags]

Run the server.

Flags:
  -h, --help                    Show context-sensitive help.
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

`--port 9000`, `-p 9000`, `--port=9000`, `--db-host x` and `--no-metrics` override `config.yaml` and `APP_*`; a flag left out does not, although Kong shows the field's default in the help. `serve` is the default command and takes its flags without being named (`default:"withargs"`). `db.password` and the maps have no flag (`kong:"-"`): they come from the file or the environment, and `ServeCmd` holds them after Parse like every other field. `app template > config.yaml` writes a starter file with every key and its default (`config.PrintTemplate`), and `app usage` lists every setting; neither command holds the config, so neither loads it, and both run before a password exists. `app print` prints the loaded config as YAML, secrets redacted. The program is `examples/cli/main.go`; `ExampleLoad` in `example_test.go` runs the same wiring with its output checked.

## Reloading

`LoadReloader` builds a `config.Reloader[T]` from the same sources and binds it for `Run`. The command owns its lifecycle — `Arm`, `go r.Run(ctx)`, and `Stop` under a shutdown deadline — and reads `r.Config()` wherever it needs a value that can change. The struct the command embeds holds a copy of the reloader's first snapshot: the startup config, which is what a key that waits for a restart holds for the life of the process, and what this listener's address is read from.

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"
)

// AppConfig: the listener's settings take effect at start, the greeting on
// every request.
type AppConfig struct {
	Addr            string        `koanf:"addr" default:":8080" desc:"Listen address"`
	ShutdownTimeout time.Duration `koanf:"shutdown_timeout" default:"15s" validate:"min=1ms" desc:"Drain deadline on SIGTERM"`
	Greeting        string        `koanf:"greeting" default:"hello" validate:"required" desc:"What GET / answers"`
}

type ServeCmd struct {
	AppConfig `embed:""`
}

// Run serves until SIGINT or SIGTERM. The receiver holds the startup config,
// which the listener uses; each request reads r.Config(), so an edit to the
// file, or SIGHUP, changes the greeting without a restart.
func (c *ServeCmd) Run(r *config.Reloader[AppConfig]) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := r.Arm(ctx); err != nil {
		return err // a config directory that cannot be watched
	}
	go func() {
		if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "config reloader ended", "error", err)
		}
	}()

	server := &http.Server{
		Addr:              c.Addr,
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintln(w, r.Config().Greeting)
		}),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	var err error
	select {
	case err = <-serveErr:
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.ShutdownTimeout)
	defer cancel()
	return errors.Join(err, server.Shutdown(shutdownCtx), r.Stop(shutdownCtx))
}

type CLI struct {
	Config string   `type:"path" default:"config.yaml" help:"Config file."`
	Serve  ServeCmd `cmd:"" default:"withargs" help:"Run the server."`
}

func main() {
	var cli CLI
	files := func(*kong.Context) ([]config.Option, error) {
		return []config.Option{config.WithFile(cli.Config)}, nil
	}
	kctx := kong.Parse(&cli,
		kongprovider.LoadReloader(context.Background(), &cli.Serve.AppConfig, []string{"APP"}, files,
			config.WithFileWatch(0),
			config.WithSignalReload(syscall.SIGHUP),
		),
	)
	if err := kctx.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err) // names the failing key, never a secret's value
		os.Exit(1)
	}
}
```

The program is `examples/reload/main.go`. No `*AppConfig` is bound for `Run`: a reloading command asks for the reloader, so it cannot mistake the startup copy for the live config. The typed flags are copied when the reloader is built, so every reload keeps the startup command line, and a typed flag pins its key for the life of the process: `--addr` above wins over every later edit of `addr` in the file. A key meant to change live must be set only by sources the reloader reads again — the file, the environment variables it reads at each reload, providers — not on the command line. `ctx` bounds the initial load only, and `ApplyInitial` appliers run during Parse; reloads use the context given to `Run` or `Reload`.

## How it works

Kong parses in this order: `BeforeReset` hooks, `Reset` (defaults), `BeforeResolve`, `Resolve` (Kong's resolvers), `BeforeApply`, `Apply` (parsed values into the struct), `Validate` (the `Validate() error` method of every command on the path, and of every exported embedded field of each, recursively), then `AfterApply`. Kong has no hook between `Apply` and `Validate`, so `Load` loads in a `BeforeApply` hook — the last one Kong calls, after every `BeforeApply` method the CLI's own types declare:

1. It checks that a node on the parsed path — the application, a command, an argument — holds a flag that writes into the config. If none does, it does nothing: a sibling command's config is neither loaded nor validated. `--help` and `--version` exit in `BeforeReset`, before any load.
2. It applies what Kong parsed, as Kong's `Apply` will again, so the options function reads the CLI's own flags and arguments (`cli.Config`) and the typed flags are where `WithOverridesFromStruct` reads them.
3. It calls the options function, adds `config.WithEnvPrefixes(envPrefixes...)` and `WithOverridesFromStruct`, and runs `config.Load` (or `config.NewReloader`). A failure fails Parse with that error.
4. It writes the loaded config into the struct, keeping the sections Kong allocated for embedded pointers (`DB *DBConfig \`embed:""\``) so the flags still write into it; a section the load left nil is set nil.
5. It replaces the parsed values of the config's flags on the path — typed and resolved — with the loaded values, so Kong's `Apply` writes the loaded config and its validators see it. A `WithPostLoad` hook that normalizes a typed value is therefore what Kong validates too.
6. It binds the config (`Load`) or the reloader (`LoadReloader`) for `Run`.

`config.Load` already runs the config's tag rules and, when `*T` implements it, its `Validate` method; Kong runs that method again on the same value, and also runs the `Validate` methods of the config's embedded sections, which the config package does not call. A section's error is Kong's: it fails Parse as a usage error.

Kong records every flag it parses from argv as an element of `kong.Context.Path`, under the flag's canonical name whatever spelling was typed — long, short, alias, `--flag=value`, or negated `--no-flag` — with the value already applied. Values supplied by Kong resolvers are marked `Resolved`, defaults never appear, and tokens after `--` are positional. Only the unresolved elements are forwarded. `Flag.Set` is not used: Kong sets it for applied defaults too. Flags map to keys by the field each flag writes, matched by address in one traversal with `config.MatchFields`, so a flag's name, prefix or alias does not need to match a key, a sibling command's flag with the same name cannot set the key, and the CLI's own flags such as `--config` are not forwarded. Mapped values unflatten into the key tree, so a flag for `db.host` overrides that nested key. The provider is a `config.OriginProvider`: in a snapshot with provenance (`config.LoadSnapshot`, a reloader `WithProvenance`), each value it set is attributed to its flag by canonical name — `flag --db-host`, however it was typed — rather than to "override provider 0".

The help comes from the config struct. Every flag that sets a key takes the field's `desc` when it has no help of its own, and its help gains the key and the environment variables that set the same value, in the order the load reads them: with `envPrefixes` `["APP_WORKER", "APP"]`, `--db-host` reads `(config: db.host; env: APP_WORKER_DB_HOST, APP_DB_HOST)`. Kong prints a field's `default` itself; the help adds it only for a flag Kong shows no default for, and only when diagnostics may print it (`config.Field.PrintableDefault`) — a sensitive default, or one that could hold a sensitive value, is also kept out of Kong's placeholder, which shows `VALUE` instead.

## Wiring by hand

`HelpFromConfig` and `WithOverridesFromStruct` are the two halves of `Load`, for a program that loads the config itself after Parse:

```go
var cli CLI // embeds AppConfig
kctx := kong.Parse(&cli, kongprovider.HelpFromConfig(&cli.AppConfig, "APP"))
cfg, err := config.Load[AppConfig](ctx,
	config.WithFile(cli.Config),
	config.WithEnvPrefix("APP"),
	kongprovider.WithOverridesFromStruct(kctx, &cli.AppConfig),
)
```

The struct inside the CLI is then Kong's view only — tag defaults and typed flags — and only `cfg` holds the environment, files and secrets. Two consequences follow, and they are why `Load` exists:

- **Kong validates its own view.** During Parse, before the load, Kong calls the `Validate` method of the config and of every exported embedded section on that view. A rule that refuses the defaults fails an invocation configured entirely by the environment as a usage error (exit 80) before the config loads, and a rule the loaded config breaks can pass Kong's check. Keep cross-field rules off the shared struct with this wiring, or use `Load`.
- **The struct is stale.** A `Run` method that reads its receiver sees the defaults for every field the environment or a file sets. Pass `cfg` instead.

Pass the same prefixes to `HelpFromConfig` and `config.WithEnvPrefixes`, or the help names variables the load never reads. Without `HelpFromConfig`, Kong reads the fields' `env` tags itself (see below). `WithOverridesFromStruct` copies the typed values when it is called, so a reloader built from it keeps the command line it started with.

When the CLI exposes only a few settings under names of its own, keep the structs apart and pass `WithOverrides` the flag name → key mapping; the load fails when a mapping names a flag the CLI does not have.

## Rules for the config struct

- **Put the config in the command that uses it.** `Load` runs for a command whose path holds one of the config's flags. A config embedded in the CLI root is on every path, so every command — `template` and `usage` included — would load and validate it.
- **Kong reads some of its tags.** `default` gives Kong the value it shows, and `sep` splits a list flag; `short`, `negatable`, `name`, `aliases`, `placeholder`, `embed` and `prefix` shape the flag, and an `enum` is checked by Kong against the loaded value. `koanf`, `desc`, `validate`, `sensitive` and `secret` are this package's and the config package's.
- **Separators differ.** Kong uses one rune and recognizes backslash-escaped separators; config splits literally on the whole separator string. Use one rune (or `none`) and no escaped separators when sharing textual defaults or environment values. Typed flags are already decoded by Kong and are forwarded as lists.
- **The shared schema comes from config.** This module requires a compatible config release, including support for `env`, `sep` and checked field discovery.
- **No Kong `required`.** Kong checks it against the command line alone, so a Kong `required` could not be satisfied by a file, the environment or a secret; use `validate:"required"`, which runs after every source.
- **`env` belongs to the config package.** It reads a field's `env` variables in its environment layer, below the prefixed variable, and reports a value that does not decode without quoting it. `Load`, `LoadReloader` and `HelpFromConfig` clear the tag from Kong's view of each config flag and name the variables in the help instead, so Kong does not parse them as flag defaults — where a variable meant for something else, such as a Kubernetes service link (`REDIS_PORT=tcp://…`), would fail the command line before the config loads, quoting its value. Without one of them, Kong reads them.
- **Fields with no flag.** Tag maps, slices of structs, interfaces and secrets `kong:"-"`: maps use Kong's separate `mapsep` syntax, and other composite values may need custom mappers. A secret on the command line ends up in shell history and the process list. Nested structs become flag groups with `embed:"" prefix:"<section>-"`.
- **Exported types.** Kong walks exported embedded fields for `Validate` methods; a section of an unexported type embedded under its type name is skipped.
- **One flag name, one key.** Subcommands share the embedded flags. When two subcommands give one flag name different meanings (`serve --port`, `admin --port`), give each command its own config struct and `Load`, or build the mapping for the selected command and pass it to `WithOverrides`:

```go
mapping := map[string]string{"port": "server.port"}
if kctx.Command() == "admin" {
	mapping = map[string]string{"port": "admin.port"}
}
cfg, err := config.Load[AppConfig](ctx, kongprovider.WithOverrides(kctx, mapping))
```

## Configuration

The module reads no environment variables or files of its own; the config package's options decide every source. The options take:

| Parameter | Type | Default | Description |
|---|---|---|---|
| `ctx` | `context.Context` | required | Bounds the load during Parse (the initial load, for `LoadReloader`). Reloads use the context given to `Run` or `Reload`. |
| `cfg` | `*T` | required | The config struct inside the parsed CLI struct: embedded, named, or the CLI struct itself. `nil`, or a struct no flag writes into, fails `kong.New`. |
| `envPrefixes` | `[]string` | none: the environment is not read | The prefixes `config.WithEnvPrefixes` takes, most specific first. They are the load's whole environment layer — a `config.WithEnvPrefix(es)` among `opts` is overridden — and the variables the help names. |
| `opts` | `func(*kong.Context) ([]config.Option, error)` | `nil`: no further options | Called once per Parse that loads, after the command line is applied: files, providers, secrets, validators, hooks, logger. Its error fails Parse. Do not include `WithOverridesFromStruct` or a second environment option. |
| `reloadOpts` | `...config.ReloadOption` | none | `LoadReloader` only: the file watch, poll interval, signals, appliers and callbacks. |

## API

- `Load(ctx, cfg *T, envPrefixes, opts) kong.Option` — help for `cfg`'s flags; when the selected command path holds one, loads `T` during Parse, before Kong validates, writes it into `*cfg` and binds `cfg` for `Run`.
- `LoadReloader(ctx, cfg *T, envPrefixes, opts, reloadOpts...) kong.Option` — the same with a `config.Reloader[T]`: `*cfg` gets a copy of its first snapshot, and the reloader is bound for `Run`. It never arms, runs or stops the reloader.
- `HelpFromConfig(cfg *T, envPrefixes...) kong.Option` — help for every flag that writes a field of `cfg`: its `desc`, key, environment variables in precedence order and printable default.
- `WithOverridesFromStruct(kctx, cfg *T) config.Option` — every typed flag that writes a field of `cfg`, a config struct inside the parsed CLI struct, as an override at that field's key.
- `WithOverrides(kctx, mapping) config.Option` — the same with an explicit flag name → key mapping.
- `New(kctx, mapping) koanf.Provider` — the provider itself, for `config.WithOverrideProvider(p, nil)`; it also implements `config.OriginProvider`, naming each value's flag.

A `Run` method or `kctx.Call` function takes the bound value as a parameter: `Run(cfg *AppConfig) error` after `Load`, `Run(r *config.Reloader[AppConfig]) error` after `LoadReloader`. Each command's config is bound only when that command loads it, so commands that embed the same config type each receive their own.

Every operation validates the complete config field catalog, including fields with no matching CLI flag. Conflicting keys, such as `koanf:"port"` and `koanf:"PORT"` on different fields, return an error matching `config.ErrSchema`. `Load`, `LoadReloader` and `HelpFromConfig` report it when Kong builds the parser; `WithOverridesFromStruct` retains it in the returned option and reports it during config loading. This structural check does not decode defaults or example tags. Ambiguous field addresses are reported with `config.ErrKey` rather than choosing a key. Matching uses the current instance, never caches addresses globally, and never allocates missing pointer ancestors. Loading still validates and decodes defaults; reading metadata does not.

## Build and test

From the repository root, `make test-config` runs this module's race tests with the config module's, `make lint` runs vet and staticcheck, and `make release-check` builds and tests it outside the workspace at the versions its `go.mod` requires. In this directory, `go test -race ./...` runs the tests, `README.md`'s two programs are compiled as `examples/cli` and `examples/reload` and a test fails when either block drifts from its file. Run the quick start with `cd examples/cli && APP_DB_PASSWORD=test go run . --no-metrics`.

## Operations and security

| Symptom | Check | Then |
|---|---|---|
| `app: error: <config error>`, exit 1 | The config package's message: it names the key and the rule (`db.password(required)`), the file and line, or the source. | Fix the source it names. The load's categories survive Kong's wrapping: `errors.Is(err, config.ErrValidation)` holds on `parser.Parse`'s error. |
| `app: error: <command>: <message>`, exit 80 | A `Validate` method only Kong calls — an embedded section's — or a Kong rule (`enum`, `xor`, missing argument) refused the loaded config. | Kong prints usage with it under `kong.UsageOnError()`. Move the rule to the root config's `Validate` to report it as a load error. |
| A flag has no effect | `app <command> --help` lists it under that command? Is the field `kong:"-"`? | Flags belong to the command that embeds the config. |
| A file edit does not take effect after a reload | Is the key typed on the command line, or set by an environment variable? | Both outrank the file; a typed flag pins the key for the life of the process. Remove it from the command line or the environment. |
| `kongprovider: Kong's parsed value of --x cannot be replaced` | The Kong version in the build. | This module is tested with the Kong version its `go.mod` requires; a Kong release that stores parsed values differently is not supported until this module supports it. |
| `no flag of the CLI … writes a field of the supplied config` | `cfg` points into the struct passed to `kong.New`, not a copy. | Pass `&cli.Serve.AppConfig`, the address inside the parsed CLI. |

The module logs nothing and exports no metrics; the config package's logger (`config.WithLogger` among `opts`) records loads and reloads. Typed values never appear in errors from this package, and sensitive defaults stay out of the help and Kong's placeholders. Secrets belong in files, the environment or a secret store (`secret` tags), never on the command line, where they reach shell history and the process list: mark them `kong:"-"`. After Parse, `*cfg` holds resolved secrets like any loaded config; print it with `config.PrintRedacted`.

## Dependencies

[Kong](https://github.com/alecthomas/kong), the [config](../README.md) module and koanf's `confmap` provider; versions are in [go.mod](go.mod). No external service is required.

## Code map

| File | Responsibility |
|---|---|
| `load.go` | `Load`, `LoadReloader`: the Parse hook, command selection, the write into the CLI's struct |
| `kongprovider.go` | The typed-flag provider (`New`, `WithOverrides`, `WithOverridesFromStruct`), `HelpFromConfig`, flag → field matching and value copying |
| `examples/cli`, `examples/reload` | The README's two programs |

## License

[MIT](../../LICENSE)
