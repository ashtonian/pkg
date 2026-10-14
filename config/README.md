# config

[![Go Reference](https://pkg.go.dev/badge/github.com/ashtonian/pkg/config.svg)](https://pkg.go.dev/github.com/ashtonian/pkg/config)

Typed configuration for long-running Go services, declared as one tagged Go struct and built on [koanf](https://github.com/knadh/koanf). The struct is the schema: its tags give each field its key, former names, variables, default, example, rules, description and secrecy, and every feature reads them. Every config, at boot and on every reload, goes through the same pipeline: merge the sources, decode, resolve secrets, normalize, validate.

```go
type DBConfig struct {
	Host     string `koanf:"host" default:"localhost" desc:"Database host"`
	MaxConns int    `koanf:"max_conns" default:"25" validate:"min=1,max=500" desc:"Connection pool size"`
	Password string `koanf:"password" validate:"required" secret:"prod/db#password" desc:"Database password"`
}

type AppConfig struct {
	DB DBConfig `koanf:"db"`
}
```

With `WithEnvPrefix("APP")`, `APP_DB_MAX_CONNS` sets the pool size; a file setting `max_conns: 0` fails validation naming `db.max_conns`; a `SecretsFrom` fetch for your store fills the password from `prod/db#password`, and no output ever shows it; and usage tables, a starter config file, a JSON Schema for editors and kongprovider's flag help all describe the three fields, with no other declaration to write.

- **The struct is the schema.** No second definition to keep in step: environment variable names, flag help, usage tables, starter files and the JSON Schema are derived from the tags, so none of them drifts from the code.
- **Any source.** Struct-tag defaults, files, environment variables, CLI flags, and any koanf provider: Kubernetes volumes, Vault, AWS, Consul, etcd, a database table.
- **Strict and validated.** A key no field takes fails the load, with the key it most likely misspells, or goes to a handler that warns; `validate` tags and a `Validate` method run on every load, and errors name the config key, never a secret's value.
- **Hot reload that cannot break production.** A reload publishes a new config only if it passed the whole pipeline; triggers are a file watch that understands Kubernetes volumes, a poll, a signal, or a provider's own watch.
- **Changes pushed, not just published.** Appliers push the changes they own into the running process — a log level, pool limits, feature gates — are retried after a failure, and the reloader reports what still waits for a restart.
- **Output that never leaks a credential.** Redacted printing, config diffs, where each value came from, usage tables, starter config files and a JSON Schema for editors.

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

The program is [`examples/quickstart`](examples/quickstart/main.go). The package's `Example` functions, named in the sections below, run as tests with their output checked.

## Defining a config

A config is a plain struct, and its tags are the whole schema: they say how each field is keyed, defaulted, validated, described and protected, and every part of the package reads them.

| Tag | Meaning | Read by | Example |
|---|---|---|---|
| `koanf` | The key. `-` skips the field, `,squash` merges a struct into its parent, `,remain` collects unknown keys in a map. Without a tag the key is the lowercased field name. | Every source and output; environment variable names derive from it. | `koanf:"max_conns"` |
| `default` | The lowest-precedence value; comma-separated for a slice, `key=value` entries for a map, which a source's entries merge with. A malformed map default fails with `ErrSchema`. | The load; usage tables, starter files, the JSON Schema and flag help show it unless the field is sensitive. | `default:"localhost"` |
| `alias` | Former keys of the field, which sources may still set: a renamed setting keeps reading deployed files, and each load warns to rename them. | Every source; the JSON Schema lists them as deprecated. | `alias:"maxconns"` |
| `sep` | The separator that splits a string set for a list — a default, a variable, a file's string — into elements, so an element may hold a comma: `sep:";"`. `none` keeps the string as one element. Without it, commas split, after any `WithDecodeHook` hook. | Every source and tag default, element defaults, starter files, the JSON Schema, examples. | `sep:";"` |
| `env` | More variables for the field, read as written, without the prefix — a platform's `PORT`, a `DATABASE_URL`, a variable a renamed key used to have. The prefixed variable wins, then the first set in the list. | Environment loading; usage tables, starter files and `Field.EnvVars` list them. | `env:"PORT,HTTP_PORT"` |
| `validate` | [go-playground/validator](https://pkg.go.dev/github.com/go-playground/validator/v10) rules. | Validation on every load and reload; `required` marks usage tables and starter files; the JSON Schema states the bounds, lengths and `oneof` enums. | `validate:"required,min=1"` |
| `desc` | What the setting is for. | Usage tables, `PrintRedacted(..., WithIncludeDescriptions())`, starter-file comments, the JSON Schema `description`, kongprovider flag help. | `desc:"Database host"` |
| `example` | A sample value, decoded as a default would be; one its field cannot hold fails with `ErrSchema`. Never a value the service runs with. | Starter files write it, commented out, where there is no default; usage tables and the JSON Schema `examples` show it — unless the field is sensitive. | `example:"db.internal"` |
| `sensitive` | `"true"` redacts everywhere — on a struct, slice or map, everything inside it; `"false"` exempts a field whose name only looks like a credential's (`max_tokens`). | Printing, diffs, reload results and usage tables show `[REDACTED]`; starter files and the JSON Schema leave out its default and any rule that names a value. | `sensitive:"true"` |
| `secret` | Where the value lives in your secret store (see [Secrets](#secrets)); implies `sensitive`. | `SecretsFrom` and `WithSecretPopulator`, after decoding and before validation. | `secret:"prod/db#password"` |

- Pointers are followed. A struct that decodes itself — an `encoding.TextUnmarshaler` such as `time.Time` or `netip.Addr`, or a mapstructure `Unmarshaler` — is one value, not a section.
- Maps, slices and interfaces are values whose entries are keys of their own. An element struct's `default` tags apply to every map entry and list element a source creates, below what the source set. An explicit `null` stays null; write `{}` for an entry that should take its defaults.
- Two fields of one struct may not share a key, and a key may not contain `.`, `[`, `]`, `*`, a quote, a backslash or a control character: such a type is refused with `ErrSchema` before anything loads.

Every output names a value the same way:

| Key | Names |
|---|---|
| `db.host` | a struct field |
| `items[0].port` | a field of a list element |
| `named["primary"].port` | a field of a map entry; map keys are always quoted |
| `items[*].port` | any element or entry, in usage tables and `FieldFor` |

`config.KeyOf(&cfg, &cfg.DB.MaxConns)` returns `"db.max_conns"` from the field itself, so code never spells a key that can drift from its field.

## Sources and precedence

Highest wins:

| | Option | Use |
|---|---|---|
| 1 | `WithOverrideProvider(p, parser)` | Operator overrides, such as CLI flags through [kongprovider](kongprovider). |
| 2 | `WithEnvPrefix(prefix)` | `PREFIX_*` environment variables. |
| 3 | `WithFile`, `WithRequiredFile` | Config files, merged in order, each parsed by its name's suffix: YAML or JSON built in, any koanf parser through `WithFileParser`. |
| 4 | `WithBaseProvider(p, parser)` | A shared or remote base the files refine. |
| 5 | `WithDefaults(map)` | Programmatic defaults keyed by dotted path. |
| 6 | `default` tags | Always. |

Each file takes the parser registered for the longest suffix of its name, ignoring case: `.yaml` and `.yml` are YAML, `.json` is JSON, and any other file is YAML. `WithFileParser(".toml", toml.Parser())` adds a format, so `config.yaml`, `overrides.json` and `local.toml` load together; `WithFileParser("", p)` changes the parser for every file no other suffix matches. JSON is strict: one object per file, integers kept exact, a key set twice in one object an error.

An optional file may be absent, but one that exists and cannot be read or parsed fails the load. Under a reloader, a file that loaded once stays required, so deleting it fails the reload rather than silently reverting its keys.

Keys match fields in any letter case, as decoding does, and each source's keys are respelled as the fields are before the merge — so precedence holds whatever case a source used. A source that spells one field two ways (`port` and `Port`) fails the load.

### Any koanf provider

A provider loads at either provider layer: pass a nil parser when it returns keys and a parser when it returns a document. Every reload reads it again through the same pipeline. A provider that implements `ContextProvider` is read with the load's context, so a slow remote read stops when the load is cancelled.

| Source | Module | Reload on |
|---|---|---|
| Kubernetes ConfigMap / Secret volume, a key per file | `github.com/knadh/koanf/providers/k8smount` | its `Watch`, or the file watch |
| AWS S3 object | `github.com/knadh/koanf/providers/s3` | a poll |
| AWS AppConfig | `github.com/knadh/koanf/providers/appconfig/v2` | its `Watch`, or a poll |
| AWS SSM Parameter Store | `github.com/knadh/koanf/providers/parameterstore/v2` | a poll |
| HashiCorp Vault KV | `github.com/knadh/koanf/providers/vault/v2` | a poll |
| Azure Key Vault | `github.com/knadh/koanf/providers/azkeyvault` | a poll |
| Consul KV, etcd, NATS KV | `…/providers/consul/v2`, `…/etcd/v2`, `…/nats` | its `Watch` |
| A database, an API, any function | `config.NewMapProvider` | a poll, or `SourceChanged` |

Bridging a provider's watch to a reloader is one call — `p.Watch(func(_ any, err error) { if err == nil { reloader.SourceChanged() } })` — and bursts coalesce into one reload.

`NewMapProvider(read, ".")` turns a function into a provider read on every load; with `"."` its keys are dotted paths, with `""` the map is already nested. What `read` returns is copied.

### Environment variables

With `WithEnvPrefix("APP")`, a key's variable is the prefix and its path, upper-cased, `.` becoming `_`. Names are matched against the key tree rather than split, so underscores inside keys survive:

| Variable | Key |
|---|---|
| `APP_DB_MAX_CONNS` | `db.max_conns` |
| `APP_LABELS_TEAM_NAME` (`map[string]string`) | `labels["team_name"]` |
| `APP_FEATURES_AUDIT_LOGS_MAX_PAGE_SIZE` (`map[string]Limits`) | `features["audit_logs"].max_page_size` |
| `APP_TAGS=web,api` (`[]string`) | `tags` = `[web api]` |
| `APP_LABELS=team=core,tier=1` (`map[string]string`) | `labels`, the whole map |

An `env` tag adds variables a key reads as written, without the prefix, for names a platform or another tool sets: `Port int koanf:"port" env:"PORT"` reads `APP_PORT`, then `PORT`. The prefixed variable wins, then the first set in the tag's list. Tagged variables are read only when `WithEnvPrefix` turns the environment on; a tag on a section, on a field of a list element or map entry, or naming a variable another field names fails with `ErrSchema`.

A variable that two keys could read — `db.port` and `db_port`, a field and a map entry, or a tagged name that is another key's own variable — fails the load naming every reading, instead of guessing. A variable under the prefix that names no key is ignored and logged at debug level — other programs may share the prefix — unless a `WithUnknownKeys` handler is set, which sees it with the variable it most likely misspells. `WithEnviron(fn)` replaces `os.Environ`, which keeps tests independent of the machine. `PrintUsage` lists every variable a config reads.

## Decoding and unknown keys

Values decode with mapstructure's weak typing (`"8080"` fills an `int`) plus string → `time.Duration`, comma-separated string → a slice of any element type (`"80,443"` fills a `[]int`, `"1s,2m"` a `[]time.Duration`; a `[]byte` or a slice type that decodes itself takes the string whole; a field's `sep` tag splits on its own separator instead, before any hook), `key=value` entries → a map (`"team=core,tier=1"`; a map of structs takes no string), and `encoding.TextUnmarshaler`. `WithDecodeHook` adds hooks of your own, which run first.

A key that no field decodes fails the load with `ErrValidation`, naming the key and the field of the same struct it most likely misspells — a typo cannot leave a service running on the default it was meant to override:

```txt
unmarshaling config: configuration validation failed: keys no field of main.AppConfig decodes: server.prot (did you mean server.port?)
```

`errors.As` reaches an `*UnknownKeysError`, whose `Keys` give each key and its `Suggestion`, for a program to report them in its own words. A suggestion is the field fewest edits away, ignoring case, within a third of the key's length (at least 2) and sharing at least one character in place; a key no field is that close to has none.

The check follows decoding exactly: it looks inside list elements and map entries of structs, through squashed structs, and lets through a map's own keys, anything beneath an interface, and keys a `,remain` field collects. A section no field holds is one key (`legacy`, not `legacy.old`).

`WithUnknownKeys(handle)` decides instead. The handler gets the unknown keys, and the variables under the `WithEnvPrefix` prefix that name no key, at every load and reload; a nil return accepts the load, an error fails it with `ErrValidation`. Accept and warn:

```go
config.WithUnknownKeys(func(ctx context.Context, unknown *config.UnknownKeysError) error {
	logger.WarnContext(ctx, "config input no field reads", "error", unknown)
	return nil
})
```

```txt
keys no field of main.AppConfig decodes: server.prot (did you mean server.port?); environment variables no field of main.AppConfig reads: APP_SERVER_PROT (did you mean APP_SERVER_PORT?)
```

Or accept some and fail on the rest, by returning a copy of `unknown` without the keys you expect — a section another program reads, a setting you removed but deployed files still carry. Keys and variables are named, never their values. `WithAllowUnknownKeys()` accepts everything silently, for a file shared with another program. A field typed `any` or `map[string]any` accepts everything beneath its key while the rest of the config stays strict.

### Renaming a key

Because an unknown key fails the load, renaming a setting would break every file still using the old name. Keep the old name as an `alias`:

```go
MaxConns int `koanf:"max_conns" alias:"maxconns,max_connections"`
```

Each source's keys are rewritten to the field's key before the sources merge, so precedence holds across old and new names, and a source that sets both fails as a field spelled two ways. Each load logs at warn level which keys used a former name, in which source, so operators know to rename them; the JSON Schema accepts the old names, marked `deprecated`. An alias renames keys, not variables: give a renamed key's old variable an `env` tag.

## Secrets

Tag a field with where its value lives and give the load a fetch function for your store. `SecretsFrom` sets every `secret` field to what the fetch returns, decoded into the field's type, after decoding and before post-load hooks and validation:

```go
type DBConfig struct {
	Host     string `koanf:"host" default:"localhost"`
	User     string `koanf:"user" secret:"prod/db#username"`
	Password string `koanf:"password" secret:"prod/db#password"`
	APIKey   string `koanf:"api_key" secret:"prod/api-key,optional"`
}

cfg, err := config.Load[AppConfig](ctx,
	config.WithFile("config.yaml"),
	config.WithSecretPopulator(config.SecretsFrom(func(ctx context.Context, ref string) (string, error) {
		out, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &ref})
		var missing *types.ResourceNotFoundException
		if errors.As(err, &missing) {
			return "", fmt.Errorf("%w: %s", config.ErrSecretNotFound, ref)
		}
		if err != nil {
			return "", err
		}
		return aws.ToString(out.SecretString), nil
	})),
)
```

- `ref#field` reads one member of a JSON-object secret; each ref is fetched once per load, with the load's context.
- `,optional` keeps what the sources set when the fetch reports `ErrSecretNotFound` — a development file can supply what production keeps in the store.
- A failure fails the load with `ErrSecretProvider`, naming the key and the ref. A value that does not decode is reported by type, never quoted.
- Under a reloader the fetch runs on every reload, so a poll picks up rotations; a store outage fails that reload and keeps the previous config live.

`WithSecretPopulator` takes any `func(context.Context, any) error` when `SecretsFrom` does not fit.

## Post-load hooks

`WithPostLoad` runs after secrets and before validation — for normalization and derived values:

```go
config.WithPostLoad(func(_ context.Context, c *AppConfig) error {
	if c.DB.MaxConns == 0 {
		c.DB.MaxConns = 4 * runtime.NumCPU()
	}
	return nil
})
```

Do this work in a hook rather than after `Load` returns: inside the pipeline, a reloader validates the normalized config before it goes live.

## Validation

`validate` tags run on every load, and failures name config keys: `db.max_conns(min=1), port(max=65535)`. `errors.As` reaches the `validator.ValidationErrors`. Rules no tag can express go in a `Validate() error` method on the config, which runs after the tags, on the config the hooks produced:

```go
func (c *AppConfig) Validate() error {
	if c.TLS.Enabled && c.TLS.CertFile == "" {
		return errors.New("tls.cert_file is required when tls.enabled is set")
	}
	return nil
}
```

`WithValidator` replaces the tag validator with any `Struct(any) error` (one with `StructCtx` gets the load's context); register `config.FieldName` as its tag-name function to keep errors named by key. `WithSkipValidation` turns validation off; unknown keys still fail.

## Hot reload

```go
reloader, err := config.NewReloader[AppConfig](ctx,
	[]config.Option{
		config.WithFile("/etc/app/config.yaml"),
		config.WithEnvPrefix("APP"),
		config.WithLogger(logger),
	},
	config.WithFileWatch(0), // 0: the default 500ms debounce
	config.WithSignalReload(syscall.SIGHUP),
	config.WithOnReloadError(func(trigger string, err error) {
		reloadFailures.WithLabelValues(trigger).Inc()
	}),
)
if err != nil {
	return err // the initial load failed
}
if err := reloader.Arm(ctx); err != nil {
	return err // a trigger cannot be set up, e.g. an unwatchable directory
}
go func() {
	if err := reloader.Run(ctx); err != nil {
		logger.Error("config reloader ended", "error", err)
	}
}()
defer func() {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := reloader.Stop(stopCtx); err != nil {
		logger.Warn("config reloader did not stop in time", "error", err)
	}
}()

cfg := reloader.Config() // one atomic load; call it on every use
```

| Call | Does |
|---|---|
| `NewReloader`, `NewReloaderFunc` | Runs the first load; its error fails construction. |
| `Arm(ctx)` | Sets up the file watch and signals and returns their error, so a service can refuse to report ready. Optional: `Run` arms itself. |
| `Run(ctx)` | Serves every trigger until `Stop` (returns nil) or `ctx` ends. |
| `Reload(ctx)` | A manual reload, returning what it published and the keys that changed; `ErrReloadInProgress` if one is running. |
| `SourceChanged()` | Asks for a reload after a change the reloader cannot watch. Never blocks; bursts coalesce. |
| `Config()`, `Snapshot()`, `Stats()` | The live config; the config with its generation and reload history from one moment; the history alone. Lock-free. |
| `Stop(ctx)` | Stops the triggers, cancels an automatic reload and waits for a manual one. |

What a reloader guarantees:

- **Only a config that passed goes live.** A reload runs every source, secrets, hooks and validation before the swap. A failed reload keeps the previous config, and is reported to `WithOnReloadError`, the log and `Stats().LastError`, which carries its category.
- **No change is lost.** Reloads are serialized; an automatic trigger arriving during a reload waits and reads the sources again. Signal a change with `SourceChanged`, not by calling `Reload` from an event handler.
- **One moment, one snapshot.** The config, its generation (1 at boot, +1 per successful reload) and the stats are published together.
- **Configs are shared.** Every reader gets the same `*T`; treat it, maps and slices included, as immutable.

`WithOnReloadSuccess` callbacks run after each swap with the old and new configs and the changed keys. `NewReloaderFunc` gives a loader of your own the same lifecycle; name the files it reads with `WithWatchedFiles`. `ExampleNewReloader` shows a valid edit swapped in and an invalid one refused.

### Applying changes

`Config()` serves code that reads the config on every use. Code that captured a value at startup — a log level, a pool's limits, a router's routes — needs the change pushed to it. An applier owns keys by prefix and is called with the changes it owns after each published config:

```go
logLevel := new(slog.LevelVar)
reloader, err := config.NewReloader[AppConfig](ctx, loadOpts,
	config.WithFileWatch(0),
	config.WithApplier(config.Applier[AppConfig]{
		Name:     "logging",
		Prefixes: []string{"log.level"},
		Apply: func(ctx context.Context, ev config.ApplyEvent[AppConfig]) (config.ApplyResult, error) {
			logLevel.Set(parseLevel(ev.Desired.Log.Level))
			return config.ApplyResult{}, nil
		},
	}),
	config.WithApplier(config.Applier[AppConfig]{
		Name:     "database",
		Prefixes: []string{"db"},
		Apply: func(ctx context.Context, ev config.ApplyEvent[AppConfig]) (config.ApplyResult, error) {
			var restart []string
			for _, ch := range ev.Changes {
				if ch.Key == "db.host" {
					restart = append(restart, ch.Key) // a new host needs a new pool
				}
			}
			pool.SetMaxConns(ev.Desired.DB.MaxConns) // idempotent: a retry may repeat it
			return config.ApplyResult{RestartKeys: restart}, nil
		},
	}),
)
```

- **Ownership.** A prefix is a section (`db`), a value (`log.level`), a map entry (`features["audit"]`) or a pattern (`features[*].gate`); the most specific prefix owns a key.
- **Measured from what was applied.** An applier is called once per published config with the keys whose published value differs from what it last acknowledged. Returning nil acknowledges them all, except the `RestartKeys` it returns.
- **Failures are retried.** An error or a panic acknowledges nothing; the keys it was given are offered again, `Retry` set, until a call applies them. Appliers must be idempotent.
- **Structures need one owner.** A map entry or pointer section that appears or disappears applies live only through an applier that owns it and everything beneath it; otherwise it waits for a restart.
- **Everything else waits for a restart.** With appliers registered, `Snapshot().PendingRestart` lists every change no applier took — unowned keys, restart keys, blocked structures — until it is applied or reverted. Register a no-op applier for keys your code reads from `Config()` on every use.
- **Startup.** Set `ApplyInitial` to have an applier apply the first load before the constructor returns; otherwise build that state from `Config()` yourself. A subsystem that may not run registers its applier only when it runs.
- **Published is not applied.** A config an applier failed on stays live: `Reload` returns its result together with an `*ApplyError` (`ErrApply`). `Snapshot().Appliers` reports each applier's account, and `Snapshot().Applying` whether calls are still running.

Keeping the account is cheap: per applier, the reloader holds only the keys where what it applied differs from the published config, as copied values, and an applier that is up to date costs nothing.

### File watching and Kubernetes

`WithFileWatch` resolves each path the way the kernel does and watches every directory the resolution passes through, so it sees direct writes, rename-based saves, symlink swaps anywhere on the path — including Kubernetes ConfigMap and Secret volume updates, which swap a `..data` symlink rather than write the file — and directories removed or recreated. A periodic recheck catches what sends no event. It cannot see sources that are not files: poll them with `WithPollInterval` or call `SourceChanged`. A volume mounted with `subPath` never updates inside the container; mount it without.

### A reload endpoint

The package mounts no HTTP routes; authentication is yours. Every result type encodes to JSON as is, with the config itself left out:

```go
mux.HandleFunc("POST /admin/config/reload", func(w http.ResponseWriter, r *http.Request) {
	result, err := reloader.Reload(r.Context())
	switch {
	case errors.Is(err, config.ErrReloadInProgress):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case errors.Is(err, config.ErrReloaderStopped):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	case err != nil && !errors.Is(err, config.ErrApply):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result) // generation, stats, changes, applier outcomes
})
mux.HandleFunc("GET /admin/config/status", func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reloader.Snapshot())
})
```

## Inspecting a config

### Printing and usage

`PrintRedacted(cfg)` writes a config as a table, JSON or YAML; `RedactedMap(cfg)` returns key → value text for a status page or a log line. A value is redacted when it is tagged `sensitive` or `secret`, sits inside something that is, or sits under a name containing `password`, `secret`, `token`, `key`, `credential`, `auth`, `private`, `apikey`, `api_key` or `dsn`. Mark anything the names cannot catch — a credential in a field called `URL` — `sensitive:"true"`, and take a field the names catch wrongly — `max_tokens`, `auth_enabled`, `key_prefix` — out with `sensitive:"false"`, which overrides its name only: a key named like a credential beneath it, an enclosing `sensitive:"true"`, and a `secret` tag still redact, and a `sensitive` value other than `true` or `false` fails the load with `ErrSchema`. No option prints a secret.

`PrintUsage[T]("APP")` lists every setting an operator can set: key, variable, type, default, whether it is required, description. Defaults that could hold a secret are withheld.

```txt
KEY       ENV           TYPE           DEFAULT     DESCRIPTION
port      APP_PORT      int            8080        HTTP listen port
timeout   APP_TIMEOUT   time.Duration  30s         Request timeout
name      APP_NAME      string         (required)  Service name
password  APP_PASSWORD  string         [REDACTED]
```

### Diffs

`Diff(old, next)` returns every key whose value differs between two configs, comparing decoded values rather than text, with sensitive values redacted on both sides:

```txt
port: 8080 -> 9090
password: [REDACTED] -> [REDACTED]
tags[1]: + api
features["audit"].rate: 10 -> 20
features["export"]: added (features[*])
features["export"].rate: + 1
```

Lists compare by index and maps by key; a section that appears, disappears or turns nil is a `Structural` change followed by the keys beneath it. `ExampleDiff` is the program behind this output.

### Where a value came from

`LoadSnapshot` loads like `Load` and records each key's origin; a reloader built `WithProvenance()` records it on every load. `PrintExplain` prints it:

```txt
KEY          VALUE       SOURCE
---------------------------------------------------
port         9000        environment APP_PORT
log_level    INFO        tag_default → post_load 0
db.host      localhost   tag_default
db.password  [REDACTED]  environment APP_DB_PASSWORD
```

The input is the source the merge kept — a default, a file by path, the exact environment variable, a provider, or a CLI flag by name — followed by the stages that changed the value afterwards. Sources are named, never quoted. Recording costs about a third more time than `Load`, and nothing unless asked for.

### Config files from code

`PrintTemplate[T]("APP")` writes the config file the struct describes — every key with its default, description, type and variable; secrets and keys without a default commented out. Loading it gives exactly the defaults, so a shipped file cannot drift from the code:

```yaml
# HTTP listen port
# int; env APP_PORT
port: 8080
db:
  # string, required, sensitive; env APP_DB_PASSWORD
  # password:
```

`RedactedTree(cfg)` gives a loaded config as nested maps for any koanf parser to marshal — YAML, JSON, TOML, dotenv — and `PrintEnv(cfg, "APP")` as the `export` lines that would load it, secrets never exported.

### JSON Schema

`JSONSchema[T]()` returns a Draft 2020-12 schema of the config file — keys, types, descriptions, printable defaults and the validation rules JSON Schema can express — for editors and linters. It is advisory: `Load` is the authority, and a `required` key is not required in any one file, since a default or the environment may supply it.

## Errors

Every error wraps one category sentinel together with its cause, so `errors.Is` and `errors.As` reach both:

| Sentinel | Meaning |
|---|---|
| `ErrValidation` | Validation failed, a value has the wrong type, an unknown key (an `*UnknownKeysError` lists them), an ambiguous environment variable, or a field spelled two ways. |
| `ErrFileRead` | A required (or previously loaded) file is missing or unreadable. |
| `ErrFileParse` | The parser rejected a file; a `*ParseError` names the file and line. |
| `ErrSource` | Defaults or a provider failed to load. |
| `ErrSecretProvider` | The secret populator failed. |
| `ErrPostLoad` | A post-load hook failed. |
| `ErrReload` | A reload failed; the previous config is still live. Wraps one of the above. |
| `ErrApply` | A config was published but an applier did not apply it; an `*ApplyError` names which. |
| `ErrReloadInProgress` | A manual reload found another running. Not a failure. |
| `ErrReloaderStopped` | A reload was requested after `Stop`. |
| `ErrSchema`, `ErrKey` | The config type cannot be addressed, or a key does not parse — programming errors. |

`Diagnose(err)` turns any of them into a `Problem{Kind, Message}` — a bounded category for a metric label or a status endpoint.

The package's own errors never quote a value meant for a sensitive field: a decode failure on one is reported by field and type, and a parse failure by file and line, the parser's message — which can quote the file — kept only in `ParseError.Err`. Errors your own providers, hooks and validators return pass through unchanged, so keep values out of them.

## Observability

The package logs through `WithLogger` (`slog.Default()` otherwise) and imports no metrics library:

| Level | Message | Meaning |
|---|---|---|
| Info | `config reloaded` | A reload published a new config, with its generation and changed keys. |
| Error | `config reload failed` | A reload was refused; the previous config is live. |
| Error | `config applier failed` | An applier errored or panicked; it is retried on the next config. |
| Debug | `environment variables under the prefix name no config key` | Probably a typo in a variable name. |

`Snapshot()` is cheap and consistent, so export it with function metrics:

```go
reg.MustRegister(
	prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "config_reload_failures_total"},
		func() float64 { return float64(reloader.Stats().Failures) }),
	prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "config_generation"},
		func() float64 { return float64(reloader.Snapshot().Generation) }),
)
```

Alert on a rising failure count: a service whose reloads keep failing is running on stale config.

## Recipes

Complete programs, each with a test, in the [cookbook](examples/cookbook):

- [Kubernetes ConfigMap and Secret volumes](examples/cookbook/README.md#kubernetes-configmap-and-secret-volumes) — the file watch on a ConfigMap, Secret keys through `k8smount` and its watch.
- [Settings in a database](examples/cookbook/README.md#settings-in-a-database) — a table read on every reload through `NewMapProvider`.
- [AWS Secrets Manager](examples/cookbook/README.md#aws-secrets-manager) — `secret` tags resolved per environment.
- [HashiCorp Vault](examples/cookbook/README.md#hashicorp-vault) — a KV v2 secret as config keys.
- [A whole service](examples/cookbook/README.md#a-whole-service) — Kong flags, validation, hot reload, appliers, admin endpoints and graceful shutdown.

## CLI flags

[kongprovider](kongprovider) loads the [Kong](https://github.com/alecthomas/kong) flags a user typed as the highest-precedence source, mapped to keys by the config field each flag writes, and fills Kong's help from the config struct:

```go
var cli CLI // embeds AppConfig
kctx := kong.Parse(&cli, kongprovider.HelpFromConfig(&cli.AppConfig, "APP"))
cfg, err := config.Load[AppConfig](ctx,
	config.WithFile(cli.Config),
	config.WithEnvPrefix("APP"),
	kongprovider.WithOverridesFromStruct(kctx, &cli.AppConfig),
)
```

## Keeping an existing koanf loader

A service that already loads with koanf can take the reload lifecycle alone: hand its loader to `NewReloaderFunc` and name the files it reads.

```go
reloader, err := config.NewReloaderFunc(ctx, func(ctx context.Context) (*AppConfig, error) {
	k := koanf.New(".")
	if err := k.Load(file.Provider("/etc/app/config.yaml"), yaml.Parser()); err != nil {
		return nil, err
	}
	var cfg AppConfig
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}
	return &cfg, cfg.Validate() // return only a complete, valid config
}, config.WithWatchedFiles("/etc/app/config.yaml"), config.WithFileWatch(0))
```

## Testing

Load without touching the process environment or working directory: write files to `t.TempDir()` and pass `WithEnviron` a fixed list. A reloader under test needs no watcher — call `Reload` and assert on `Config()`:

```go
cfg, err := config.Load[AppConfig](t.Context(),
	config.WithFile(file),
	config.WithEnvPrefix("APP"),
	config.WithEnviron(func() []string { return []string{"APP_DB_PASSWORD=test"} }),
)
```

## Options

Load options:

| Option | Default | Description |
|---|---|---|
| `WithFile(path)`, `WithFiles(paths...)` | none | Optional config files, merged in order. |
| `WithRequiredFile(path)`, `WithRequiredFiles(paths...)` | none | Required config files. |
| `WithFileParser(suffix, p)` | `.yaml`, `.yml`: YAML; `.json`: JSON; others: YAML | The koanf parser for files whose names end in `suffix`; the longest matching suffix wins, `""` matches every file, and a nil parser removes one. |
| `WithEnvPrefix(prefix)` | environment not read | Read `PREFIX_*` variables. |
| `WithEnviron(fn)` | `os.Environ` | Where variables come from. |
| `WithDefaults(map)` | none | Defaults by dotted path, above `default` tags; repeated calls merge. |
| `WithBaseProvider(p, parser)` | none | A provider below the files. |
| `WithOverrideProvider(p, parser)` | none | A provider above the environment. |
| `WithDecodeHook(h)` | none | A mapstructure decode hook, run before the built-in ones. |
| `WithUnknownKeys(fn)` | unknown keys fail, unknown variables ignored | Decide what unknown keys and variables do: report, accept, or fail. |
| `WithAllowUnknownKeys()` | unknown keys fail | Accept keys no field decodes, silently. |
| `WithSecretPopulator(fn)` | none | Resolve secrets; see `SecretsFrom`. |
| `WithPostLoad(fn)` | none | Normalize after secrets, before validation. |
| `WithValidator(v)` | go-playground, keys as field names | Replace the tag validator. |
| `WithSkipValidation()` | validation on | Skip tag validation and `Validate`. |
| `WithLogger(l)` | `slog.Default()` | Load diagnostics and reload events. |

Reload options:

| Option | Default | Description |
|---|---|---|
| `WithFileWatch(debounce)` | off; `0` means 500ms | Reload when a config file changes. |
| `WithWatchedFiles(paths...)` | none | More files to watch; for `NewReloaderFunc`, the only ones. |
| `WithPollInterval(d)` | off | Reload every `d`. |
| `WithSignalReload(sigs...)` | off | Reload on a signal, such as `SIGHUP`. |
| `WithApplier(a)` | none | Push owned changes into the process; see [Applying changes](#applying-changes). |
| `WithOnReloadSuccess(fn)` | none | Called after each successful reload. |
| `WithOnReloadError(fn)` | none | Called after each failed reload. |
| `WithProvenance()` | off | Record each value's origin on every load. |
| `WithReloadLogger(l)` | the load's logger | Logger for reload events. |

Print options: `WithFormat` (table, JSON, YAML, env — per printer), `WithWriter` (default `os.Stdout`), `WithSortByName`, `WithIncludeDescriptions`.

## License

[MIT](../LICENSE)
