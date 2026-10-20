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

## Pick a naming convention

The engine decides which fields exist and what they are called. Create one per
convention, once, in a package variable:

| Your users write | Engine |
|---|---|
| Go field names; tags are only annotations | none: use `gowalker.Compile[T]()` |
| `db:"id"`: only explicitly named fields, each one whole value | `gowalker.MustNew(gowalker.WithTaggedFields("db"))` |
| `json:"id,omitempty"`: the members `encoding/json` writes — untagged fields keep Go names, embedded structs are promoted, collisions resolve by depth, `json:"-,"` names `-` | `MustNew(WithTaggedFields("json", UntaggedName(goName), PromoteEmbedded(), DashCommaName()), ResolveByDepth())` |
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
| `gowalker.NewTypeCache(engine, build)` | Your per-type metadata, built once | Your library's hot path |
| `schema.AccessName(name)` | `*Accessor`, cached by the schema | Reading or writing one field across many values |
| `schema.ResolveText("server.port")` | `*Accessor`, not cached | Nested locations; keep it yourself |
| `schema.WalkType(visit)` | Every field and nested type, with `Inherited(tag)` | Catalogs, section-wide annotations |
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

## Guides

- [Recipes](docs/usage.md): per-type metadata, naming conventions, nested
  sections, inherited tags, self-decoding types, nil and missing values, errors.
- [API reference](docs/api.md): inputs, results, errors, ownership and limits.
- [How it works](docs/architecture.md): what is compiled, cached and reused.
- [Scope](docs/scope.md): what gowalker does and what your library does.
- [Performance](docs/performance.md): reuse, allocations and measurements.
- [Executable examples](example_test.go): examples checked by `go test`.
