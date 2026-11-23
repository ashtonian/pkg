# Defining a config

[Documentation](README.md) · [Quick start](../README.md#quick-start) · [Go API](https://pkg.go.dev/github.com/ashtonian/pkg/config)

- [Defaults in code](#defaults-in-code)
- [Decoding and unknown keys](#decoding-and-unknown-keys)
- [Post-load hooks](#post-load-hooks)
- [Validation](#validation)

A config is a plain struct, and its tags are the whole schema: they say how each field is keyed, defaulted, validated, described and protected, and every part of the package reads them.

| Tag | Meaning | Read by | Example |
|---|---|---|---|
| `koanf` | The key. `-` skips the field, `,squash` merges a struct into its parent — an unexported embedded struct too, its exported fields promoted as `encoding/json` promotes them — `,remain` collects unknown keys in a map. Without a tag the key is the lowercased field name. | Every source and output; environment variable names derive from it. | `koanf:"max_conns"` |
| `default` | The lowest-precedence value; comma-separated for a slice, `key=value` entries for a map with string keys and non-struct values, which a source's entries merge with. Malformed entries in such a map default fail with `ErrSchema`. | The load; usage tables, starter files, the JSON Schema and flag help show it unless the field is sensitive. | `default:"localhost"` |
| `alias` | Former keys of the field, which sources may still set: a renamed setting keeps reading deployed files, and each load warns to rename them. | Every source; the JSON Schema lists them as deprecated. | `alias:"maxconns"` |
| `sep` | The separator that splits a string set for a list — a default, a variable, a file's string — into elements, so an element may hold a comma: `sep:";"`. Elements are trimmed and empty ones dropped. `none` keeps the string as one element, as written. Without it, commas split, after any `WithDecodeHook` hook. | Every source and tag default, element defaults, starter files, the JSON Schema, examples. | `sep:";"` |
| `format` | `format:"json"`: a string set for the field — a variable, a default, a file's string — is the JSON of its value, for a section, map, list or interface that one variable should set whole. The JSON's keys are checked, aliased and defaulted like any source's. | Every source and tag default; usage tables, starter files and `PrintEnv` mark and write it as JSON; the JSON Schema accepts the value or its JSON text. | `format:"json"` |
| `env` | More variables for the field, read as written, without the prefix — a platform's `PORT`, a `DATABASE_URL`, a variable a renamed key used to have. Every prefixed variable wins, then the first set in the list. | Environment loading; usage tables, starter files, the JSON Schema's `x-config-env` and `Field.EnvVars` list them. | `env:"PORT,HTTP_PORT"` |
| `envref` | `"true"` marks a string or `[]string` whose decoded value names variables read by the application. Pointers are followed; `"false"` opts out. Other types or tag values fail with `ErrSchema`. | Unknown-variable claims, `Field.EnvRef`, JSON Schema `x-env-ref`. | `envref:"true"` |
| `reload` | A nonempty class inherited by descendants; an inner tag overrides its parent. The default is `restart`; `live` and other labels denote live changes. | `Field.Reload`, usage, JSON Schema `x-reload`; applier routing when `WithReloadTags` is enabled. | `reload:"live"` |
| `validate` | [go-playground/validator](https://pkg.go.dev/github.com/go-playground/validator/v10) rules. | Validation on every load and reload; `required` marks usage tables and starter files; the JSON Schema states the bounds, lengths and `oneof` enums. | `validate:"required,min=1"` |
| `desc` | What the setting is for. | Usage tables, `PrintRedacted(..., WithIncludeDescriptions())`, starter-file comments, the JSON Schema `description`, kongprovider flag help. | `desc:"Database host"` |
| `example` | A sample value, decoded as a default would be; one its field cannot hold fails with `ErrSchema`. Never a value the service runs with. | Starter files write it, commented out, where there is no default; usage tables and the JSON Schema `examples` show it — unless the field is sensitive. | `example:"db.internal"` |
| `sensitive` | `"true"` redacts diagnostics — on a struct, slice or map, everything inside it; `"false"` exempts a field whose name has a credential word but holds no secret (`key_prefix`). Numeric counters, booleans and durations do not redact from their names alone. | Printing, diffs, reload results and usage tables show `[REDACTED]`; starter files and the JSON Schema leave out its default and any rule that names a value. | `sensitive:"true"` |
| `secret` | Where the value lives in your secret store (see [Secrets](secrets.md#secrets)); implies `sensitive`. | `SecretsFrom` and `WithSecretPopulator`, after decoding and before validation. | `secret:"prod/db#password"` |

- Pointers are followed. A struct that decodes itself — an `encoding.TextUnmarshaler` such as `time.Time` or `netip.Addr`, or a mapstructure `Unmarshaler` — is one value, not a section.
- Maps, slices and interfaces are values whose entries are keys of their own. An element struct's `default` tags apply to every map entry and list element a source creates, below what the source set; map defaults merge missing entries there too. An explicit `null` stays null; write `{}` for an entry that should take its defaults.
- An embedded pointer to an unexported struct cannot be squashed: decoding cannot allocate it, so the type is refused with `ErrSchema`; embed the struct itself, or export its type.
- A `,remain` field collects the keys no other field of its struct, or of a struct it squashes, reads. Decoding fills one map keyed by strings that it can set, so the field must be exported (promoted through an unexported embedded struct is fine), a map keyed by strings, not also squashed, and the only one so tagged: anything else is refused with `ErrSchema` before any source is read.
- Two fields of one struct may not share a key, and a key may not contain `.`, `[`, `]`, `*`, a quote, a backslash or a control character: such a type is refused with `ErrSchema` before anything loads.
- A tag `reflect` cannot read whole (an unquoted value, a stray character, a key written twice) is refused with `ErrSchema`, since `reflect` would read only part of it.
- A tag key config does not read is refused when it most likely misspells one it does: one character inserted, deleted, replaced or swapped with its neighbour away from one of the keys above, or equal to one ignoring case, `-` and `_` (`defualt`, `vaildate`, `envRef`). A load, a reload, `CheckKey`, every generator (`Usage`, `JSONSchema`, `PrintTemplate`, `PrintEnv`, …) and checked redaction refuse it alike; raw inspection (`Fields`, `FieldFor`, `Tree`) does not, so it can diagnose the type ([declaration checks](inspection.md#declaration-checks)). The `ErrSchema` names the field, the key written and the key meant. A key another library reads that sits that close to one of config's is listed by the struct declaring the field:

  ```go
  type Column struct {
      Name string `koanf:"name" defaults:"id"` // read by another library
  }

  func (Column) ForeignTagKeys() []string { return []string{"defaults"} }
  ```

  The list covers the fields of the struct that declares the method, wherever they are reached, embedded ones included. A struct that embeds `Column` is promoted the method but not the allowance: a key on its own fields is still refused unless it declares a `ForeignTagKeys` of its own, so a misspelt `defaults:"80"` beside the embedded struct does not load as zero. The method must be pure and return the same keys each time; it may run more than once for one type. A type another package owns is covered by a defined type over it (`type Column lib.Column`) that lists its keys.
- A load, a reload and `CheckKey` judged by the default validator refuse a `validate` tag it cannot parse — an undefined rule (`requird`), `keys` without `dive`, an empty rule — with `ErrSchema` naming the field and the validator's complaint, before any source is read, wherever the validator reads the tag on every validation, whatever the config holds: on the root's fields and on the fields of every struct held by value beneath them — a squashed or embedded struct, a section, a `koanf:"-"` struct, a struct that decodes itself from text, and each element of an array of one or more elements a `dive` reaches included — but not inside `time.Time`, past `nostructlevel`, nor below a struct a `structonly` rule reaches, whose own tags it still reads. A struct the validator reads only when a value puts it there is not checked up front: one behind a pointer, a map, a slice or an interface, one beneath an `omitempty` or `omitzero` rule, and one whose value the validator replaces with its `ValidatorValue`. An array of no elements (`[0]T`) is never walked, so nothing beneath its `dive` is checked. Such a struct may carry rules registered on another validator while the config leaves it nil or empty: a tag the validator never reads is never refused. On every field whose value the validator walks there — all but those of a struct a `structonly` rule reaches — a `dive` the field's type cannot take, through pointers as the validator follows them, is refused the same way, naming the field and the rule: `dive` on an int, `keys` after a `dive` on a slice, a third `dive` on a `[][]int`. The validator would panic on it only once the field held something to walk, but it is wrong whatever the field holds. Where validation does reach a tag it cannot parse, or a rule that parses but cannot apply to its field's type — `min=abc`, `len` or `oneof` on a kind they do not measure, `required_if` with an odd number of params, a misapplied `dive` in a struct not checked up front — the load fails with `ErrSchema` naming, by key, the field the validator was validating (`at items[0].list`), followed by the validator's message; the field's value is never shown. The validator would otherwise panic midway through the load. A tag's params are the source's own text, never a field's value. A validator set with `WithValidator` reads its own rules, registered ones included, and its panics are its own, as are those of the caller's code the default validator calls (a `validateFn` method); `WithSkipValidation` reads none.

Credential-name inference judges a Go field name or map key by its words. A name splits at every character that is neither a letter nor a digit, at each change from lower to upper case, between letters and digits, and before the last capital of an acronym that a lower-case letter follows: `APIKey` is *api key*, `HTTPAuthMode` *http auth mode*, `db_password` *db password*; an all-capitals run such as `APIKEY` stays one word. The name is a credential's when one of its words is a credential word — `password`, `passwd`, `pwd`, `pass`, `passphrase`, `secret`, `token`, `key`, `apikey`, `accesskey`, `secretkey`, `privatekey`, `credential`, `cred`, `auth`, `authorization`, `private` or `dsn` — or its plural, or ends with one of five letters or more, as an unsplit compound does (`dbpassword`, `accesstoken`). A word that only contains one is not: `Keyspace`, `Tokenizer`, `Monkey` and `AuthenticationMode` are not inferred sensitive.

Inference applies to text-capable types: strings, bytes, maps, interfaces, text decoders and containers that can hold text. An explicit `sensitive:"false"` overrides only the field name; a sensitive parent or `secret` tag still wins.

Every output names a value the same way:

| Key | Names |
|---|---|
| `db.host` | a struct field |
| `items[0].port` | a field of a list element |
| `named["primary"].port` | a field of a map entry; map keys are always quoted |
| `items[*].port` | any element or entry, in usage tables and `FieldFor` |

`config.KeyOf(&cfg, &cfg.DB.MaxConns)` returns `"db.max_conns"` from the field itself, so code never spells a key that can drift from its field.

`ParseKey(text)` exposes this grammar as a `Key` of typed `Segment` values. `Key.String()` returns canonical spelling, `Key.Fields()` removes collection selectors, and `Key.LastName()` returns the last field or map key. Malformed or empty keys return `ErrKey`.

## Defaults in code

A `default` tag is the right place for a default that holds wherever the field's type appears. When it does not — a shared section that each program embedding it sets differently, or one type used at several paths with different defaults — set it in a `SetDefaults()` method on the type that owns the choice:

```go
type RedisConfig struct {
	PoolSize int           `koanf:"pool_size" default:"10"`
	TTL      time.Duration `koanf:"ttl" default:"5m"`
}

type Caches struct {
	Sessions RedisConfig `koanf:"sessions"`
	Queries  RedisConfig `koanf:"queries"`
}

// Query results churn: a bigger pool and a shorter TTL than sessions.
func (c *Caches) SetDefaults() {
	c.Queries.PoolSize = 50
	c.Queries.TTL = 30 * time.Second
}
```

`SetDefaults` runs on a value that already holds the tag defaults, so it changes only the fields it sets, and every source overrides it. Usage tables, the JSON Schema, starter files and key checks all show the defaults it sets. See [code defaults](sources.md#code-defaults) for the order of the calls, collections, pointer sections and embedding.

## Decoding and unknown keys

Values decode with mapstructure's weak typing (`"8080"` fills an `int`) plus string → `time.Duration`, comma-separated string → a slice of any element type (`"80, 443"` fills a `[]int`, `"1s,2m"` a `[]time.Duration`; a `[]byte` or a slice type that decodes itself takes the string whole; a field's `sep` tag splits on its own separator instead, before any hook), `key=value` entries → a map (`"team=core, tier=1"`; a map of structs takes no string), and `encoding.TextUnmarshaler`. A split string's elements and entries are trimmed of surrounding white space and empty ones are dropped, so `"a, b,"` and `"a,b"` set the same list; `ListElements` exposes that rule for integrations that split a list themselves. A list a file writes as a list keeps its elements as written. `WithDecodeHook` adds hooks of your own, which run first.

A key that no field decodes fails the load with `ErrValidation`, naming the key and the field of the same struct it most likely misspells — a typo cannot leave a service running on the default it was meant to override:

```txt
unmarshaling config: configuration validation failed: keys no field of main.AppConfig decodes: server.prot (did you mean server.port?)
```

`errors.As` reaches an `*UnknownKeysError`, whose `Keys` give each key and its `Suggestion`, for a program to report them in its own words. A suggestion is the field fewest edits away, counting an adjacent transposition as one edit and ignoring case, within a third of the key's length (at least 2) and sharing at least one character in place; a key no field is that close to has none.

The check follows decoding exactly: it looks inside list elements and map entries of structs, through squashed structs, and lets through a map's own keys, anything beneath an interface, and keys a `,remain` field collects. A section no field holds is one key (`legacy`, not `legacy.old`).

`WithUnknownKeys(handle)` decides instead. The handler gets the unknown keys, and the variables under the `WithEnvPrefix` prefix that name no key, whenever either exists, at every load and reload; a nil return accepts the load, an error fails it with `ErrValidation`. Accept and warn:

```go
config.WithUnknownKeys(func(ctx context.Context, unknown *config.UnknownKeysError) error {
	logger.WarnContext(ctx, "config input no field reads", "error", unknown)
	return nil
})
```

```txt
keys no field of main.AppConfig decodes: server.prot (did you mean server.port?); environment variables no field of main.AppConfig reads: APP_SERVER_PROT (did you mean APP_SERVER_PORT?)
```

Or accept some and fail on the rest, by returning a copy of `unknown` without the keys you expect — a section another program reads, a setting you removed but deployed files still carry. Keys and variables are named, never their values. Diagnostics for keys inside a sensitive map or list name only the enclosing field; this also applies to alias warnings. `WithAllowUnknownKeys()` accepts everything silently, for a file shared with another program. A field typed `any` or `map[string]any` accepts everything beneath its key while the rest of the config stays strict. To accept what one source sets — a settings store that keeps rows for removed fields — and keep every other source strict, give that source `WithUnknownEntries` ([sources and precedence](sources.md#unknown-keys-of-one-source)): the keys it claims go to its own callback, and the handler sees only the rest.

A successful `LoadSnapshot` or reloader publication retains its accepted report
in `Snapshot.UnknownKeys`, the keys `WithUnknownEntries` sources accepted
included; a clean publication sets it to nil. Refused or
canceled attempts preserve the previous publication's report. Reports contain
names and suggestions, never values, and every handler/snapshot/result receives
a detached copy. JSON uses `unknown_keys` with `keys` and `variables` arrays of
`key`/optional `suggestion`; the Go `reflect.Type` is omitted.

### Renaming a key

Because an unknown key fails the load, renaming a setting would break every file still using the old name. Keep the old name as an `alias`:

```go
MaxConns int `koanf:"max_conns" alias:"maxconns,max_connections"`
```

Each source's keys are rewritten to the field's key before the sources merge, so precedence holds across old and new names, and a source that sets both fails as a field spelled two ways. Each load logs at warn level which keys used a former name, in which source, so operators know to rename them; the JSON Schema accepts the old names, marked `deprecated`. Aliases must differ from every key and alias in the same struct, ignoring case, including through squashed fields. Provider origin annotations accept the same aliases and spelling as their data. An alias renames keys, not variables: give a renamed key's old variable an `env` tag.

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

`validate` tags run on every load, and failures name config keys: `db.max_conns(min=1), port(max=65535)`. A cross-field rule's param names the fields it compares by key too — `max_wait(gtefield=min_wait)`, `store.bucket(required_if=store.type s3)`, `store.owner(required_with=store.bucket store.region)` — for the `*field` and `*csfield` comparisons, `fieldcontains`, `fieldexcludes` and the `required_*`, `excluded_*` and `skip_unless` families; a rule comparing against a sensitive field's value shows no param. `errors.As` reaches the `validator.ValidationErrors`. Rules no tag can express go in a `Validate() error` method on the config, which runs after the tags, on the config the hooks produced:

```go
func (c *AppConfig) Validate() error {
	if c.TLS.Enabled && c.TLS.CertFile == "" {
		return errors.New("tls.cert_file is required when tls.enabled is set")
	}
	return nil
}
```

Kong also calls a `Validate() error` method on any struct its CLI embeds, during Parse. With the config struct embedded in a Kong command, wire it with [`kongprovider.Load`](../kongprovider/README.md), which loads before Kong validates, so both calls judge the loaded config.

`WithValidator` replaces the tag validator with any `Struct(any) error` (one with `StructCtx` gets the load's context); register `config.FieldName` as its tag-name function to keep errors named by key. `WithSkipValidation` turns validation off; unknown keys still fail.
