# Changelog

## [0.14.0](https://github.com/ashtonian/pkg/compare/config/v0.13.1...config/v0.14.0) (2026-11-23)


### ⚠ BREAKING CHANGES

* **config:** A koanf:",remain" field's element type is checked as any field's type is. A misspelt tag key, or a tag value config cannot use, in it or in a remain element within it fails Load, Decode, CheckKey, reloaders, Diff and the generators with ErrSchema before any source is read, and raw inspection refuses one whose structure is invalid; config/v0.13.1 read none of a remain element type's tags.
* **config:** An applier prefix naming a map entry is spelled as CanonicalKey spells the key, so by_int["010"] owns the key Diff reports as by_int["8"]. A prefix naming an entry by text its key type does not read back as that key fails construction with ErrKey; config/v0.13.1 took a string-kind key type's prefix text as written.
* **config:** In config/v0.13.1 Tree, TreeFunc, RedactedTree, RedactedMap and PrintRedacted (table, JSON and YAML) walked declared fields only and omitted every key a koanf:",remain" field collects. They now show them. Tree nests each collected key and value as the source holds it, so the tree loads back. TreeFunc passes each key a non-sensitive remain field collects as a leaf with the remain field as its Field, its Sensitive set by its name as any key's is, and passes a sensitive remain field once, whole, at vault[*]: a map the policy returns sets keys of the section. RedactedTree, RedactedMap and PrintRedacted show a non-sensitive remain field's keys with their values redacted by the usual rules (plugins.foo=1, plugins.api_token=[REDACTED], plugins.endpoint=postgres://u:[REDACTED]@db/x) and a sensitive remain field as vault[*]=[REDACTED]; no value config/v0.13.1 showed is hidden or changed, and no key or value a sensitive remain field collects appears in redacted output.
* **config:** In config/v0.13.1 Diff, reloads and appliers did not see what a koanf:",remain" field collects: a Diff whose only differences were remain entries returned no changes, WithReloadTagChecks accepted a reload:"live" remain field with no applier, WithApplier refused the prefix plugins.foo ('plugins has no field "foo"'), LoadSnapshot's Provenance and PrintExplain had no row for a collected key, and Shadowed was empty when base and override both set plugins.foo. Each collected key is now a key of its section, compared after the section's fields by name, with the remain field as its Field (Path plugins[*]) and routed by the remain field's reload tag: reload checks fail with "live key plugins[*] has no applier", the prefix plugins.foo is accepted, Provenance and PrintExplain show plugins.foo with its source, and Shadowed lists plugins.foo. A sensitive remain field is compared whole: Diff reports one redacted change at vault[*], the one Change.Key that holds a pattern, provenance gives it one row at vault[*], Shadowed names only vault, and applier prefixes beneath it are refused as beneath a sensitive map.
* **config:** A koanf:",remain" field's sensitive and reload tags are checked as any field's: a remain field tagged sensitive other than "true" or "false", with an invalid reload class, or with a reload tag in a section type two keys share fails with ErrSchema. A validation error in a remain field's map names the key it collects (labels.eng(min=3)), or only its struct when the remain field is sensitive (vault(min)), where config/v0.13.1 named the struct with "(exact location unavailable)". A value under a key a map[string]any remain field collects merges as an interface field's: a struct that marshals to text, such as netip.Addr, set by a map source or WithDefaults decodes as its text where config/v0.13.1 decoded a zero value.
* **config:** A source that writes one entry of a map keyed by an integer, bool, float, time.Duration or text-method type under two spellings (by_int: {"010": a, "8": b}) fails the load with ErrValidation naming by_int["8"] (as "010" and "8"); config/v0.13.1 loaded one of the two, a or b, at random. The message for a field spelled two ways reads "set one key" where it read "set one field". A WithDecodeHook for such a key type receives a source's key as its key's text ("8", not "010") wherever the built-in decoding reads it, and as written only where the built-in decoding cannot.
* **config:** PrintRedacted, RedactedMap, Diff, the changes a reload reports and appliers receive, and validation failures name a key beneath an interface as a field wherever it can be one: extra.x, extra.l[0].y and tags["t"].u for Extra any and Tags map[string]any, where config/v0.13.1 wrote extra["x"], extra["l"][0]["y"] and tags["t"]["u"]. A key that cannot be a field stays quoted (extra["a.b"]). Code matching Change.Key or RedactedMap keys beneath an interface by the old spelling no longer matches.
* **config:** A struct field keyed "$" (koanf:"$") is refused with ErrSchema wherever its type's schema is read, where config/v0.13.1 read it as a key. A source key spelled "$" is named ["$"] in an unknown-key report.
* **config:** A map keyed by an integer, bool, float, time.Duration or a type with MarshalText and UnmarshalText (slog.Level, netip.Addr) is walked entry by entry, as a map keyed by strings is; config/v0.13.1 took it as one value. Diff, and the changes a reload reports and appliers receive, name each changed key inside an entry (by_int["80"].host, old h1, new h2), where v0.13.1 gave one change at by_int with [REDACTED] on both sides. An Applier prefix may name such an entry or a pattern beneath it (by_int["80"], by_int[*].port), which v0.13.1 refused as reaching beneath a map "whose keys are not compared one by one". The reload tags of the element's fields apply per entry: a change to by_int["80"].port tagged reload:"live" carries Field.Reload live beside a restart-tagged host, so WithReloadTags routes it live, where v0.13.1 reported one change at the map with the map field's class (restart when untagged). WithReloadTagChecks counts the element's live fields as live keys: a reloader with no applier owning by_int[*].port now fails construction ("live key by_int[*].port has no applier"), and one whose applier owns only by_int now builds, where v0.13.1 built the first and refused the second ("applier owns no live key"). FieldFor(`by_int["80"].host`) returns the element's field (path by_int[*].host, its own reload and sensitive tags), where v0.13.1 returned the map field. PrintRedacted, RedactedMap, Diff and PrintEnv show each entry's values and redact only what a sensitive tag or a credential-like field name or key covers; v0.13.1 printed every such map as one [REDACTED] value, whatever its tags, so values it hid (map[int]string{1: "x"} prints scalars["1"] x) are now shown, and a field tagged sensitive:"true" in the element is redacted per entry. Usage lists one row per element field (by_int[*].host, reload and sensitivity per field) instead of one row for the map. The JSON Schema gives such a map its element's schema as additionalProperties, x-reload and writeOnly per field, in place of {"type": "object", "x-config-type": ...}; neither version sets propertyNames. PrintTemplate writes the element's keys under a <name>: entry. PrintEnv names each entry's key and says no variable sets it, where v0.13.1 offered APP_BY_INT=, a variable no load could read. No variable sets such an entry in either version. A key unknown inside an entry is named in the key grammar, by_int["80"].bogus, where v0.13.1 wrote by_int[80].bogus, which ParseKey reads as an index. CheckKey and DecodeKey refuse a key no field of the element reads (by_int["80"].bogus) or whose entry text the decoder cannot read (by_int["080"]) as an Unknown *KeyError matching ErrKey, where v0.13.1 reported "invalid value for key" matching ErrValidation.
* **config:** WithInvalidEntries judges entries in the configuration every source makes, where v0.13.1 judged each entry alone against the defaults, Validate methods included. An entry is withheld only when, beside the entries the configuration does not fail at, it fails at a key it sets or at a key holding a value of it the merge keeps (a list it grows past its max, say), and only values that do not decode and the validator's field rules judge it. A failure at no key (a Validate method's error, a WithValidator error holding no field errors) and a failure at a key a higher source not using the option sets, with no entry's value kept beneath it, now withhold nothing: the load fails with them. An entry invalid against the defaults but valid in the final configuration, such as one that needs another source's value, is kept. report runs once every source is read rather than after each source's read, and not at all when a source fails to read. The defaults no longer have to validate before any source is read: the "invalid baseline for provider entry validation" failure is gone, and defaults nothing repairs fail the load on the final configuration. Entries that each pass beside the rest but break a rule together are kept and fail the load: with Min validate:"ltefield=Max", Max default 10, a file setting max: 50 and an override provider under WithInvalidEntries setting min: 20 and max: 15, v0.13.1 withheld min and loaded {Min: 0, Max: 15}; the load now fails on min(ltefield=max).
* **config:** A struct held in an array of one or more elements that a validate:"dive" reaches is checked as a struct held by value: a dive its own fields' types cannot take (keys after a dive on a slice, a dive on an int) fails every load, reload and key check with ErrSchema naming the field and the rule, before any source is read. config/v0.13.1 loaded such a config while the element's field was empty or nil, and panicked once it held something to walk. An undefined rule or a dive on an int in such an element, which panicked on every load at config/v0.13.1, is that ErrSchema too.
* **config:** A validate tag whose dive its field's type cannot take (dive on an int, keys after a dive on a slice, a third dive on a [][]int, a dive among the keys rules of a string-keyed map, a dive on a map's int elements), on a field the default validator reads on every validation, now fails every load, reload and key check with ErrSchema naming the field and the rule, before any source is read. config/v0.13.1 loaded such a config while the field was empty, nil, or zero beneath omitempty, panicked once it held something to walk, and failed a nil pointer field carrying the dive with ErrValidation. Remove the dive the type cannot take, or correct the tag. A config validated with WithValidator or loaded with WithSkipValidation is not checked.
* **config:** a struct that embeds a ForeignTagDeclarer no longer has its own fields' tag keys exempted by the embedded struct's list. Every checked entry point — a load, reloaders, CheckKey, the generators and checked redaction — refuses such a key on the embedding struct's fields with ErrSchema. Declare a ForeignTagKeys method on the embedding struct to list the keys its own fields carry.
* **config:** TreeFunc with a non-nil leaf callback returns ErrSchema, and no tree, for a type reaching a tag key that misspells one config reads. Fix the key, or list another library's key in the declaring struct's ForeignTagKeys method; Tree still exports such a type.
* **config:** A key inside a CheckKey or DecodeKey value that names nothing config reads (primary.bogus in primary = {"bogus": 1}) is now a *KeyError at that key with Unknown set, matching ErrKey, where config/v0.13.1 returned one at the edited key matching ErrValidation.
* **config:** A typed map keyed by string or by a predeclared integer type, and a typed section, that WithDefaults or a provider sets (a map[string]Server, a map[string]bool, a map[int]Server, a Server), and a map a Defaulter's SetDefaults sets that is keyed by a predeclared integer type or keyed by string and drops an entry its field's default tag sets (Labels `default:"a=1,b=2"` set to {c: 3}), replace what the layers beneath them set, as before. What a higher source sets beneath them key by key now merges into them instead of replacing them: a file, an environment variable naming a key beneath them (APP_LABELS_D), an override provider's map[string]any, a flag that sets one field of a section, and a key edit in CheckKey or DecodeKey. A file's `labels: {d: 4}` over that Defaulter's map now loads {c: 3, d: 4} where config/v0.13.1 loaded {d: 4}, and a file's `origins: {example.com: true}` over a default map[string]bool{"*": true} loads both entries. A higher source can add and change entries but no longer remove one; an entry set to null keeps its key at the element's zero value. To let a higher source narrow a default, give the default as a list, which a higher source replaces whole; treat an entry's zero value ("*": false) as absent; or set the map to null in one source and to the narrowed map in a source above it. A higher source's typed value still replaces them whole, as in config/v0.13.1: a flag that sets a whole map (kongprovider hands on Kong's typed map), a provider's typed map or section, and an environment variable that sets the whole map (APP_LABELS=d=4). A map keyed by a named type (slog.Level, time.Duration, or a string or integer type of the program's own) keeps its own keys and is replaced whole when WithDefaults or a provider sets it, and when SetDefaults sets one keyed by a named integer type or by a named string type that drops an entry its default tag sets, as in config/v0.13.1; one keyed by a named string type that SetDefaults only adds to merges with a higher source entry by entry, as in config/v0.13.1. A key edit in CheckKey or DecodeKey whose value is a map merges into what the layers beneath it set there key by key, as a file's map does, where config/v0.13.1 replaced it: with Host `default:"dflt" validate:"required"`, primary = {"port": 6} now checks as {Host: dflt, Port: 6} where config/v0.13.1 failed with ErrValidation.
* **config:** WithForeignTagKeys is removed; list the keys from a ForeignTagKeys() []string method on the struct declaring the field. A tag key that misspells one config reads (defualt, sensitve, envRef), unless the declaring struct lists it from ForeignTagKeys, is now refused with ErrSchema by every entry point that did not refuse it in config/v0.13.1: NewReloaderFunc; the populator SecretsFrom returns; Usage, PrintUsage, JSONSchema, PrintTemplate, PrintEnv, PrintExplain and Diff; PrintRedacted, RedactedMapChecked and RedactedTreeChecked; and RedactedMap and RedactedTree, which return nil. The generators write no output when they refuse. Usage and PrintUsage also return the ErrSchema a load returns for a malformed map default. Tree, TreeFunc without a callback, Fields, FieldsChecked, FieldFor, KeyOf, KeyOfValue, MatchFields and DefaultsFromTags still read the type, to diagnose it.
* **config:** A load whose context has ended returns an error that wraps ctx.Err() and its cause ("loading config abandoned: …") where config/v0.13.1 returned ctx.Err() itself, so compare it with errors.Is, not ==. A load whose context ends while its validator runs fails that way rather than with ErrValidation. NewReloaderFunc and NewReloader fail when their context has ended before the first load, or ends during it or the ApplyInitial appliers, where config/v0.13.1 published generation 1: the error wraps ctx.Err(), and is an *AbandonedApplyError naming the initial appliers that applied the configuration when any did.

### Features

* **config:** close the config review batch ([#359](https://github.com/ashtonian/pkg/issues/359)) ([0eeae29](https://github.com/ashtonian/pkg/commit/0eeae292367f3410f2a1be10d78f9f8fda829bbf)), refs [#318](https://github.com/ashtonian/pkg/issues/318) [#307](https://github.com/ashtonian/pkg/issues/307) [#308](https://github.com/ashtonian/pkg/issues/308) [#313](https://github.com/ashtonian/pkg/issues/313) [#312](https://github.com/ashtonian/pkg/issues/312) [#310](https://github.com/ashtonian/pkg/issues/310) [#306](https://github.com/ashtonian/pkg/issues/306) [#319](https://github.com/ashtonian/pkg/issues/319) [#311](https://github.com/ashtonian/pkg/issues/311) [#314](https://github.com/ashtonian/pkg/issues/314) [#309](https://github.com/ashtonian/pkg/issues/309) [#320](https://github.com/ashtonian/pkg/issues/320)

## [0.13.1](https://github.com/ashtonian/pkg/compare/config/v0.13.0...config/v0.13.1) (2026-11-22)


### Bug Fixes

* **config, parse:** error text leaves tag values out; config compiles a type once and checks reload tags on gowalker's declared-view walk; on gowalker v0.8.0 ([#349](https://github.com/ashtonian/pkg/issues/349)) ([f6a39ca](https://github.com/ashtonian/pkg/commit/f6a39ca1c551cfeeeeb625851c58161e700fd48d)), refs [#302](https://github.com/ashtonian/pkg/issues/302) [#303](https://github.com/ashtonian/pkg/issues/303) [#304](https://github.com/ashtonian/pkg/issues/304) [#305](https://github.com/ashtonian/pkg/issues/305) [#315](https://github.com/ashtonian/pkg/issues/315)

## [0.13.0](https://github.com/ashtonian/pkg/compare/config/v0.12.0...config/v0.13.0) (2026-11-22)


### ⚠ BREAKING CHANGES

* **parse:** document members on gowalker's WithJSONFields and misspelt tag keys refused; config on gowalker's matcher ([#300](https://github.com/ashtonian/pkg/issues/300))

### Features

* **parse:** document members on gowalker's WithJSONFields and misspelt tag keys refused; config on gowalker's matcher ([#300](https://github.com/ashtonian/pkg/issues/300)) ([35cc7f0](https://github.com/ashtonian/pkg/commit/35cc7f0911b70804f2db1960441780db1138cf63))

## [0.12.0](https://github.com/ashtonian/pkg/compare/config/v0.11.1...config/v0.12.0) (2026-11-22)


### ⚠ BREAKING CHANGES

* a struct tag reflect cannot read whole (an unquoted value, a key written twice) is refused with ErrSchema wherever the type is used, and a load, reload or CheckKey refuses a tag key one edit from one config reads, or equal to one ignoring case, '-' and '_', such as defualt or envRef. Name another library's keys that close to config's with WithForeignTagKeys.

### Features

* gowalker names every member encoding/json writes and checks tag-key spelling; parse and config on gowalker v0.6.0 ([#298](https://github.com/ashtonian/pkg/issues/298)) ([b505c00](https://github.com/ashtonian/pkg/commit/b505c0025d65e5fb7299f6faee7acbbd3bb26781)), refs [#288](https://github.com/ashtonian/pkg/issues/288)

## [0.11.1](https://github.com/ashtonian/pkg/compare/config/v0.11.0...config/v0.11.1) (2026-11-22)


### Bug Fixes

* **config:** keep netip defaults whole, refuse a second Reloader.Run, drive reloader tests by events ([#247](https://github.com/ashtonian/pkg/issues/247)) ([c5f9316](https://github.com/ashtonian/pkg/commit/c5f9316322f8cae8556e35b65e094815a8df5af6)), refs [#231](https://github.com/ashtonian/pkg/issues/231) [#228](https://github.com/ashtonian/pkg/issues/228) [#230](https://github.com/ashtonian/pkg/issues/230) [#186](https://github.com/ashtonian/pkg/issues/186)

## [0.11.0](https://github.com/ashtonian/pkg/compare/config/v0.10.0...config/v0.11.0) (2026-11-19)


### ⚠ BREAKING CHANGES

* **config:** infer credential names by whole words ([#191](https://github.com/ashtonian/pkg/issues/191)): a field or map key whose name only contains a credential word's letters (Keyspace, Author, Tokenizer, AuthenticationMode) is no longer inferred sensitive, so its value appears in redacted output and its default in usage, templates, JSON Schema and flag help. Tag such a field sensitive:"true" to keep it redacted. Names now inferred that were not before: those with the words pass, passwd, pwd, passphrase or cred.
* **config:** trim the elements of lists split from a string ([#191](https://github.com/ashtonian/pkg/issues/191)): elements of a list set from a string, and entries of a map set from a key=value string, are trimmed of surrounding white space, and empty ones are dropped: "a, ,b" sets [a b], not [a " " b]. Write a list in a config file as a list to keep such elements.
* **config:** add caller columns to usage tables, and Reload only when it informs ([#191](https://github.com/ashtonian/pkg/issues/191)): PrintUsage's table and Markdown leave out the Reload column when every key's reload class is restart. A program parsing those formats should read the header, or use Usage or the JSON format, whose reload field is unchanged.

### Features

* **config:** add caller columns to usage tables, and Reload only when it informs ([#191](https://github.com/ashtonian/pkg/issues/191)) ([1ccd869](https://github.com/ashtonian/pkg/commit/1ccd869759a678532ef8e27fa7a107ce40067cef))
* **config:** trim the elements of lists split from a string ([#191](https://github.com/ashtonian/pkg/issues/191)) ([30d05dc](https://github.com/ashtonian/pkg/commit/30d05dc4e7987b86048b70e27489c9a4505c255c))


### Bug Fixes

* **config:** infer credential names by whole words ([#191](https://github.com/ashtonian/pkg/issues/191)) ([7604891](https://github.com/ashtonian/pkg/commit/760489185aaf2d6cf52ef40390a203be906e9165))
* **config:** name cross-field rule params by key in validation errors ([#191](https://github.com/ashtonian/pkg/issues/191)) ([a6db6d8](https://github.com/ashtonian/pkg/commit/a6db6d8a76969a14912f2c093f3b69c8a345570c))
* **config:** write the URL userinfo marker unescaped ([#191](https://github.com/ashtonian/pkg/issues/191)) ([35b7ef4](https://github.com/ashtonian/pkg/commit/35b7ef4ba4f01699af6052dc1a05e845237ac5b6))

## [0.10.0](https://github.com/ashtonian/pkg/compare/config/v0.9.0...config/v0.10.0) (2026-11-13)


### ⚠ BREAKING CHANGES

* **config:** set defaults in code with Defaulter ([#182](https://github.com/ashtonian/pkg/issues/182)): DefaultProvider and ConfigDefaults are removed; implement Defaulter's SetDefaults instead. SourceSchemaDefaults ("schema_defaults") is replaced by SourceCodeDefaults ("code_defaults").
* **config:** report inputs a higher-precedence source shadows ([#180](https://github.com/ashtonian/pkg/issues/180)): every NewReloader load reads an OriginProvider's annotations, not only one WithProvenance, so a provider whose annotations are invalid fails the reloader with ErrSource.

### Features

* **config:** report inputs a higher-precedence source shadows ([#180](https://github.com/ashtonian/pkg/issues/180)) ([f7ede63](https://github.com/ashtonian/pkg/commit/f7ede6395f439e4d99fcd04bda582aa2eccd2894)), closes [#177](https://github.com/ashtonian/pkg/issues/177)
* **config:** set defaults in code with Defaulter ([#182](https://github.com/ashtonian/pkg/issues/182)) ([5b33c23](https://github.com/ashtonian/pkg/commit/5b33c23010c05d4994ec30ec06ffa3d494a2bf98)), closes [#178](https://github.com/ashtonian/pkg/issues/178)

## [0.9.0](https://github.com/ashtonian/pkg/compare/config/v0.8.1...config/v0.9.0) (2026-11-11)


### ⚠ BREAKING CHANGES

* **config:** Source has a Prefix field, set for every variable read under a prefix, so code comparing a whole environment Source must include it. Field.EnvVars, Usage and JSONSchema take variadic prefixes: calls compile unchanged, function values of the former types do not. UsageEntry.AlsoEnv lists the broader prefixes' variables before an env tag's.

### Features

* **config:** read environment variables under layered prefixes ([65fff05](https://github.com/ashtonian/pkg/commit/65fff0555d9b245840d9242aef1bb8aabdb431bb))


### Bug Fixes

* **config:** name only variables that can set a key in usage and templates ([e71292e](https://github.com/ashtonian/pkg/commit/e71292ede64e5143235d76ea227560e5ad8411d9))

## [0.8.1](https://github.com/ashtonian/pkg/compare/config/v0.8.0...config/v0.8.1) (2026-11-06)

### Bug Fixes

* Preserve duration validation bounds in usage metadata and JSON Schema annotations as exact integer nanoseconds, and render them as durations in Markdown. Conditional bounds and sensitive-field withholding remain intact; runtime validation is unchanged. ([#124](https://github.com/ashtonian/pkg/pull/124))

## [0.8.0](https://github.com/ashtonian/pkg/compare/config/v0.7.0...config/v0.8.0) (2026-11-06)

### Features and fixes

* Share conditional validation constraints between JSON Schema and usage metadata, preserving `omitempty` ordering, nullable values and exact integer bounds. Expose value, length, item and property bounds in `UsageEntry` and Markdown.
* Add root `DefaultProvider.ConfigDefaults()` declarations shared by loading, usage, JSON Schema and templates, with explicit precedence and schema validation.
* Add `CheckKey` and typed `DecodeKey` for source-free candidate validation, and per-provider `WithInvalidEntries` reporting that preserves lower-precedence values and final combined validation.
* Apply `RedactText` credential-shape filtering across redacted text/byte diagnostics; withhold credential-bearing defaults from metadata and templates while preserving raw trees ([#122](https://github.com/ashtonian/pkg/pull/122)).

## [0.7.0](https://github.com/ashtonian/pkg/compare/config/v0.6.2...config/v0.7.0) (2026-11-06)

### Breaking changes

* Withheld usage defaults are omitted from JSON and marked explicitly. Unknown-input JSON uses lowercase field names and omits `reflect.Type`. Invalid `reload` and `envref` tags fail schema construction.

### Features and fixes

* Expose inherited reload metadata and opt-in restart/live routing through `WithReloadTags` and ownership checks through `WithReloadTagChecks`.
* Add structured `Usage`, Markdown formatting, canonical `ParseKey`, and raw/per-leaf `Tree` projections with explicit serialization limits.
* Publish accepted unknown-input reports with their snapshots and claim referenced environment variables with `envref:"true"`, exact names or a predicate before callbacks and logging.
* Count adjacent transpositions in suggestions and apply credential-name sensitivity according to the value's type ([#114](https://github.com/ashtonian/pkg/pull/114)).

## [0.6.2](https://github.com/ashtonian/pkg/compare/config/v0.6.1...config/v0.6.2) (2026-10-19)


### Bug Fixes

* **config:** report key conflicts with gowalker's diagnostics ([15f07c9](https://github.com/ashtonian/pkg/commit/15f07c9a9f8e0c956dd9ec5d75ff5e9f12d507c4))

## [0.6.1](https://github.com/ashtonian/pkg/compare/config/v0.6.0...config/v0.6.1) (2026-10-19)


### Bug Fixes

* **config:** never watch the filesystem root through a root-level symlink ([1c225bb](https://github.com/ashtonian/pkg/commit/1c225bb134a658d3cf295b1a84cdff8f7a2fd25f))

## [0.6.0](https://github.com/ashtonian/pkg/compare/config/v0.5.1...config/v0.6.0) (2026-10-18)


### Features

* **config:** reuse field catalogs and match reflected targets in one traversal ([00233e4](https://github.com/ashtonian/pkg/commit/00233e4646aa66ff13e9717b6eda521414316256))

## [0.5.1](https://github.com/ashtonian/pkg/compare/config/v0.5.0...config/v0.5.1) (2026-10-18)


### Bug Fixes

* **config:** replace retracted MIME detection dependency ([948b1a2](https://github.com/ashtonian/pkg/commit/948b1a261bdfe01bf79d6e14e864e2a2843bdb96))

## [0.5.0](https://github.com/ashtonian/pkg/compare/config/v0.4.0...config/v0.5.0) (2026-10-18)


### ⚠ BREAKING CHANGES

* **config:** config now requires Go 1.27 or later.

### Features

* **config:** adopt shared reflection metadata ([93233a4](https://github.com/ashtonian/pkg/commit/93233a420f3e84da71d53902bfd8b2d0f454e823))

## [0.4.0](https://github.com/ashtonian/pkg/compare/config/v0.3.1...config/v0.4.0) (2026-10-14)


### Features

* **config:** Decode runs a map through the load pipeline ([2b8f49e](https://github.com/ashtonian/pkg/commit/2b8f49e3a6c106c1d4f658963c3bd643305eee69))
* **config:** format:"json" lets one variable set a section, map or list as JSON ([54d2aff](https://github.com/ashtonian/pkg/commit/54d2aff4a657d69378052886be1626c159815a29))


### Bug Fixes

* **config:** attribute keys beneath a variable set whole to that variable ([9d40559](https://github.com/ashtonian/pkg/commit/9d40559d8687ceeade2351c1311a24ea50d62123))

## [0.3.1](https://github.com/ashtonian/pkg/compare/config/v0.3.0...config/v0.3.1) (2026-10-14)


### Bug Fixes

* **config:** promote an unexported embedded struct's fields when it is squashed ([2041f49](https://github.com/ashtonian/pkg/commit/2041f4965eeb00dd1c1608d7e1596cef2d5e54a6))

## [0.3.0](https://github.com/ashtonian/pkg/compare/config/v0.2.0...config/v0.3.0) (2026-10-14)


### Features

* **config:** sep tags split a list's strings on a separator of the field's ([c490f1e](https://github.com/ashtonian/pkg/commit/c490f1edcecb283ee51ecc58f3c7ed6be23dd57f))


### Bug Fixes

* **config:** keep secrets out of diagnostics, and make the tags work together ([b6bffa7](https://github.com/ashtonian/pkg/commit/b6bffa7f93e194a5b18e59935f49d76025eb3257))

## [0.2.0](https://github.com/ashtonian/pkg/compare/config/v0.1.0...config/v0.2.0) (2026-10-14)


### ⚠ BREAKING CHANGES

* **config:** WithFileParser takes the suffix it applies to. To keep one parser for every file, WithFileParser(p) becomes WithFileParser("", p) together with WithFileParser(".yaml", nil), WithFileParser(".yml", nil) and WithFileParser(".json", nil): WithFileParser("", p) alone sets the parser for files no other suffix matches, and .yaml, .yml and .json files keep their built-in parsers. A .json file is parsed as JSON, not YAML, so a .json file holding YAML now fails; WithFileParser(".json", nil) restores YAML for it.

### Features

* **config:** alias tags keep a renamed key reading deployed files ([e87e616](https://github.com/ashtonian/pkg/commit/e87e616e80e635cbfb98963761e87cc44cdd173f))
* **config:** default tags and strings fill maps as key=value entries ([62802aa](https://github.com/ashtonian/pkg/commit/62802aa7709684f94439e862af675f74ec6a6858))
* **config:** env tags name more variables for a field ([c2ebcaf](https://github.com/ashtonian/pkg/commit/c2ebcaf9a05d7c4b3d656f8796ca4000fbcbba1b))
* **config:** example tags show a sample value where there is no default ([71bd728](https://github.com/ashtonian/pkg/commit/71bd728301fe99e7cdab6133c5f1a0f0aa6f13a7))
* **config:** parse each config file by the suffix of its name ([#2](https://github.com/ashtonian/pkg/issues/2)) ([b446990](https://github.com/ashtonian/pkg/commit/b4469906fef00ea6353e0f1de4008634bae49d12))
* **config:** report unknown keys as an UnknownKeysError, each with the field it likely misspells ([#1](https://github.com/ashtonian/pkg/issues/1)) ([100b7ff](https://github.com/ashtonian/pkg/commit/100b7ffa246ec6bf76226e1d281b9ca22942e786))
* **config:** sensitive:"false" exempts a field its name wrongly redacts ([6e19aa2](https://github.com/ashtonian/pkg/commit/6e19aa2c5b5a1f81eabee038de0a38d385ee3727))
* **config:** WithUnknownKeys decides what input no field reads does ([909fee4](https://github.com/ashtonian/pkg/commit/909fee40bb4fde6d735be1edd6471a2931ead916))


### Bug Fixes

* **config:** split comma-separated strings into slices of any element type ([a911638](https://github.com/ashtonian/pkg/commit/a9116383d18b9eb59664852ce33ce6cbb0d7c772))

## 0.1.0 (2026-10-14)

First release as `github.com/ashtonian/pkg/config`: typed loading from defaults, files, the environment and any koanf provider through one validated pipeline; hot reload that publishes only a config that passed; appliers that push changes into the running process and report what waits for a restart; `Diff`, provenance, redacted printing, config file templates and an advisory JSON Schema.
