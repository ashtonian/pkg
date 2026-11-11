# Sources and precedence

[Documentation](README.md) · [Quick start](../README.md#quick-start) · [Go API](https://pkg.go.dev/github.com/ashtonian/pkg/config)

- [Any koanf provider](#any-koanf-provider)
- [Environment variables](#environment-variables)
  - [Layered prefixes](#layered-prefixes)

Highest wins:

| | Option | Use |
|---|---|---|
| 1 | `WithOverrideProvider(p, parser)` | Operator overrides, such as CLI flags through [kongprovider](../kongprovider). |
| 2 | `WithEnvPrefix(prefix)`, `WithEnvPrefixes(prefixes...)` | `PREFIX_*` environment variables, under one prefix or several. |
| 3 | `WithFile`, `WithRequiredFile` | Config files, merged in order, each parsed by its name's suffix: YAML or JSON built in, any koanf parser through `WithFileParser`. |
| 4 | `WithBaseProvider(p, parser)` | A shared or remote base the files refine. |
| 5 | `WithDefaults(map)` | Programmatic defaults keyed by dotted path. |
| 6 | `DefaultProvider.ConfigDefaults()` | Defaults belonging to the root schema. |
| 7 | `default` tags | Always. |

Each file takes the parser registered for the longest suffix of its name, ignoring case: `.yaml` and `.yml` are YAML, `.json` is JSON, and any other file is YAML. `WithFileParser(".toml", toml.Parser())` adds a format, so `config.yaml`, `overrides.json` and `local.toml` load together; `WithFileParser("", p)` changes the parser for every file no other suffix matches. JSON is strict: one object per file, integers within int64/uint64 kept exact (larger integer literals fail), a key set twice in one object an error.

To migrate the old single-parser API, register the parser for `""` and remove the built-in suffix registrations with `WithFileParser(".yaml", nil)`, `WithFileParser(".yml", nil)` and `WithFileParser(".json", nil)`. Setting only `""` leaves those registrations in force.

An optional file may be absent, but one that exists and cannot be read or parsed fails the load. Under a reloader, a file that loaded once stays required, so deleting it fails the reload rather than silently reverting its keys.

Keys match fields in any letter case, as decoding does, and each source's keys are respelled as the fields are before the merge — so precedence holds whatever case a source used. A source that spells one field two ways (`port` and `Port`) fails the load.

## Any koanf provider

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

## Schema-owned defaults

A root config may implement `ConfigDefaults() map[string]any` (the
`DefaultProvider` interface). Its keys are dot paths, as for `WithDefaults`.
For shared types with different defaults at different paths:

```go
func (*Config) ConfigDefaults() map[string]any {
    return map[string]any{
        "read_http.retry.attempts": 3,
        "write_http.retry.attempts": 1,
    }
}
```

`Load`, `Usage`, `JSONSchema` and `PrintTemplate` use the same declarations.
They override tags and remain below caller `WithDefaults`, providers, files
and environment. Provenance names this layer `schema_defaults`. A zero value,
empty string or explicit null is a declared default, not an absent entry.

The method runs on a fresh zero root and must return deterministic data
without I/O or global mutation. Returned maps, structs and collections are
copied. Unknown keys and values the built-in decoder cannot decode fail with
`ErrSchema`, even if a later source would replace them. A section and a dot
path beneath it cannot both appear in the map. Only the root method runs;
collection elements retain their existing tag-default behavior. Caller
`WithDefaults` still supplies runtime profiles that static documentation
cannot know.

## Validating stored entries

`CheckKey[Config](ctx, "server.port", "8080")` decodes one key against defaults
and validates it with the same hooks, tag validator and `Validate` method as
`Load`. `DecodeKey` returns the resulting `*Config` when a caller also needs
its typed values. A rejected key returns `*KeyError`: `Unknown` and `ErrKey`
distinguish an unknown key; a known key with an invalid value matches
`ErrValidation`. Schema/setup errors and cancellation return directly.
No partial config is returned on failure. Error messages contain the key,
not the value; wrapped causes are for trusted diagnostics.

These calls honor `WithDefaults`, `WithDecodeHook`, `WithValidator` and
`WithSkipValidation`. They read no provider, file or environment and run no
secret, post-load or unknown-input callbacks. Other fields remain at defaults,
so cross-field rules judge that context. Keys use `ParseKey` syntax, with
aliases and case folding accepted for fields. List indexes must already
exist in defaults or append immediately after them; pass the whole list to
create several elements. Wildcards and sparse expansion are refused.

For a store whose older rows may be invalid, pass a per-source option:

```go
config.WithBaseProvider(store, nil, config.WithInvalidEntries(
    func(ctx context.Context, rejected []config.KeyError) error {
        return reportRejected(ctx, rejected)
    },
))
```

This checks each known entry against defaults and withholds invalid entries,
leaving their lower-precedence values intact. Struct sections are checked
field by field; collections and self-decoding values are atomic. The callback
receives sorted key/error pairs after every read, including an empty report;
nil discards reports. Its error fails the load with `ErrSource`.

Defaults must validate before any provider is read. Unknown keys still reach
the normal diagnostics, including declared unknown children of a withheld
collection. Read/parser errors and cancellation fail the load. Finally, the
combined config must validate: independently valid rows can conflict with one
another, and the loader does not choose arbitrary rows to discard to resolve
such a conflict. The option applies equally to `WithOverrideProvider`.

## Environment variables

With `WithEnvPrefix("APP")`, a key's variable is the prefix and its path, upper-cased, `.` becoming `_`. Names are matched against the key tree rather than split, so underscores inside keys survive:

| Variable | Key |
|---|---|
| `APP_DB_MAX_CONNS` | `db.max_conns` |
| `APP_LABELS_TEAM_NAME` (`map[string]string`) | `labels["team_name"]` |
| `APP_FEATURES_AUDIT_LOGS_MAX_PAGE_SIZE` (`map[string]Limits`) | `features["audit_logs"].max_page_size` |
| `APP_TAGS=web,api` (`[]string`) | `tags` = `[web api]` |
| `APP_LABELS=team=core,tier=1` (`map[string]string`) | `labels`, the whole map |

An `env` tag adds variables a key reads as written, without the prefix, for names a platform or another tool sets: `Port int koanf:"port" env:"PORT"` reads `APP_PORT`, then `PORT`. Every prefixed variable wins, then the first set in the tag's list. Tagged variables are read only when `WithEnvPrefix` turns the environment on; a tag on a section, on a field of a list element or map entry, or naming a variable another field names fails with `ErrSchema`.

Setting a whole map and an entry beneath it in the same environment layer fails with `ErrValidation`, naming both variables; choose one form.

A field tagged `format:"json"` takes its whole value from one variable, as JSON — a list of structs, a map of structs, a section:

```go
Routes []Route `koanf:"routes" format:"json"`
```

```bash
APP_ROUTES='[{"path":"/api","timeout":"10s"},{"path":"/health"}]'
```

The JSON is parsed before decoding, so each element's keys are checked as strict keys, take aliases, and get the element struct's defaults; `Diff` and provenance see every element. Text that is not JSON fails with `ErrValidation`, naming the key and line but not the text. A section's variable and one for a key inside it cannot both be set. The same tag lets a file give the value as a JSON string, and a `default` hold it as JSON.

A variable that two keys could read — `db.port` and `db_port`, a field and a map entry, or a tagged name that is another key's own variable — fails the load naming every reading, instead of guessing. A variable under the prefix that names no key is ignored and logged at debug level — other programs may share the prefix — unless a `WithUnknownKeys` handler is set, which sees it with the variable it most likely misspells. `WithEnviron(fn)` replaces `os.Environ`, which keeps tests independent of the machine. `PrintUsage` lists every variable a config reads.

### Layered prefixes

A binary that runs several services as subcommands reads one shared namespace and, above it, each service's own. `WithEnvPrefixes` takes the prefixes most specific first; for every key, the first prefix whose variable is set wins, then the names its `env` tag lists:

```go
config.WithEnvPrefixes("APP_WORKER", "APP")
```

| Set | `db.host` | `db.port` |
|---|---|---|
| `APP_DB_HOST=shared`, `APP_DB_PORT=5432` | `shared` | `5432` |
| … and `APP_WORKER_DB_HOST=worker` | `worker` | `5432` |

The prefixes are one layer, between the files and the override providers, with every rule above. A trailing `_` is optional; empty and repeated prefixes are left out, so `WithEnvPrefixes(servicePrefix, "APP")` with an empty `servicePrefix` reads `APP` alone. `WithEnvPrefix(p)` is `WithEnvPrefixes(p)`.

- **Ambiguity spans prefixes.** A name that reads one key under one prefix and another key under a second fails the load naming both: with a `worker` section in the struct, `APP_WORKER_DB_HOST` is `db.host` under `APP_WORKER` and `worker.db.host` under `APP`. Rename the field or choose another prefix.
- **A value and keys beneath it.** Map entries and section keys merge across prefixes key by key, the narrower prefix winning on a shared key, and a whole map or `format:"json"` section under the narrower prefix replaces the one under the broader. Setting a whole map or section under one prefix and a key beneath it under another fails with `ErrValidation` naming both variables, as it does under one prefix: no merge of the two is the one an operator obviously meant.
- **Unknown variables.** A variable under a broad prefix that a narrower prefix's key reads (`APP_WORKER_DB_HOST`, seen from `APP`) is not unknown. One under the shared prefix that only another service reads is: claim those with `WithVariableFilter`, so a strict `WithUnknownKeys` handler still reports a misspelling with the variable it most likely meant:

```go
config.WithVariableFilter(func(name string) bool {
	return strings.HasPrefix(name, "APP_API_") // the api service's
})
```

- **Provenance** names the variable and, in `Source.Prefix`, the prefix it was read under; an `env` tag's name has none.
- **Diagnostics.** `Field.EnvVars`, `Usage` and `JSONSchema` take the prefix list; `PrintUsage`, `PrintTemplate` and `PrintEnv` take its first prefix and `WithFallbackEnvPrefixes` the rest. Each names every variable in precedence order; `PrintEnv` and the `FormatEnv` template write the first prefix's.
- **Reloading** reads every prefix again on each reload.

### Variables read by the application

Mark a field that names other variables with `envref:"true"`:

```go
PasswordEnv string   `koanf:"password_env" envref:"true"`
FallbackEnv []string `koanf:"fallback_env" envref:"true"`
```

After merging and decoding, the effective names are excluded from unknown
variable diagnostics. A higher-precedence reference replaces its default;
reference fields inside nested structs, maps and lists work the same way.
This runs before secret resolution and post-load hooks. It does not read the
referenced values or assign them to another field: the application does that.
The `env` tag retains its separate meaning of alternate input variable names.

`WithKnownVariables("APP_RUNTIME_MODE")` claims exact, case-sensitive names
read elsewhere; repeated calls accumulate. `WithVariableFilter(fn)` claims
names for which `fn(name)` returns true; the predicate receives no values,
the last option wins, and nil clears it. Claims affect otherwise unmatched
variables only. Ordinary field decoding, ambiguity detection and unknown
config-key checks remain in force. Unclaimed names still reach the
`WithUnknownKeys` handler, or debug logging when no handler is installed.
