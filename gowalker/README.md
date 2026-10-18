# gowalker

[![Go Reference](https://pkg.go.dev/badge/github.com/ashtonian/pkg/gowalker.svg)](https://pkg.go.dev/github.com/ashtonian/pkg/gowalker)

Build libraries that use Go struct tags. Discover fields, read their tags, and
reuse the same field information when generating help, validating input, or
reading and writing values.

A configuration library might read `env` and `default` tags. A database library
might use `db` tags as column names. Gowalker handles field discovery, duplicate
names and access through pointers; your library decides what the tags mean.

Go 1.27+ · standard library only · MIT

```sh
go get github.com/ashtonian/pkg/gowalker@latest
```

## Read a struct's tags

You can inspect a type without creating a value. This program lists the fields
and the settings a configuration library could use:

```go
package main

import (
    "fmt"

    "github.com/ashtonian/pkg/gowalker"
)

func main() {
    type Settings struct {
        Port  int    `env:"PORT" default:"8080"`
        Label string `env:"LABEL" default:""`
        Debug bool   `env:"DEBUG"`
    }
    engine, err := gowalker.New()
    if err != nil { panic(err) }
    schema, err := engine.Compile[Settings]()
    if err != nil { panic(err) }

    for _, field := range schema.Root().Fields() {
        text, present := field.Tags().Lookup("default")
        fmt.Printf("%s: type=%s env=%q default=%q hasDefault=%t\n",
            field.Name(), field.Type(), field.Tags().Get("env"), text, present)
    }
}
```

Output:

```text
Port: type=int env="PORT" default="8080" hasDefault=true
Label: type=string env="LABEL" default="" hasDefault=true
Debug: type=bool env="DEBUG" default="" hasDefault=false
```

`Fields()` returns field descriptions in declaration order. `Tags()` returns a
`reflect.StructTag`: `Lookup` distinguishes an explicit empty default from no
default at all. This example reads metadata; applying defaults and loading the
environment are jobs for your configuration library.

## Use a tag as the field's name

For a row containing `ID int` tagged `db:"id"`, construct the engine with
`gowalker.New(gowalker.WithTaggedFields("db"))`. After `engine.Compile[Row]()`,
`schema.AccessName("id")` returns a reusable reader/writer for that column.
`id.GetAs[int](row)` returns the row's ID as `(int, error)`.

This preset selects exported fields with explicit names before the first comma.
It skips missing names, empty names and `"-"`, preserves whitespace, and ignores
comma options. Each column is one whole value: the contents of a JSON payload,
for example, cannot introduce more columns. Embedded fields are not promoted.
Conflicting column names return an error during compilation.

See the [complete tagged-row example](docs/usage.md#read-one-column-from-many-rows),
which reads IDs `10` and `20` using one reader. For custom names, aliases or
nested sections, use [custom field rules](docs/usage.md#choose-your-librarys-field-rules).

## Reuse the work in your library

| Step | What you keep | What it does |
|---|---|---|
| `New(options...)` | Engine | Holds your field rules and a bounded type cache |
| `engine.Compile[T]()` | Schema | Describes one Go type without an instance |
| `schema.Root().Fields()` | Field descriptions | Exposes names, tags and declared types |
| `schema.AccessName(name)` | Accessor | Reads or writes the same field across many values |

Use `CompileType` when the type is only known at runtime. Engines and compiled
handles can be shared across goroutines; synchronize access to your own values.
An engine retains metadata, not the rows or configurations passed to it.

For nested locations, `ResolveText("Database.Port")` builds an accessor to keep
and reuse. `AccessName("id.part")` always means one literal field name, including
the dot. For occasional reads and writes using Go field names, `Get`, `GetAs`
and `Set` also accept text paths directly.

## When it helps

Use gowalker when several library operations must agree on field selection,
names and access, or when the same types are used repeatedly. A one-time scan
of a simple flat struct may need only ordinary reflection.

Gowalker exposes tags; it does not decode their values, apply defaults, validate
rules, generate SQL, or reproduce another library's tag conventions. Field
names selected by your rules must be unique; annotation values may repeat.

Named fields can be read or replaced as whole values. Missing ancestors, nil
values and zero values remain distinct. Compilation and walks have finite
limits; even fields treated as whole values retain raw type information and
its compilation cost. See the [API reference](docs/api.md) for these contracts.

## Guides

- [Usage](docs/usage.md): tags, named columns, custom rules, returned values and errors.
- [API reference](docs/api.md): inputs, results, naming, ownership and limits.
- [How it works](docs/architecture.md): field discovery, cached metadata and access.
- [Scope](docs/scope.md): what gowalker and the consuming library each handle.
- [Performance](docs/performance.md): reuse, allocations and measurements.
- [Executable examples](example_test.go): examples checked by `go test`.
