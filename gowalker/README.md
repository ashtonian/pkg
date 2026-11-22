# gowalker

[![Go Reference](https://pkg.go.dev/badge/github.com/ashtonian/pkg/gowalker.svg)](https://pkg.go.dev/github.com/ashtonian/pkg/gowalker)

Struct-tag plumbing for Go libraries that their users configure by annotating
their own structs: configuration loaders, database row mappers, CLI flag
binders, redactors, validators. Gowalker finds the fields, applies your tag
naming convention, caches what your library derives per type, and reads or
writes values by name. Your library decides what the tags mean.

Go 1.27+ · standard library only · MIT

```sh
go get github.com/ashtonian/pkg/gowalker@latest
```

## When to use it

Use gowalker when your library:

- reads its own struct tags, such as `env:"PORT"`, `db:"id,pk"` or `reload:"hot"`,
  from types your users write;
- needs the same field list in several places — loading, help text, validation,
  printing — and those places must agree on names;
- reads or writes the same field across many values;
- has to handle nested sections, embedded structs, pointers, or a tag on a
  section that applies to everything inside it.

Skip it when you are encoding or decoding a format (use `encoding/json` and
friends), when application code reads one tag of one known struct once
(`reflect.TypeFor[T]().FieldByName` is enough), or when you need deep copy,
diff, or struct-to-struct mapping, which gowalker does not provide.

## Quick start: read your tags

No setup is needed for Go field names: `gowalker.Compile` uses a shared,
cached default engine. This program lists what a configuration library would
read from its users' settings struct:

```go
package main

import (
	"fmt"

	"github.com/ashtonian/pkg/gowalker"
)

type Settings struct {
	Port  int    `env:"PORT" default:"8080"`
	Label string `env:"LABEL" default:""`
	Debug bool   `env:"DEBUG"`
}

func main() {
	schema, err := gowalker.Compile[Settings]()
	if err != nil {
		panic(err)
	}
	for _, field := range schema.Root().Fields() {
		env, _ := field.Tag("env")
		text, hasDefault := field.Tags().Lookup("default")
		fmt.Printf("%s %s env=%s default=%q hasDefault=%t\n",
			field.Name(), field.Type(), env.Name, text, hasDefault)
	}
}
```

Output:

```text
Port int env=PORT default="8080" hasDefault=true
Label string env=LABEL default="" hasDefault=true
Debug bool env=DEBUG default="" hasDefault=false
```

`Field.Tag("env")` parses the common `name,option,option` form; `Tags()` is the
raw `reflect.StructTag`, whose `Lookup` tells an explicit empty default from no
default at all. Applying defaults and reading the environment stay in your library.
Compilation refuses a struct tag that `reflect.StructTag` cannot read to its
end, or that repeats a key, so a typo never turns an annotation off silently;
`TagKeys` lists a tag's keys for checking them against the ones you read, a
compiled field or declaration yields the keys compilation already read
(`TagKeys()`), and `MisspeltTagKey(key, known)` names the key of yours a key you
do not read most likely misspells (equal ignoring case, `-` and `_`, or one
character away), for your library to refuse it rather than ignore it.

## Pick a naming convention

The engine decides which fields exist and what they are called. Create one per
convention, once, in a package variable:

| Your users write | Engine |
|---|---|
| Go field names; tags are only annotations | none: use `gowalker.Compile[T]()` |
| `db:"id"`: only explicitly named fields, each one whole value | `gowalker.MustNew(gowalker.WithTaggedFields("db"))` |
| `json:"id,omitempty"`: the members `encoding/json` writes — its tag-name grammar, Go names for untagged fields, promoted embedded structs, collisions resolved by depth, `json:"-,"` naming `-` | `MustNew(WithJSONFields("json"))`; add `DropTies()` to drop tied fields as `encoding/json` does |
| `koanf:"port"`: untagged fields are lowercased, sections nest | `WithTaggedFields("koanf", UntaggedName(strings.ToLower), NestedFields())` |
| Aliases, option-driven inlining such as `,squash`, or tag validation | `WithFieldResolver(func(reflect.StructField) (FieldRule, error))` |

`goName` above is `func(name string) string { return name }`. Fields whose
types decode themselves, such as `time.Time` or a `sql.Scanner`, should be
single values: add `WithAtomicImplementers(reflect.TypeFor[encoding.TextUnmarshaler]())`.
Two fields claiming one name fail compilation with an error naming both.

## The pieces

| Call | Returns | Keep it for |
|---|---|---|
| `gowalker.Compile[T]()` or `engine.Compile[T]()` | `*Schema`, cached by the engine | Every use of `T` |
| `schema.Root().Fields()` | `[]*Field`: `Name`, `Type`, `Tag(key)`, `Tags`, `Index` | Building your metadata |
| `schema.Root().Match(name)` | The field `encoding/json` decodes a member named `name` into, and whether it matched exactly or only by case (`ExactMatch`, `FoldedMatch`, `NoMatch`); `MatchBytes` for a decoder's bytes | Telling a member spelled in another case from an unknown one |
| `schema.Root().Declarations()` | Every declared field with what the view made of it: named, unnamed, shadowed, tied, skipped | Checking your tags on every field; reading fields the view hides, with `KeepHidden` |
| `gowalker.NewTypeCache(engine, build)` | Your per-type metadata, built once | Your library's hot path |
| `schema.AccessName(name)` | `*Accessor`, cached by the schema | Reading or writing one field across many values |
| `schema.ResolveText("server.port")` | `*Accessor`, not cached | Nested locations; keep it yourself |
| `schema.WalkType(visit)` | Every field and nested type, with `Inherited(tag)`; with `WithDeclaredView()`, every named declaration, inline owners and fields that lost their name included | Catalogs, section-wide annotations, checking your tags along every route |
| `schema.At(shape)` | The `*Schema` of a node of the compiled graph, such as a collection's element, cached per node | Accessors relative to a nested value in hand, without compiling its type again |
| `gowalker.Get`, `GetAs`, `Set` | One read or write using Go names | Occasional access |

Engines, schemas, caches and accessors are safe to share across goroutines and
retain metadata only, never the values you pass them. Synchronize your own values.

## Terms

- **Leaf** (also *atomic*): a value read or replaced whole; paths and walks do
  not descend into it. Each `WithTaggedFields` field is a leaf unless
  `NestedFields` is set; `WithAtomicType` and `WithAtomicImplementers` make
  whole types leaves.
- **Inline** / **promote**: expose an embedded struct's fields as if they were
  declared on the outer struct.
- **Present**, **Nil**, **Missing**: a read reached a value (zero included), reached
  a nil, or could not reach the location (absent map entry, nil parent).
- **Route**: the fields from the root to a visited type, used to find tags a
  section passes down to its fields.

## Configuration

An engine is configured once, by options to `New` or `MustNew`; it has no
other settings and reads no environment.

| Option | Default | Effect |
|---|---|---|
| `WithTaggedFields(key, ...TagOption)` | Go field names | Names fields by a tag; `UntaggedName`, `PromoteEmbedded`, `DashCommaName`, `KeepHidden`, `ValidName` and `NestedFields` adjust it |
| `WithJSONFields(key, ...TagOption)` | Go field names | `encoding/json`'s names under `key`; implies `ResolveByDepth` |
| `WithFieldResolver(fn)` | Go field names | Your own `FieldRule` per field |
| `WithCaseFoldNames()` | Exact matching | Lookup and collisions under Unicode simple folding |
| `ResolveByDepth()` | Every collision fails | The shallowest claim wins, then an explicit name |
| `DropTies()` | A remaining tie fails | Every tied field is dropped; requires `ResolveByDepth` or `WithJSONFields` |
| `WithAtomicType(fn)`, `WithAtomicImplementers(ifaces...)` | None | Types treated as leaves |
| `WithCacheCapacity(n)` | 256 | Root schemas retained; 0 retains none |
| `WithUnboundedCache()` | Off | Retains every type |
| `WithLimits(Limits)` | `DefaultLimits()`: 4096 types, 65536 fields, depth 128, 256 path steps | Compilation and access budgets |

Walks take `WithWalkLimits` (default depth 128, 100000 nodes), `WithLeaveEvents`,
`WithUnorderedMaps`, `WithMapKeyOrder` and, for type walks, `WithDeclaredView`; writes take
`WithPointerAllocation`, `WithMapAllocation` and `WithCheckedNumbers`. The
[API reference](docs/api.md) defines each one.

## Build and test

From the repository root: `make test-gowalker` runs the race tests, `make lint`
vets and lints every module, and `make gowalker-release-check` builds the
module from its release archive, runs its tests there, and runs the quick start
above and every program in the [usage guide](docs/usage.md) against their
printed output. `go test -run '^$' -fuzz FuzzWithJSONFields ./gowalker` fuzzes
the `encoding/json` naming against `json.Marshal`.

## Operations

Gowalker is a library: it logs nothing, exports no metrics and starts no
goroutines. What an owner can observe:

- `Engine.CacheStats()` and `TypeCache.CacheStats()`. A rising `Uncached` means
  a type compiles, or its metadata builds, on every call: raise the capacity or
  use `WithUnboundedCache` (see [choosing a cache bound](docs/performance.md#choosing-a-cache-bound)).
- Every failure is a `*gowalker.Error` matching one sentinel (`ErrSchema`,
  `ErrPath`, `ErrType`, `ErrLimit` and the others in the
  [API reference](docs/api.md#results-and-errors)), with the operation, the
  type, the path and the failing step. Compilation errors name the declaring
  type and field: a malformed or repeated struct tag (`*TagError`), two fields
  claiming one name (`*NameConflict`), or a rule the engine refuses.
- `ErrLimit` means a type or path exceeded `Limits` or a walk exceeded
  `WalkLimits`; the budgets bound work, not time or bytes.

## Security

- Gowalker reads and writes exported fields only, through `reflect`'s safe API.
  It never imports `unsafe` and cannot read or allocate private fields, which
  keeps a caller from reaching storage the type's package hid. The unexported
  embedded struct `WithJSONFields` names, as `encoding/json` writes it, is a
  field whose value reads and writes return `ErrAccess`; only its exported
  fields are reachable.
- Error text names operations, Go types, field names and tag keys, which come
  from declarations. It never includes values, map keys or path steps, which
  can be caller data, nor tag values, which can hold secrets such as a default
  password: an unnamed struct type is printed without its tags, in a generic
  type's arguments too, and `TypeText(t)` prints a type that way for your own
  errors. Read `Error.Path` and `Error.Step` to show a location,
  and `Error.Type` or `TagError.Tag` to show a tag, under your own policy.
- Text paths from untrusted input are bounded: `path.Parse` refuses input over
  65536 bytes and resolution refuses more than `MaxPathSteps` steps. Walks of
  untrusted values stop at `WalkLimits`; compilation of a hostile type stops at
  `Limits`.
- Schemas and accessors retain metadata only. An accessor retains the map keys
  of its own path, and nothing retains the values you pass. Synchronize your
  own values; gowalker does not lock them.

## Dependencies

The standard library only, Go 1.27 or later. There are no external services.

## Code map

| File | Responsibility |
|---|---|
| `engine.go` | `Engine`, options, limits, the schema cache and `CacheStats` |
| `tags.go` | Tag parsing, `TagKeys`, `WithTaggedFields` and `WithJSONFields` |
| `tag_spelling.go` | `MisspeltTagKey`: the known tag key a misspelt one means |
| `shape.go` | Compilation: the raw type graph, field rules, inlining, name resolution, the view pass and `Declarations` |
| `schema.go` | `Schema`, `At`, path resolution and cached root-field accessors |
| `route.go` | Static routes for paths of struct fields and pointers |
| `access.go` | Reads and value states |
| `write.go`, `assign.go` | Writes, allocation, `Locate` and checked numeric assignment |
| `walk.go`, `walk_type.go`, `walk_value.go` | Type and value walks |
| `cache.go` | `TypeCache` for your per-type metadata |
| `errors.go` | `Error`, sentinels and `NameConflict` |
| `path/` | Typed paths and their text grammar |

## Guides

- [Recipes](docs/usage.md): per-type metadata, naming conventions, nested
  sections, inherited tags, self-decoding types, nil and missing values, errors.
- [API reference](docs/api.md): inputs, results, errors, ownership and limits.
- [How it works](docs/architecture.md): what is compiled, cached and reused.
- [Scope](docs/scope.md): what gowalker does and what your library does.
- [Performance](docs/performance.md): reuse, allocations and measurements.
- [Executable examples](example_test.go): examples checked by `go test`.
