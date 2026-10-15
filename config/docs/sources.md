# Sources and precedence

[Documentation](README.md) · [Quick start](../README.md#quick-start) · [Go API](https://pkg.go.dev/github.com/ashtonian/pkg/config)

- [Any koanf provider](#any-koanf-provider)
- [Environment variables](#environment-variables)

Highest wins:

| | Option | Use |
|---|---|---|
| 1 | `WithOverrideProvider(p, parser)` | Operator overrides, such as CLI flags through [kongprovider](../kongprovider). |
| 2 | `WithEnvPrefix(prefix)` | `PREFIX_*` environment variables. |
| 3 | `WithFile`, `WithRequiredFile` | Config files, merged in order, each parsed by its name's suffix: YAML or JSON built in, any koanf parser through `WithFileParser`. |
| 4 | `WithBaseProvider(p, parser)` | A shared or remote base the files refine. |
| 5 | `WithDefaults(map)` | Programmatic defaults keyed by dotted path. |
| 6 | `default` tags | Always. |

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

## Environment variables

With `WithEnvPrefix("APP")`, a key's variable is the prefix and its path, upper-cased, `.` becoming `_`. Names are matched against the key tree rather than split, so underscores inside keys survive:

| Variable | Key |
|---|---|
| `APP_DB_MAX_CONNS` | `db.max_conns` |
| `APP_LABELS_TEAM_NAME` (`map[string]string`) | `labels["team_name"]` |
| `APP_FEATURES_AUDIT_LOGS_MAX_PAGE_SIZE` (`map[string]Limits`) | `features["audit_logs"].max_page_size` |
| `APP_TAGS=web,api` (`[]string`) | `tags` = `[web api]` |
| `APP_LABELS=team=core,tier=1` (`map[string]string`) | `labels`, the whole map |

An `env` tag adds variables a key reads as written, without the prefix, for names a platform or another tool sets: `Port int koanf:"port" env:"PORT"` reads `APP_PORT`, then `PORT`. The prefixed variable wins, then the first set in the tag's list. Tagged variables are read only when `WithEnvPrefix` turns the environment on; a tag on a section, on a field of a list element or map entry, or naming a variable another field names fails with `ErrSchema`.

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
