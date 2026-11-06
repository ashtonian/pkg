# Inspecting a config

[Documentation](README.md) · [Quick start](../README.md#quick-start) · [Go API](https://pkg.go.dev/github.com/ashtonian/pkg/config)

- [Fields and struct tags](#fields-and-struct-tags)
- [Printing and usage](#printing-and-usage)
- [Canonical keys](#canonical-keys)
- [Raw and policy-controlled trees](#raw-and-policy-controlled-trees)
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

`PrintRedacted(cfg)` writes a config as a table, JSON or YAML;
`RedactedMapChecked(cfg)` returns text values keyed by path. Explicit
`sensitive`/`secret` tags and sensitive parents protect their values.
Credential-like names also redact text-capable types, including strings,
bytes, maps, interfaces and text decoders. Numeric counters, booleans and
durations do not redact from their names alone. `sensitive:"false"` exempts
a text field such as `key_prefix` from name inference; it cannot override
an enclosing sensitive tag or a `secret` tag.

All redacted renderers also apply `RedactText` to text and byte values. It
removes recognizable URL credentials, PEM blocks, credential key/value pairs,
Basic/Bearer credentials and JWTs while keeping non-secret context. A URL
with a password keeps its username, host and path; userinfo with no password
is withheld because it may itself be a token. `RedactText` exposes the same
policy for environment listings or logs. Shape matching is not a complete
secret detector; retain explicit sensitivity declarations. Raw `Tree` and
`TreeFunc` do not apply it.

`Usage[T]("APP")` returns `([]UsageEntry, error)` in declaration order.
It describes whole collection inputs and entry patterns, with environment
names, type, description, required state, safe defaults/examples, allowed
values, bounds, former key names and resolved reload class. `Allowed` uses the same
`oneof` parser and withholding rules as JSON Schema. `EnvVars` preserves
primary/alternate precedence. `Field` contains caller-owned declared
metadata, including original tags, and is excluded from JSON.

A withheld default is empty in `Default` and sets `Withheld`; JSON therefore
omits `default` and emits `withheld: true`. The table shows `[REDACTED]`.
JSON retains `env` and `also_env` and adds `env_vars`, `allowed`, `deprecated`
and `reload`. Callers needing an arbitrary generator can consume this
catalog without rebuilding the schema walk.

`Bounds` is a conjunction of `UsageBounds` entries. Each names its target
(`value`, `duration` in nanoseconds, Unicode `length`, `items` or `properties`), optional minimum/maximum
as exact JSON numbers, exclusive flags and whether that clause admits the
field's zero value. Other clauses still apply. Sensitive values withhold
numeric/value operands but may expose size bounds. Markdown includes these
bounds. Rules after `omitempty` describe zero or the constrained value;
earlier rules still apply. Nil pointers and collections are distinct from
non-nil zero/empty values. Integer bounds and enums retain 64-bit precision.
Duration operands also retain exact signed 64-bit precision; Markdown renders
them as duration strings. For example, `validate:"min=1s"` yields a duration
minimum of `1000000000` and displays `duration >= 1s`.

A root `DefaultProvider` supplies the same effective defaults to this catalog,
JSON Schema and templates as to loading. `Field.Tag` remains the original
struct metadata. Credential-bearing defaults are withheld, so a template
leaves them to the declared default instead of loading a redaction marker.

`PrintUsage[T]("APP")` renders that catalog as a table, JSON,
`FormatMarkdown` or an environment template. Markdown escapes cell content,
including pipes and newlines. Tables include the resolved reload class:

```txt
KEY       ENV           TYPE           DEFAULT     RELOAD   DESCRIPTION
port      APP_PORT      int            8080        restart  HTTP listen port
timeout   APP_TIMEOUT   time.Duration  30s         restart  Request timeout
name      APP_NAME      string         (required)  restart  Service name
password  APP_PASSWORD  string         [REDACTED]  restart
```

## Canonical keys

`ParseKey` applied to `named["a.b"].port` returns field, quoted-key and field
segments. `Key.String()` preserves that canonical spelling; `Key.Fields()`
returns `named.port`, and `Key.LastName()` returns `port`. Index selectors
and `[*]` patterns use distinct segment kinds. Empty or malformed keys
return an error wrapping `ErrKey`.

## Raw and policy-controlled trees

`Tree(cfg)` returns `(map[string]any, error)` with resolved values, including
secrets, detached from the config. It uses canonical config keys and scalar
representations the decoder accepts: duration text, text-marshaled values,
byte strings, numeric byte arrays and ordinary scalar values. Supported
finite values load back with their sensitive values intact.

`TreeFunc(cfg, leaf)` passes each leaf's actual key, declared `Field` and
detached tree value to a policy function. Its return value replaces that
leaf. Sensitive maps, lists and interfaces reach the callback once, whole,
so a policy can withhold their keys too. Empty and nil containers are leaves;
a nil callback is identity. Fields inside a dynamic interface retain the
interface's declared metadata. A redaction callback can return `[REDACTED]`
when `f.Sensitive && value != nil`.

Cycles use the diagnostic `[CYCLE]` marker and cannot round-trip. Opaque
values without a scalar/text representation return `ErrSchema`;
`MarshalText` failures return with the key and no partial tree. A custom
decoder must accept its marshaler's output. Dynamic interface values retain
the tree's scalar forms, not arbitrary original Go type identity. Redacted
projection can withhold a sensitive opaque value without serializing it.

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

`JSONSchema[T]()` returns a Draft 2020-12 schema of the config file — keys, types, descriptions, printable defaults and the validation rules JSON Schema can express — for editors and linters. `x-reload` records the resolved reload class and `x-env-ref` marks reference fields. It is advisory: `Load` is the authority, and a `required` key is not required in any one file, since a default or the environment may supply it.

Duration values remain strings. `x-config-duration-bounds` annotates their
runtime bounds with integer nanoseconds, using `minimum`, `maximum`,
`exclusiveMinimum`, `exclusiveMaximum` and `const` as applicable. Standard
JSON Schema validators ignore this annotation; `Load` enforces the rules.
The shared translator preserves conditional `omitempty` clauses and withholds
these value operands for sensitive fields.

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
templates and schema generation also decode the values they show. `Usage`
also decodes defaults when the root implements `DefaultProvider`.
