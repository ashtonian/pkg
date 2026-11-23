# Reference and operations

[Documentation](README.md) · [Quick start](../README.md#quick-start) · [Go API](https://pkg.go.dev/github.com/ashtonian/pkg/config)

- [Errors](#errors)
- [Checked inspection](#checked-inspection)
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
| `ErrApply` | An applier did not apply a config; an `*ApplyError` names which. After a reload the config was published; on a reloader's first load (`Initial`) nothing was. |
| `ErrReloadInProgress` | A manual reload found another running. Not a failure. |
| `ErrReloaderStopped` | A reload was requested after `Stop`. |
| `ErrReloaderRunning` | `Run` was called while another `Run` of the same reloader runs. |
| `ErrSchema`, `ErrKey` | Invalid schema/default declarations, or a malformed/unknown key. A `validate` tag the default validator cannot parse, or whose `dive` its field's type cannot take, is a schema error naming the field: on a field the validator reads on every validation it is refused before any source is read, even while the field is empty or nil; elsewhere, and for a rule that parses but cannot apply to its field's type, when validation reaches it, at the field's key (`at items[0].list`). Fix the tag ([validation](schema.md#validation)). `KeyError` distinguishes unknown keys from invalid values in `CheckKey`, `DecodeKey` and `CanonicalKey`; `EditsError` is a batch `DecodeEdits` refuses: it wraps each refused `*KeyError`, which keeps its own category (an unknown key `ErrKey`, any other refusal `ErrValidation`, as `DecodeKey` gives it), and `Err`, the cause of a candidate that does not decode or validate (`ErrValidation`). |

`Diagnose(err)` turns any of them into a `Problem{Kind, Message}` — a bounded category for a metric label or a status endpoint.

The package's own errors never quote a value meant for a sensitive field. A decode failure on one is reported by field and type; a validation failure on one omits the rule's operand (`eq=...`); a parse failure is reported by file and line, the parser's message — which can quote the file — kept only in `ParseError.Err`; and type names leave out struct tags, which can hold a default or an example. Errors your own providers, hooks and validators return pass through unchanged, so keep values out of them.

## Checked inspection

Use these forms when callers need to distinguish an empty result from an invalid
schema. Schema errors support `errors.Is(err, config.ErrSchema)`; malformed keys wrap `ErrKey`.

| Function | Returns | Failure behavior |
|---|---|---|
| `FieldsChecked[T]()` | `([]Field, error)` in declaration order | Rejects a non-struct root and invalid or ambiguous keys |
| `MatchFields(&cfg, targets)` | `([]Field, error)` in target order | Matches addresses in one traversal; empty Path means no match; ambiguous addresses are ErrKey |
| `RedactedMapChecked(cfg)` | `(map[string]string, error)` with text values keyed by path | Returns nil on structural errors and misspelt tag keys, including reached dynamic types |
| `Usage[T](prefixes...)` | `([]UsageEntry, error)` with safe printable metadata | Reports the schema errors a load reports; keeps raw struct tags only in the non-JSON Field member |
| `ParseKey(text)` | `(Key, error)` | Reports malformed keys with ErrKey |
| `CanonicalKey[T](key)` | `(string, error)`: the key as this package prints it, aliases, case and map-key quoting resolved | An Unknown `*KeyError` (ErrKey) for a key that does not parse, is not concrete or names nothing T reads; `ErrSchema` for a declaration a load refuses, its `validate` tags aside |
| `Key.Within(p)` | `bool`: the key is `p` or beneath it | — |
| `Tree(cfg)`, `TreeFunc(cfg, leaf)` | `(map[string]any, error)` with raw or policy-transformed values | Returns no partial tree on schema/scalar/text-marshaling errors |
| `RedactedTreeChecked(cfg)` | `(map[string]any, error)` with nested redacted values | Returns nil on structural errors and misspelt tag keys; no partial tree |

Each function checks the declarations its result depends on — see
[declaration checks](inspection.md#declaration-checks). The redacting and raw
forms inspect metadata without decoding default or example tags; `Usage`
validates tags as a load does and decodes defaults when a `Defaulter` sets
some.
The original `Fields`, `RedactedMap` and `RedactedTree` retain their single-result
signatures and return nil on failure. `PrintRedacted` returns structural errors
before writing output. See [inspection errors](inspection.md#inspection-errors)
for examples and tag-validation boundaries.

## Observability

The package logs through `WithLogger` (`slog.Default()` otherwise) and imports no metrics library:

| Level | Message | Meaning |
|---|---|---|
| Info | `config reloaded` | A reload published a new config, with its generation and changed keys. |
| Error | `config reload failed` | A reload was refused; the previous config is live. |
| Error | `config applier failed` | An applier errored or panicked; it is retried on the next config. |
| Warn | `config keys set under a former name; rename them` | A source used a field alias. |
| Warn | `config input changed beneath a higher-precedence source; the change does not take effect` | A reload read an edit — a key set, changed or removed — that a higher source keeps from taking effect: `key`, the edited `source`, and `by`, the source kept. Remove `by`'s variable or flag, or change the value there. See [shadowed inputs](sources.md#shadowed-inputs). |
| Debug | `config inputs a higher-precedence source outranks` | Every shadowed input of a load that looks for them (`LoadSnapshot`, a reloader, a `WithShadowedInputs` handler). |
| Debug | `environment variables under the prefix name no config key` | Probably a typo in a variable name. Under a shared `WithEnvPrefixes` prefix it may be another service's variable: claim those with `WithVariableFilter`. |

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

Load and reload options copy their arguments when they are called — paths,
names, prefixes, signals and `WithDefaults` values — so a caller reusing a
slice or map afterwards changes nothing a load or a `Reloader` reads or
watches. Providers, `WithEnviron` and callbacks are read live on every load.

Load options:

| Option | Default | Description |
|---|---|---|
| `WithFile(path)`, `WithFiles(paths...)` | none | Optional config files, merged in order. |
| `WithRequiredFile(path)`, `WithRequiredFiles(paths...)` | none | Required config files. |
| `WithFileParser(suffix, p)` | `.yaml`, `.yml`: YAML; `.json`: JSON; others: YAML | The koanf parser for files whose names end in `suffix`; the longest matching suffix wins, `""` matches every file, and a nil parser removes one. |
| `WithEnvPrefix(prefix)` | environment not read | Read `PREFIX_*` variables; `WithEnvPrefixes(prefix)`. |
| `WithEnvPrefixes(prefixes...)` | environment not read | Read each prefix's variables as one layer, most specific first; for each key the first prefix that sets it wins, then its `env` tag. Empty and repeated prefixes are left out; last call wins. |
| `WithEnviron(fn)` | `os.Environ` | Where variables come from. |
| `WithKnownVariables(names...)` | none | Claim exact names read by the application; repeated calls accumulate. |
| `WithVariableFilter(fn)` | none | Claim otherwise unmatched names when fn returns true; last call wins, nil clears. |
| `WithDefaults(map)` | none | Defaults by dotted path, above the code defaults (`SetDefaults`) and tags; repeated calls merge. |
| `WithBaseProvider(p, parser, opts...)` | none | A provider below the files. |
| `WithOverrideProvider(p, parser, opts...)` | none | A provider above the environment. |
| `WithBaseEntries(read, opts...)`, `WithOverrideEntries(read, opts...)` | none | Stored entries keyed in the key grammar, read with the load's context on every load, among the base or override providers in the order added. |
| `WithDecodeHook(h)` | none | A mapstructure decode hook, run before the built-in ones. |
| `WithUnknownKeys(fn)` | unknown keys fail, unknown variables ignored | Decide what unknown keys and variables do: report, accept, or fail. |
| `WithAllowUnknownKeys()` | unknown keys fail | Accept keys no field decodes, silently. |
| `WithShadowedInputs(fn)` | listed in snapshots, never fail | Decide what inputs a higher-precedence source outranks do: accept, report, or fail with `ErrValidation`. |
| `WithSecretPopulator(fn)` | none | Resolve secrets; see `SecretsFrom`. |
| `WithPostLoad(fn)` | none | Normalize after secrets, before validation. |
| `WithValidator(v)` | go-playground, keys as field names | Replace the tag validator. |
| `WithSkipValidation()` | validation on | Skip tag validation and `Validate`. |
| `WithLogger(l)` | `slog.Default()` | Load diagnostics and reload events. |

Provider source options:

| Option | Default | Description |
|---|---|---|
| `WithInvalidEntries(report)` | fail on invalid entries | Withhold each entry that fails at a key it sets in the configuration every source makes, beside the entries it does not fail at; decode failures and field rules only, before secrets, hooks and `Validate`. A failure at no key or under a higher source not using the option withholds nothing. At most 2k+1 judgements for k entries blamed. Retain lower sources; report sorted keys/errors on every load, once every source is read. Unknown keys remain reportable. |
| `WithUnknownEntries(report)` | the load's handler; unknown keys fail | Send the keys this source sets that no field reads to report instead of `WithUnknownKeys`: nil goes on, an error fails with `ErrValidation`, a nil report accepts them. A key is claimed only when every source that set any part of it uses the option (overridden ones included) and no default is there; the highest of them reports it. Otherwise it stays the load's. |

`Defaulter`, `CheckKey`, `DecodeKey`, `CanonicalKey`, `DecodeEdits` and the
entry options are described in
[sources and precedence](sources.md#validating-stored-entries). `RedactText` uses the diagnostic
[credential-shape policy](inspection.md#printing-and-usage).

Reload options:

| Option | Default | Description |
|---|---|---|
| `WithFileWatch(debounce)` | off; `0` means 500ms | Reload when a config file changes. |
| `WithWatchedFiles(paths...)` | none | More files to watch; for `NewReloaderFunc`, the only ones. |
| `WithPollInterval(d)` | off | Reload every `d`. |
| `WithSignalReload(sigs...)` | off | Reload on a signal, such as `SIGHUP`. |
| `WithApplier(a)` | none | Push owned changes into the process; see [Applying changes](reloading.md#applying-changes). |
| `WithReloadTags()` | off | Route by inherited reload classes; restart changes wait even beneath a broad applier. Untagged schemas retain prefix routing. |
| `WithReloadTagChecks()` | off | Enable class routing and require live-key coverage and a live key per applier before loading. |
| `WithOnReloadSuccess(fn)` | none | Called after each successful reload. |
| `WithOnReloadError(fn)` | none | Called after each failed reload. |
| `WithProvenance()` | off | Record each value's origin on every load. |
| `WithReloadLogger(l)` | the load's logger | Logger for reload events. |

Print options: `WithFormat` (table, JSON, YAML, Markdown, env — per printer; Markdown is supported by `PrintUsage`), `WithWriter` (default `os.Stdout`), `WithSortByName`, `WithIncludeDescriptions`, `WithFallbackEnvPrefixes(prefixes...)` (default none: the broader prefixes of a `WithEnvPrefixes` load, read beneath the one `PrintUsage`, `PrintTemplate` and `PrintEnv` are given), `WithUsageColumn(title, value)` (default none: a column of `PrintUsage`'s table and Markdown after the variables, each cell `value(entry)`; repeated calls add columns in order).
