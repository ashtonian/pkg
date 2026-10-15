# Reference and operations

[Documentation](README.md) · [Quick start](../README.md#quick-start) · [Go API](https://pkg.go.dev/github.com/ashtonian/pkg/config)

- [Errors](#errors)
- [Observability](#observability)
- [Testing](#testing)
- [Options](#options)

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

The package's own errors never quote a value meant for a sensitive field. A decode failure on one is reported by field and type; a validation failure on one omits the rule's operand (`eq=...`); a parse failure is reported by file and line, the parser's message — which can quote the file — kept only in `ParseError.Err`; and type names leave out struct tags, which can hold a default or an example. Errors your own providers, hooks and validators return pass through unchanged, so keep values out of them.

## Observability

The package logs through `WithLogger` (`slog.Default()` otherwise) and imports no metrics library:

| Level | Message | Meaning |
|---|---|---|
| Info | `config reloaded` | A reload published a new config, with its generation and changed keys. |
| Error | `config reload failed` | A reload was refused; the previous config is live. |
| Error | `config applier failed` | An applier errored or panicked; it is retried on the next config. |
| Warn | `config keys set under a former name; rename them` | A source used a field alias. |
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
| `WithApplier(a)` | none | Push owned changes into the process; see [Applying changes](reloading.md#applying-changes). |
| `WithOnReloadSuccess(fn)` | none | Called after each successful reload. |
| `WithOnReloadError(fn)` | none | Called after each failed reload. |
| `WithProvenance()` | off | Record each value's origin on every load. |
| `WithReloadLogger(l)` | the load's logger | Logger for reload events. |

Print options: `WithFormat` (table, JSON, YAML, env — per printer), `WithWriter` (default `os.Stdout`), `WithSortByName`, `WithIncludeDescriptions`.
