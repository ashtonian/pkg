# Defining a config

[Documentation](README.md) · [Quick start](../README.md#quick-start) · [Go API](https://pkg.go.dev/github.com/ashtonian/pkg/config)

- [Decoding and unknown keys](#decoding-and-unknown-keys)
- [Post-load hooks](#post-load-hooks)
- [Validation](#validation)

A config is a plain struct, and its tags are the whole schema: they say how each field is keyed, defaulted, validated, described and protected, and every part of the package reads them.

| Tag | Meaning | Read by | Example |
|---|---|---|---|
| `koanf` | The key. `-` skips the field, `,squash` merges a struct into its parent — an unexported embedded struct too, its exported fields promoted as `encoding/json` promotes them — `,remain` collects unknown keys in a map. Without a tag the key is the lowercased field name. | Every source and output; environment variable names derive from it. | `koanf:"max_conns"` |
| `default` | The lowest-precedence value; comma-separated for a slice, `key=value` entries for a map with string keys and non-struct values, which a source's entries merge with. Malformed entries in such a map default fail with `ErrSchema`. | The load; usage tables, starter files, the JSON Schema and flag help show it unless the field is sensitive. | `default:"localhost"` |
| `alias` | Former keys of the field, which sources may still set: a renamed setting keeps reading deployed files, and each load warns to rename them. | Every source; the JSON Schema lists them as deprecated. | `alias:"maxconns"` |
| `sep` | The separator that splits a string set for a list — a default, a variable, a file's string — into elements, so an element may hold a comma: `sep:";"`. `none` keeps the string as one element. Without it, commas split, after any `WithDecodeHook` hook. | Every source and tag default, element defaults, starter files, the JSON Schema, examples. | `sep:";"` |
| `format` | `format:"json"`: a string set for the field — a variable, a default, a file's string — is the JSON of its value, for a section, map, list or interface that one variable should set whole. The JSON's keys are checked, aliased and defaulted like any source's. | Every source and tag default; usage tables, starter files and `PrintEnv` mark and write it as JSON; the JSON Schema accepts the value or its JSON text. | `format:"json"` |
| `env` | More variables for the field, read as written, without the prefix — a platform's `PORT`, a `DATABASE_URL`, a variable a renamed key used to have. The prefixed variable wins, then the first set in the list. | Environment loading; usage tables, starter files and `Field.EnvVars` list them. | `env:"PORT,HTTP_PORT"` |
| `validate` | [go-playground/validator](https://pkg.go.dev/github.com/go-playground/validator/v10) rules. | Validation on every load and reload; `required` marks usage tables and starter files; the JSON Schema states the bounds, lengths and `oneof` enums. | `validate:"required,min=1"` |
| `desc` | What the setting is for. | Usage tables, `PrintRedacted(..., WithIncludeDescriptions())`, starter-file comments, the JSON Schema `description`, kongprovider flag help. | `desc:"Database host"` |
| `example` | A sample value, decoded as a default would be; one its field cannot hold fails with `ErrSchema`. Never a value the service runs with. | Starter files write it, commented out, where there is no default; usage tables and the JSON Schema `examples` show it — unless the field is sensitive. | `example:"db.internal"` |
| `sensitive` | `"true"` redacts everywhere — on a struct, slice or map, everything inside it; `"false"` exempts a field whose name only looks like a credential's (`max_tokens`). | Printing, diffs, reload results and usage tables show `[REDACTED]`; starter files and the JSON Schema leave out its default and any rule that names a value. | `sensitive:"true"` |
| `secret` | Where the value lives in your secret store (see [Secrets](secrets.md#secrets)); implies `sensitive`. | `SecretsFrom` and `WithSecretPopulator`, after decoding and before validation. | `secret:"prod/db#password"` |

- Pointers are followed. A struct that decodes itself — an `encoding.TextUnmarshaler` such as `time.Time` or `netip.Addr`, or a mapstructure `Unmarshaler` — is one value, not a section.
- Maps, slices and interfaces are values whose entries are keys of their own. An element struct's `default` tags apply to every map entry and list element a source creates, below what the source set; map defaults merge missing entries there too. An explicit `null` stays null; write `{}` for an entry that should take its defaults.
- An embedded pointer to an unexported struct cannot be squashed: decoding cannot allocate it, so the type is refused with `ErrSchema`; embed the struct itself, or export its type.
- Two fields of one struct may not share a key, and a key may not contain `.`, `[`, `]`, `*`, a quote, a backslash or a control character: such a type is refused with `ErrSchema` before anything loads.

Every output names a value the same way:

| Key | Names |
|---|---|
| `db.host` | a struct field |
| `items[0].port` | a field of a list element |
| `named["primary"].port` | a field of a map entry; map keys are always quoted |
| `items[*].port` | any element or entry, in usage tables and `FieldFor` |

`config.KeyOf(&cfg, &cfg.DB.MaxConns)` returns `"db.max_conns"` from the field itself, so code never spells a key that can drift from its field.

## Decoding and unknown keys

Values decode with mapstructure's weak typing (`"8080"` fills an `int`) plus string → `time.Duration`, comma-separated string → a slice of any element type (`"80,443"` fills a `[]int`, `"1s,2m"` a `[]time.Duration`; a `[]byte` or a slice type that decodes itself takes the string whole; a field's `sep` tag splits on its own separator instead, before any hook), `key=value` entries → a map (`"team=core,tier=1"`; a map of structs takes no string), and `encoding.TextUnmarshaler`. `WithDecodeHook` adds hooks of your own, which run first.

A key that no field decodes fails the load with `ErrValidation`, naming the key and the field of the same struct it most likely misspells — a typo cannot leave a service running on the default it was meant to override:

```txt
unmarshaling config: configuration validation failed: keys no field of main.AppConfig decodes: server.prot (did you mean server.port?)
```

`errors.As` reaches an `*UnknownKeysError`, whose `Keys` give each key and its `Suggestion`, for a program to report them in its own words. A suggestion is the field fewest edits away, ignoring case, within a third of the key's length (at least 2) and sharing at least one character in place; a key no field is that close to has none.

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

Or accept some and fail on the rest, by returning a copy of `unknown` without the keys you expect — a section another program reads, a setting you removed but deployed files still carry. Keys and variables are named, never their values. Diagnostics for keys inside a sensitive map or list name only the enclosing field; this also applies to alias warnings. `WithAllowUnknownKeys()` accepts everything silently, for a file shared with another program. A field typed `any` or `map[string]any` accepts everything beneath its key while the rest of the config stays strict.

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
