# Inspecting a config

[Documentation](README.md) · [Quick start](../README.md#quick-start) · [Go API](https://pkg.go.dev/github.com/ashtonian/pkg/config)

- [Fields and struct tags](#fields-and-struct-tags)
- [Printing and usage](#printing-and-usage)
- [Diffs](#diffs)
- [Where a value came from](#where-a-value-came-from)
- [Config files from code](#config-files-from-code)
- [JSON Schema](#json-schema)

## Fields and struct tags

Use `FieldsChecked[T]()` when building an integration that needs to describe
settings. It returns `([]Field, error)` in declaration order. For example:

```go
type Settings struct {
    Port int `koanf:"port" default:"8080" desc:"HTTP port"`
}
fields, err := config.FieldsChecked[Settings]()
```

On success, the first result has `Path: "port"`, `Type: int`, `Index: []int{0}`,
and the original struct tags. `fields[0].Tag.Lookup("default")` returns
`"8080", true`. Inspecting those tags does not apply the default or call a
custom decoder. Fields and index slices are caller-owned copies of cached
metadata, so changing the result does not change future calls.

Use `FieldFor[T]("server.port")` to describe one key. `KeyOf(&cfg, &cfg.Port)`
finds the key for an actual field by its address. Both inspect metadata without
decoding default/example tags.

For several reflected targets, such as CLI flag destinations, use
`MatchFields(&cfg, targets)`. It walks the config instance once and returns a
`[]Field` in target order. An empty `Path` means that target is not a config
field. Targets outside cfg, skipped fields and collection elements do not
match. Ambiguous addresses return `ErrKey`, and an invalid schema returns
`ErrSchema`. Nil ancestors are never allocated. Matching describes the current
instance: call again after replacing pointers or targets. No addresses are
stored in the global metadata cache.

## Printing and usage

`PrintRedacted(cfg)` writes a config as a table, JSON or YAML; `RedactedMapChecked(cfg)` returns `(map[string]string, error)` for a status page or log line, with text values keyed by path. A value is redacted when it is tagged `sensitive` or `secret`, sits inside something that is, or sits under a name containing `password`, `secret`, `token`, `key`, `credential`, `auth`, `private`, `apikey`, `api_key` or `dsn`. Mark anything the names cannot catch — a credential in a field called `URL` — `sensitive:"true"`, and take a field the names catch wrongly — `max_tokens`, `auth_enabled`, `key_prefix` — out with `sensitive:"false"`, which overrides its name only: a key named like a credential beneath it, an enclosing `sensitive:"true"`, and a `secret` tag still redact, and a `sensitive` value other than `true` or `false` fails the load with `ErrSchema`. No option prints a secret.

`PrintUsage[T]("APP")` lists every setting an operator can set — a collection one variable can set as a whole, and its entry patterns — with key, variable, type, default, whether it is required, description. JSON rows keep the primary variable in `env`, extra names in `also_env`, and a printable sample in `example`. Export templates comment every line of a sample. Defaults that could hold a secret are withheld.

```txt
KEY       ENV           TYPE           DEFAULT     DESCRIPTION
port      APP_PORT      int            8080        HTTP listen port
timeout   APP_TIMEOUT   time.Duration  30s         Request timeout
name      APP_NAME      string         (required)  Service name
password  APP_PASSWORD  string         [REDACTED]
```

## Diffs

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

## Where a value came from

`LoadSnapshot` loads like `Load` and records each key's origin; a reloader built `WithProvenance()` records it on every load. `PrintExplain` prints it:

```txt
KEY          VALUE       SOURCE
---------------------------------------------------
port         9000        environment APP_PORT
log_level    INFO        tag_default → post_load 0
db.host      localhost   tag_default
db.password  [REDACTED]  environment APP_DB_PASSWORD
```

The input is the source the merge kept — a default, a file by path, the exact environment variable, a provider, or a CLI flag by name — followed by the stages that changed the value afterwards. Scalar list and array elements have individual rows with their values; sensitive containers remain redacted. Sources are named, never quoted. Recording costs about a third more time than `Load`, and nothing unless asked for.

## Config files from code

`PrintTemplate[T]("APP")` writes the config file the struct describes — every key with its default, description, type and variable; secrets and keys without a default commented out. Loading it gives exactly the defaults, so a shipped file cannot drift from the code:

```yaml
# HTTP listen port
# int; env APP_PORT
port: 8080
db:
  # string, required, sensitive; env APP_DB_PASSWORD
  # password:
```

`RedactedTreeChecked(cfg)` returns `(map[string]any, error)`, giving a loaded config as nested maps for any koanf parser to marshal — YAML, JSON, TOML, dotenv — and `PrintEnv(cfg, "APP")` as the `export` lines that would load it, using each list field's `sep` (commas by default). Lists that cannot round-trip, including one empty string or multiple elements with `sep:"none"`, are commented out; secrets are never exported.

## JSON Schema

`JSONSchema[T]()` returns a Draft 2020-12 schema of the config file — keys, types, descriptions, printable defaults and the validation rules JSON Schema can express — for editors and linters. It is advisory: `Load` is the authority, and a `required` key is not required in any one file, since a default or the environment may supply it.

## Inspection errors

`FieldsChecked[T]()` returns `([]Field, error)` and reports invalid or ambiguous
keys with `ErrSchema`. For example, fields tagged `koanf:"Port"` and
`koanf:"PORT"` conflict because matching ignores case. `Fields[T]()` keeps its
slice-only signature and returns nil for an invalid schema; use the checked
form when building integrations. Loading uses the checked catalog; the Kong bridge validates metadata while
matching its flag targets in one traversal.

`RedactedMapChecked(cfg)` returns `(map[string]string, error)`;
`RedactedTreeChecked(cfg)` returns `(map[string]any, error)`. Both return nil
and `ErrSchema` if a reached type has invalid keys, including dynamic values
inside interfaces. They never return partial results. The original
`RedactedMap` and `RedactedTree` return nil on that error. `PrintRedacted`
reports it before writing any output.

These inspection functions read field metadata without decoding `default`
or `example` tags. Printing a live value therefore does not invoke a custom
unmarshaler for its examples. Invalid sensitivity tags still redact
conservatively. Loading, templates and schema generation validate their tags;
templates and schema generation also decode the values they show.
