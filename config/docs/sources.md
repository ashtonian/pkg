# Sources and precedence

[Documentation](README.md) · [Quick start](../README.md#quick-start) · [Go API](https://pkg.go.dev/github.com/ashtonian/pkg/config)

- [Shadowed inputs](#shadowed-inputs)
- [Code defaults](#code-defaults)
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
| 6 | `SetDefaults()` ([`Defaulter`](#code-defaults)) | Defaults set in code where a tag cannot say them: per program, per path. |
| 7 | `default` tags | Always. |

Each file takes the parser registered for the longest suffix of its name, ignoring case: `.yaml` and `.yml` are YAML, `.json` is JSON, and any other file is YAML. `WithFileParser(".toml", toml.Parser())` adds a format, so `config.yaml`, `overrides.json` and `local.toml` load together; `WithFileParser("", p)` changes the parser for every file no other suffix matches. JSON is strict: one object per file, integers within int64/uint64 kept exact (larger integer literals fail), a key set twice in one object an error.

To migrate the old single-parser API, register the parser for `""` and remove the built-in suffix registrations with `WithFileParser(".yaml", nil)`, `WithFileParser(".yml", nil)` and `WithFileParser(".json", nil)`. Setting only `""` leaves those registrations in force.

An optional file may be absent, but one that exists and cannot be read or parsed fails the load. Under a reloader, a file that loaded once stays required, so deleting it fails the reload rather than silently reverting its keys.

Keys match fields in any letter case, as decoding does, and each source's keys are respelled as the fields are before the merge — so precedence holds whatever case a source used. A source that spells one field two ways (`port` and `Port`) fails the load.

## Shadowed inputs

A key that two sources set takes the higher one's value, so the lower one's input is *shadowed*: it cannot take effect while the higher source sets the key. Maps merge key by key, so `APP_DB_HOST` shadows a file's `db.host` and leaves the rest of its `db` in force; any other value — a scalar, a list, `null` — replaces everything the lower source set beneath its key.

| Where | What it lists |
|---|---|
| `Snapshot.Shadowed`, from `LoadSnapshot` and every `NewReloader` load | Every shadowed input of the load: the key, the `Source` that set it and the source kept instead (`By`). |
| `ReloadResult.Shadowed`, and one warning in the log per entry | The inputs that changed since the previous generation — set, edited or removed — while a higher source holds their key: edits a reload read and could not apply. |
| `WithShadowedInputs(fn)` | Called with every shadowed input at each load and reload that has any; a nil return accepts them, an error fails the load with `ErrValidation`. Return the `*ShadowedInputsError` it gets to require one source per key. |

```txt
log_level (file /etc/app/config.yaml, kept by environment APP_LOG_LEVEL)
```

Overriding a file from the environment or a flag is a legitimate pattern, so shadowed inputs never fail a load without a handler; a load that looks for them logs them at debug level. Two layerings are designed overrides and are never listed: defaults (`default` tags, `SetDefaults`, `WithDefaults`) beneath any source, and the precedence inside the environment layer — a narrower `WithEnvPrefixes` prefix over a broader one, a prefixed name over an `env` tag's — which is one source. Entries name keys and sources, never values; inside a sensitive map or list the key stops at the container and an environment variable is named by its prefix alone. Input no field reads is reported by [`WithUnknownKeys`](schema.md) instead.

**A key meant to change while the process runs must be set only by sources that change.** A reloader reads every source again, but the process environment and typed CLI flags (kongprovider copies them when the reloader is built) stay what they were at startup, and both outrank files. A variable left behind after a setting moved into a file, or a flag on the command line, pins its key for the life of the process: the file's edit is read and ignored. `ReloadResult.Shadowed` and the warning name the key, the file and the variable or flag that kept it. A plain `Load` looks for shadowed inputs only for a `WithShadowedInputs` handler; `NewReloaderFunc` has no view of its loader's sources and reports none.

## Any koanf provider

A provider loads at either provider layer: pass a nil parser when it returns keys and a parser when it returns a document. Every reload reads it again through the same pipeline. A provider that implements `ContextProvider` is read with the load's context, so a slow remote read stops when the load is cancelled. A load whose context has ended starts no further stage — no provider, environment read, decode, report callback, secret populator, post-load hook or validation — and returns an error wrapping `ctx.Err()` and its `context.Cause`; a stage already running observes the context itself.

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
| Stored entries keyed as `CanonicalKey` spells them | `config.WithBaseEntries`, `config.WithOverrideEntries` | a poll, or `SourceChanged` |

Bridging a provider's watch to a reloader is one call — `p.Watch(func(_ any, err error) { if err == nil { reloader.SourceChanged() } })` — and bursts coalesce into one reload.

`NewMapProvider(read, ".")` turns a function into a provider read on every load; with `"."` its keys are dotted paths, with `""` the map is already nested. What `read` returns is copied. `WithBaseEntries(read)` and `WithOverrideEntries(read)` read entries keyed in this package's own key grammar instead — see [stored entries](#stored-entries).

A provider that returns keys, `WithDefaults` and `SetDefaults` may hold typed Go values. Each source is deep-copied as it merges, and the copy keeps a struct only by its exported fields unless it knows the type, as it knows `time.Time`. A value whose state that copy would lose — a `netip.Addr` or `netip.Prefix`, alone or in a section, list, array or map — is read through its `MarshalText` instead; a field of interface type holding one receives that text. A `MarshalText` error fails the load with `ErrValidation`, naming the key and not the value. From a provider or `WithDefaults`, a typed map or section still replaces what the layers beneath it set, and a nil list stays nil. A higher source that sets keys beneath it then sets one key of it as it sets one key of a nested map: a file's `addresses: {first: {port: 9}}` over a default `map[string]Server` changes that one port, and every other entry and field keeps its value. So does an environment variable naming a key beneath it (`APP_ADDRESSES_FIRST_PORT`), an override provider's `map[string]any`, a stored entry, a flag that sets one field of a section, and a key edit. A typed value replaces it whole instead, as it replaces any layer beneath it — a higher provider's typed map or section, a flag that sets a whole map ([kongprovider](../kongprovider) hands on Kong's typed map) — and so does a variable that sets the whole map (`APP_LABELS=team=core`). A `json.Number`, which a JSON decoder with `UseNumber` returns so large integers keep every digit, decodes as the number it holds wherever a number belongs — into a `time.Duration` as a file's number does — and as its text into anything else.

A higher source can add entries to such a map and change them, but not remove one: an entry it sets to `null` keeps its key, holding the element's zero value. To let a higher source narrow a default, such as an allow-list `map[string]bool{"*": true}`, give the default as a list, which a higher source replaces whole; have the program treat an entry's zero value (`"*": false`) as absent; or set the map to `null` in one source and to the narrowed map in a source above it. Only a map keyed by `string` or by a predeclared integer type merges this way. A map keyed by a named type — `slog.Level`, `time.Duration`, or a string or integer type of your own — is the exception: a decode hook of yours can read such a key its own way, which no text written for it would survive, so it keeps its own keys, and a higher source replaces it whole. Its entries are named, and keys beneath them reached, by their keys' text all the same (see [map entries](schema.md#map-entries)). A map `SetDefaults` sets follows the [code defaults](#code-defaults) rules instead: one keyed by a named string type that only adds to its tag's entries merges entry by entry.

A provider whose `Read` returns a copy made with koanf's `maps.Copy` has lost such a value before the load reads it; `confmap.Provider` makes that copy when it is built. Return the map as built, or use `NewMapProvider`, which copies without the loss.

## Code defaults

A `default` tag says what a field defaults to wherever its type appears. For a default that depends on where the type is used, implement `SetDefaults()` (the `Defaulter` interface) on the type that owns the choice. Each program that embeds a shared section sets that section's defaults its own way, and a type used at several paths gets different defaults at each:

```go
type Broker struct {
	Host string `koanf:"host"`
	User string `koanf:"user" default:"guest"`
}

type Brokers struct {
	Primary   Broker `koanf:"primary"`
	Secondary Broker `koanf:"secondary"`
}

// Only the primary broker has a default host.
func (b *Brokers) SetDefaults() { b.Primary.Host = "tcp://localhost:1883" }

type Worker struct {
	Queue   QueueConfig `koanf:"queue"`
	Brokers Brokers     `koanf:"brokers"`
}

// The worker's own channel and topic; every other queue field keeps its tag.
func (w *Worker) SetDefaults() {
	w.Queue.Channel = "worker"
	w.Queue.Topic = "jobs"
}
```

A load fills a fresh value with the default tags, then calls `SetDefaults` on every `Defaulter` in it, innermost first, so `Worker`'s overrides what `QueueConfig.SetDefaults` sets. What the calls change is the code-defaults layer: above the tags, below `WithDefaults`, providers, files, the environment and flags. A field `SetDefaults` leaves alone keeps its tag — `primary.user` is still `guest`. Provenance names the layer `code_defaults`. Because the values are typed Go, a misspelled field or a value of the wrong type fails to compile rather than to load.

- **Pointer sections.** A nil pointer section whose type sets defaults is allocated for its `SetDefaults` and kept only if it then holds anything. Setting a section to `nil` in `SetDefaults` turns off what its tags would set, as a source's `null` does.
- **Collections.** A map's or list's element type that is a `Defaulter` runs for every entry a source creates. The entry holds what the source wrote, then, for the fields it left out, what the element's `SetDefaults` sets, then the element's tags: a file's `routes: {a: {timeout: 1s}}` keeps its timeout and takes the rest from `Route`. A map or list `SetDefaults` itself sets merges like a source's: map entries one by one, a list whole, its elements taking their element defaults. A map keyed by a predeclared integer type, and a map keyed by `string` that drops an entry the field's `default` tag sets (`Labels` tagged `default:"a=1,b=2"` set to `{c: 3}`), replace the tag's map whole, as a typed map from `WithDefaults` does, and a higher source merges into them the same way: a file's `labels: {d: 4}` then loads `{c: 3, d: 4}`. A map keyed by a named integer type, and one keyed by a named string type that drops such an entry, replace the tag's map too, and a higher source replaces them whole, as it replaces such a map from `WithDefaults`.
- **Embedding.** An embedded struct's `SetDefaults` is promoted as Go promotes methods: it runs as the enclosing struct's, unless the enclosing struct declares its own, which then calls the embedded one when it should run. Two embedded `Defaulter`s whose methods collide promote neither and fail with `ErrSchema`, as does a `SetDefaults` with a value receiver, which could change nothing.
- **Values that marshal to text.** A `netip.Prefix`, `[]netip.Addr` or `map[string]netip.Prefix` that `SetDefaults` sets loads as set, like a typed value from any source ([above](#any-koanf-provider)).
- **Determinism.** `SetDefaults` must be deterministic and do no I/O: it runs at every load and reload, and for `Usage`, `JSONSchema`, `PrintTemplate`, `CheckKey` and `DecodeKey`, which all show and use these defaults. A sensitive field's default stays withheld from every one of them.

Caller `WithDefaults` still supplies runtime profiles — values the program knows only when it starts — above the code defaults.

## Validating stored entries

`CheckKey[Config](ctx, "server.port", "8080")` decodes one key against defaults
and validates it with the same hooks, tag validator and `Validate` method as
`Load`. `DecodeKey` returns the resulting `*Config` when a caller also needs
its typed values. A rejected key returns `*KeyError`: `Unknown` and `ErrKey`
distinguish an unknown key — the key itself, or a key inside its value that
no field reads, such as `primary.bogus` in `primary` set to `{"bogus": 1}`;
a known key with an invalid value matches `ErrValidation`. Schema/setup
errors and cancellation return directly, among them a `validate` tag the
default validator cannot apply to its field (`dive` on an `int`), which is
`ErrSchema` with no `*KeyError`. No partial config is returned on
failure. Error messages contain the key, not the value; wrapped causes are
for trusted diagnostics.

These calls honor `WithDefaults`, `WithDecodeHook`, `WithValidator` and
`WithSkipValidation`. They read no provider, file or environment and run no
secret, post-load, unknown-input or shadowed-input callbacks. Other fields remain at defaults,
so cross-field rules judge that context. Keys use `ParseKey` syntax, with
aliases and case folding accepted for fields. List indexes must already
exist in defaults or append immediately after them; pass the whole list to
create several elements. Wildcards and sparse expansion are refused.

The value sets its key as a source above the defaults would. A
`map[string]any` — what a file or `encoding/json` decodes to — merges into
what the defaults hold there key by key: `addresses["first"].port` set to
`9`, or `addresses["first"]` set to `{"port": 9}`, keeps that entry's host
and every other entry, whether the defaults hold them as a typed
`map[string]Server` or as nested maps. Any other value replaces what is
beneath it: a struct and a typed map such as `map[string]int` are the whole
section or map, as `WithDefaults` sets them, so `addresses["first"]` set to
`Server{Port: 9}` leaves that entry's host empty. `nil` sets the zero value,
as an explicit null from any source does. An index edits that element of
the defaults' list. A key beneath a value decoded from text, such as a
`netip.Prefix`, names nothing config reads. A key a section's
`koanf:",remain"` field collects is a key like any other, and so is an entry
of a map keyed by any type with text (see [map entries](schema.md#map-entries)):
`ports["80"].port`, `levels["WARN"].host`, its fields resolving through
aliases and case as any section's do. A map keyed by a named type is
replaced whole by a higher source (see [any koanf provider](#any-koanf-provider)),
and a key is one: `levels["WARN"].host` set over defaults holding that map
leaves the map one entry holding one host. The caller's defaults are never
changed, and the result shares nothing with them.

### Canonical keys

`CanonicalKey[Config](key)` returns a key spelled the one way this package
prints it: each field by its key, never an alias tag's former name or
another letter case (`MAIN.Port` → `primary.port`); a map entry quoted, by
the text of the key a load reads it as (`ports["0x50"]` → `ports["80"]`,
`timeouts["60s"]` → `timeouts["1m0s"]`; see [map entries](schema.md#map-entries));
and a key beneath an interface, or one a remain field collects, as a field
wherever it can be one (`extra["x"]` → `extra.x`); what lies beneath a key a
remain field collects is spelled by the field's element type, as beneath any
map entry. It refuses what `CheckKey` refuses as unknown,
with an `Unknown` `*KeyError`, and reads no value and no `validate` tag: a
key's spelling does not depend on validation. A store that keys its rows by
it holds one row per key, whatever spelling a client sent.

`Key.Within(p)` reports whether a key is `p` or beneath it, so a store can
find the rows a write of `p` replaces: `primary.port` and `primary` are
within `primary`, `pool[10]` is not within `pool[1]`, and a wildcard in `p`
stands for any map key, list index or field, the last because `CanonicalKey`
spells a key beneath an interface or remain field as one: `extra.x` is
within `extra[*]`. `Within` reads no schema, so a wildcard after a struct
matches its fields too. Compare keys `CanonicalKey` spelled.

### Validating a batch of edits

`CheckKey` judges each key against defaults, so a loop over a batch judges
no edit against the others: with defaults `min=0, max=10` and a rule
`min <= max`, `min=8` and `max=4` each pass alone and fail together, and
`min=12` fails alone while `min=12, max=20` is valid. `DecodeEdits` sets
every edit of a batch over one explicit baseline — a configuration, or the
defaults when it is `nil` — and decodes and validates the result once:

```go
candidate, err := config.DecodeEdits(ctx, &baseline, map[string]any{
    "limits.min": 12,
    "limits.max": 20,
})
```

Edits follow `DecodeKey`'s keys and values, `nil` setting the zero value.
They apply in key order, an index by its number and a key before the keys
beneath it. One batch can append several list elements (`pool[1]`,
`pool[2]`, …), and a key beneath another sets what it names in the value the
other set: `primary` set to `{"port": 6}` merges into the baseline's section
and `primary.port` set to `7` then sets its port, while `primary` set to
`Server{Port: 6}` replaces the section first. A map set beneath merges into
the value the other set as into the baseline's: `addresses` set to
`{"a": {"host": "h", "port": 1}}` and `addresses["a"]` to `{"port": 2}` leave
`a` at `{h 2}`. The entries of one source apply the same way. A batch is refused whole when
any of its keys is. The error is one `*EditsError` whose `Refused` lists
every refused key, in key order (an index by its number: `pool[2]` before
`pool[10]`), whichever stage refused it; a `*KeyError`
that is not `Unknown` says why in its message, naming keys only:

| Edits | Refused key |
|---|---|
| A key naming nothing config reads, or a wildcard | the key, `Unknown` |
| A key inside a value that no field reads: `primary` set to `{"bogus": 1}` | `primary.bogus`, `Unknown` |
| One value under two spellings: `port` and `PORT`, or a field and its alias | the key, once |
| An index past the next element of the list beneath — the baseline's, or one the batch sets | the index's key |
| A key beneath one set to a value that cannot hold it: `primary` set to `5`, or `nil`, and `primary.port` | the key beneath |
| Keys selecting both elements and fields of one value: `extra[0]` and `extra.x` | the element's key |

When every key applies but the candidate does not decode or validate, the
`*EditsError` names every edited key in `Keys` and keeps the decoder's or
validator's cause in `Err` for trusted diagnostics. The error wraps every
refused `*KeyError` and `Err`, so each keeps the category `DecodeKey` gives
it: `errors.Is` reaches `ErrKey` when an unknown key was refused and
`ErrValidation` when another refusal was made or the candidate failed, and
`errors.As` reaches the first refused `*KeyError`. The message names keys
and never a value. A baseline holding a value
that does not marshal to text is an error of its own, neither, and a
`validate` tag the default validator cannot apply is `ErrSchema` as it is,
no batch's fault. On any
failure no candidate is returned. The baseline is only read, and the
candidate shares nothing with it or with the edits. The candidate is decoded
afresh from the baseline's keys, so an unexported field is zero in it; an
exported `koanf:"-"` field, which no key reaches, holds a copy of the
baseline's, as a typed section's does in a load, unless an edit replaces
what holds it. `WithDefaults` applies only when
the baseline is `nil`; otherwise the options apply as they do to `CheckKey`.

What `DecodeEdits` checks is the batch over the baseline, with Load's
hooks, validator and `Validate` method, and nothing else. It does not say
what the next load makes of the same entries: that load reads every source
again, so it sees a lower source that changed since the baseline was
loaded, misses a row the store has deleted since — which a live config used
as the baseline still holds — and applies options the call did not get. A
store whose rows are a load's entries checks a write with that load: build
the candidate table, load it with the options the service loads with, and
commit only the table that load accepts, as the
[settings-store recipe](../examples/cookbook/README.md#settings-a-service-writes)
does. Persisting, authorizing and ordering commits stay the writer's.

### Withholding invalid rows

For a store whose older rows may be invalid, pass a per-source option:

```go
config.WithBaseProvider(store, nil, config.WithInvalidEntries(
    func(ctx context.Context, rejected []config.KeyError) error {
        return reportRejected(ctx, rejected)
    },
))
```

This withholds the source's invalid entries, leaving their lower-precedence
values intact. A provider's entries are its struct sections' fields, checked
field by field; a collection or self-decoding value is one entry. An entry
of `WithBaseEntries` or `WithOverrideEntries` is one stored key.

Entries are judged in the configuration every source makes: the sources
merged in rank order over the defaults, the entries of every source using
the option among them, decoded as `Load` decodes and checked against the
validator's field rules — the default validator's `validate` tags, or
`WithValidator`'s. **An entry is withheld when, beside the entries the
configuration does not fail at, it fails at a key it sets or at a key
holding a value of it that the merge keeps.** A failure at no key, and a
failure at a key that a source not using the option sets above the entries
with nothing of theirs kept beneath it, withholds nothing: the load fails
with it.

- **Where a failure is.** A value that does not decode is at its key, and a
  field rule at its field's. A rule comparing fields (`eqfield`, `ltefield`,
  `required_with`, `eqcsfield`, …) is also at each field its param names,
  never at the struct holding them. A `Validate` method's error, a
  `WithValidator` validator's that holds no field errors, and a failure the
  decoder places beneath a map it decodes whole are at no key.
- **Who is blamed.** A failure at a key is blamed on the entry that set the
  key last: the highest-ranked source to set it, and within a source the
  entry applied last. An entry sets a key when what it merges holds the key,
  or replaces a key holding it whole — a value that is not a map, or a typed
  map or section. An entry whose map merges key by key and holds nothing at
  the key does not: `db: {host: prod-db}` is not blamed for a missing
  `db.password`. A failure at a key is also blamed on each entry beneath it
  whose own key it set last, so stored rows that grow a list past its `max`,
  or a map past its `len`, are judged. An entry the merge cannot set, such
  as `pool[2]` past the next element, is blamed for that.
- **What is withheld.** Each round judges the entries left and tests every
  entry it blames for the first time beside the entries it does not blame.
  An entry blamed there is withheld. One that is not is kept: independently
  valid rows can conflict, and the loader does not choose rows to discard
  to resolve the conflict, so the load fails on it. Rounds repeat until one
  withholds nothing, so an entry left unable to apply by one withheld —
  `pool[2]` once `pool[1]` goes — is found by the next round.

So a value a higher source repairs is no entry's fault, an entry a higher
source sets over is not reported whatever it holds, a lower source's entry
valid beside a higher source's is kept with it, and a valid `cert_file` and
`key_file` pair is kept beside a `min_version` that breaks its `ltefield`
rule, which alone is withheld. Under `max=3`, over two default elements,
entries `pool[2]` and `pool[3]` keep `pool[2]` and withhold `pool[3]`;
two map entries each fitting a `max=2` map beside its default but not
together are both kept, and the load fails on the map's rule.

Entries are judged before secrets, post-load hooks and `Validate` methods,
which run once, in the load. A field rule comparing an entry's key with a
field they fill therefore judges against the value before they fill it, and
a required field they fill blames no entry. Under `WithSkipValidation` only
values that do not decode and entries that cannot be set are blamed.

A load reads every source before it judges any entry, so the callback runs
only once the last source is read: a slow provider delays it, and a source
that fails to read fails the load with nothing reported. The callback
receives sorted key/error pairs on every load, including an empty report;
nil discards reports. Its error fails the load with `ErrSource`.

A judgement merges the sources from the lowest that uses the option up,
decodes the result and runs the field rules. Judging takes one judgement
when it blames no entry and, for k entries it ever blames, at most 2k+1,
however many entries there are: each blamed entry is tested at most once,
and each round but the last withholds at least one, so there are at most k
tests and k+1 rounds. One bad row among a thousand costs three judgements. Each judgement starts from a copy of what lies beneath those
sources and copies the configuration tree once for each source it merges
and once more to decode.

Unknown keys still reach the normal diagnostics, including declared unknown
children of a withheld entry. A `validate` tag the default validator cannot
apply fails the load with `ErrSchema`, whichever entry brings the validator
to it. Read/parser errors and cancellation fail the load, with the cause
the context ended with, and a load whose context has ended reports
nothing. The option applies equally to `WithOverrideProvider`.

### Unknown keys of one source

A store that keeps rows for fields since removed would fail every load
under the strict default. Claim the keys such a source sets that no field
reads, without relaxing the check for every other source:

```go
config.WithOverrideEntries(readSettings, config.WithUnknownEntries(
    func(ctx context.Context, unknown []config.UnknownKey) error {
        logger.WarnContext(ctx, "stored settings no field reads", "keys", unknown)
        return nil
    },
))
```

The keys go to the callback instead of `WithUnknownKeys` or the strict
default: a nil return lets the load go on, an error fails it with
`ErrValidation`, the error kept as its cause, and a nil callback accepts
them silently. It is called at every load and reload that finds any, with
the keys sorted and each one's likely misspelling, and never once the
load's context has ended. `Snapshot.UnknownKeys` lists them beside the
keys the load-wide handler accepts.

A key is unknown exactly as for `WithUnknownKeys`: the decoder leaves it
unused as it decodes the merged sources. A key beneath an interface, one a
`koanf:",remain"` field collects, and the keys a self-decoding value or a
decode hook reads are not unknown. An entry naming nothing config reads is,
and under `WithInvalidEntries` on the same source so are the keys inside a
withheld entry's value. A key is claimed only when every source that set
any part of it (the key, or a key beneath it) uses the option, a source a
higher one set over and an entry that never merged included, and no
default is there; the highest-ranked of those sources reports it.
Otherwise the key stays with `WithUnknownKeys` or the strict default, so
the same key in a file still fails, beneath the claiming source or above
it.

Finding a key's sources costs no decode: the load looks the key up in what
each source merged and in an index of the keys it set that never merged, a
few lookups per source for each unknown key, as deep as the key. The
option applies to `WithBaseProvider`, `WithOverrideProvider` and both entry
options.

### Stored entries

A settings store keyed in this package's key grammar loads through
`WithBaseEntries` or `WithOverrideEntries`, with no translation between
grammars: `port`, `names["a.b"]` (a map key holding a dot),
`addresses["first"].port`, `pool[1].host`. A dotted provider cannot carry
those keys — `a.b` would nest, `["first"]` is not a segment — and
`NewMapProvider` keeps its own grammar for the stores already written in it.

```go
cfg, err := config.Load[AppConfig](ctx,
    config.WithFile("config.yaml"),
    config.WithOverrideEntries(func(ctx context.Context) (map[string]any, error) {
        return loadSettings(ctx, db) // key → value, keys as CanonicalKey spells them
    }),
)
```

`read` is called with the load's context on every load and reload; what it
returns is copied, a nil map is an empty source, and its error fails the
load with `ErrSource`. The entries load among the base or override
providers in the order added. Each entry is one key set over the sources
merged beneath it, as `DecodeKey` sets one over defaults and in the order
`DecodeEdits` applies a batch. Field names resolve through aliases (the load
warns, as for any source) and letter case; `nil` sets the zero value.
Provenance names the entries only for what they set, and beneath a source
set above them their keys are replaced like any lower source's.

An index is a position in the list beneath the entries as each load merges
it. It sets that element, or appends one immediately after the last, and
leaves the other elements to the sources that set them, so a change to the
lower list takes effect at the next reload and is not reported shadowed.
When the lower list shrinks so an index lies beyond its next element — a
file drops `pool[1]` while an entry sets `pool[2].host` — that entry no
longer applies: the load fails, or `WithInvalidEntries` withholds it. Store
a whole list where the list beneath can change.

| Entry | Load |
|---|---|
| Names nothing config reads, or does not parse; or a key inside its value that no field reads | an unknown key, for the unknown-input handler or the source's `WithUnknownEntries` callback, with the field it most likely misspells |
| One value under two spellings (`port`, `PORT`; a field and its alias) | fails with a `*KeyError` matching `ErrValidation` |
| An index past the next element of the list beneath | fails with a `*KeyError` matching `ErrValidation` |
| A key beneath one set to a value that cannot hold it, or keys selecting both elements and fields of one value | fails with a `*KeyError` matching `ErrValidation` |
| A value its field's `format` or `sep` tag cannot read | fails with a `*KeyError` matching `ErrValidation` |

Every refused entry is named, and every message names keys, cut short
beneath a sensitive container, and never a value. Under
`WithInvalidEntries` the refused entries, and the entries the final
configuration fails on as that option judges it, are withheld and reported
instead. The
[settings-store recipe](../examples/cookbook/README.md#settings-a-service-writes)
canonicalizes a write's keys, checks the table it makes with the service's
own load, stores it in one serialized transaction and reloads.

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
