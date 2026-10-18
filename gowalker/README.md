# gowalker

[![Go Reference](https://pkg.go.dev/badge/github.com/ashtonian/pkg/gowalker.svg)](https://pkg.go.dev/github.com/ashtonian/pkg/gowalker)

Inspect Go types, read and write nested values, and walk object graphs through
one reusable reflection API. Libraries supply field names, aliases, inlining and
leaf rules; gowalker handles discovery, access and writeback.

Use it to share field discovery and nested access across a library’s operations.
The library keeps its own decoding, validation and domain behavior.

Go 1.27+ · standard library only · MIT

```sh
go get github.com/ashtonian/pkg/gowalker@latest
```

## Quick start

```go
package main

import (
    "fmt"

    "github.com/ashtonian/pkg/gowalker"
)

type Database struct{ Port int }
type Config struct{ Database *Database }

func main() {
    var cfg Config
    err := gowalker.Set(&cfg, "Database.Port", 8080,
        gowalker.WithPointerAllocation(),
    )
    if err != nil {
        panic(err)
    }
    port, err := gowalker.GetAs[int](&cfg, "Database.Port")
    if err != nil {
        panic(err)
    }
    fmt.Println(port) // 8080
}
```

Reads follow existing pointers without creating anything. `GetAs[T]` returns
`ErrMissing` for an absent map entry or nil ancestor. Use `Get` when you need to
distinguish `Missing`, a reached `Nil`, and ordinary `Present` values such as zero.

Writes require a pointer to the declared root. Assignment follows Go’s assignability rules by default;
`WithCheckedNumbers()` allows lossless numeric conversion. Pointer and map
allocation require separate options. A rejected write leaves the graph unchanged.

## Reuse work in a library

An engine is a reusable schema compiler and cache. Its settings describe a
library's field names, aliases, inlining and leaf types. One-call helpers compile
and resolve on every call. Keep an engine, schema and accessor when operating on
many values or repeatedly accessing the same field. Using `Config` above:

```go
func incrementPorts(configs []*Config) error {
    engine, err := gowalker.New()
    if err != nil { return err }
    schema, err := engine.Compile[Config]()
    if err != nil { return err }
    port, err := schema.ResolveText("Database.Port")
    if err != nil { return err }

    for _, cfg := range configs {
        current, err := port.GetAs[int](cfg)
        if err != nil { return err }
        if err := port.Set(cfg, current+1); err != nil { return err }
    }
    return nil
}
```

Use `CompileType(reflectType)` for runtime types. `GetValue`, `SetValue` and
`WalkValueReflect` accept exact declared `reflect.Value` roots. Generic methods
share the same implementation; types and paths remain checked at runtime.

`schema.Root()` exposes immutable type and field metadata. `WithFieldResolver`
sets names, aliases, skip and inline rules; `WithAtomicType` defines leaves.
Set `FieldRule.Atomic` to treat just that field as a leaf, such as a database
column whose contents need no further inspection. `schema.AccessField(field)`
returns `(*Accessor, error)` for a visible root field obtained from `Lookup` or
`Fields`; repeated calls reuse it without rebuilding a path. A leaf can be read
or replaced as a whole, but paths and walks do not descend into it. Here,
“atomic” means a traversal leaf; it does not add synchronization.

Compilation checks all visible fields. For a direct-column reader, make every
selected column a leaf so nested payload fields do not participate in column
naming. The [tagged-column example](example_test.go) shows this setup.
`WithCaseFoldNames` applies the same Unicode matching to lookup and collision
checks. The default uses exported Go names, exact matching and declared nesting.

See the [compiled examples](example_test.go) for field rules, db tags,
map/interface writeback, recursive types and walking before mutation.

## What reads return

For `type Row struct { Count int; Child *Row }` and `row := Row{}`:

| Call | Result |
|---|---|
| `Get(&row, "Count")` | State `Present`, Type `int`; `Interface()` returns `int(0)` |
| `Get(&row, "Child")` | State `Nil`, Type `*Row`; `Interface()` returns `(*Row)(nil)` |
| `Get(&row, "Child.Count")` | State `Missing`, Type `int`; `Reflect()` is invalid |
| `GetAs[int](&row, "Child.Count")` | `0, ErrMissing` (wrapped; use `errors.Is`) |
| `Get(&row, "Unknown")` | An error matching `ErrPath` |

`Get` returns `(Value, error)`. `Value` also carries the reached path and dynamic
interface type. `GetAs[T]` returns `(T, error)`; it requires assignability to T.
Writes return an error, with nil indicating success. A field declared `any`
keeps that declaration until explicitly unboxed with `path.Deref()`; see the
[interface example](example_test.go).

## Paths and walking

Text paths support fields, quoted string map keys and existing indexes:
`Servers["primary.eu"].Ports[0]`. An empty path selects the root. The
[`path` package](path/path.go) also supports typed keys, arbitrary field names
and explicit dereferencing. Keys retain their Go types.

`schema.WalkType` visits the visible type graph and reports recursive references.
`schema.WalkValue` visits actual values, reports cycles, and supports pruning and
early stop. Both have finite depth/node budgets. Collect paths during a walk and
apply changes afterward.

## Contracts and limits

| Behavior | Contract |
|---|---|
| Sharing | Schemas/accessors can be reused concurrently; callers synchronize live values |
| Reads | Borrow references and preserve terminal pointer/interface types; no decoding |
| Writes | Prepare assignment and optional allocations before committing one location |
| Containers | Map-held structs and boxed interfaces receive writeback; writes through existing references update shared storage |
| Collections | No implicit slice growth, map deletion, wildcard updates or batch transactions |
| Visibility | Safe reflection; no private-field access or automatic embedded-field promotion |
| Resources | Cache bounded by root count, with separate compilation, path and walk limits |

The engine is a structural utility. Codec rules, defaults, redaction, validation,
SQL scanning, cloning and diff semantics belong to consumers.

[Usage examples](docs/usage.md) · [API contracts](docs/api.md) · [Architecture](docs/architecture.md) ·
[Scope and limits](docs/scope.md) · [Performance](docs/performance.md)

## Development

```sh
go test -race ./...
go vet ./...
go test . -run '^$' -bench . -benchmem
```

Ordinary tests include the existing fuzz seed cases. Module archive checks also
download the package into an isolated consumer and run this quick start.
