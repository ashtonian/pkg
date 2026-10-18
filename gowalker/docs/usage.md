# Using gowalker

Start with [installation and reading struct tags](../README.md). The examples
below are complete Go programs. Their bodies also appear in the
[executable examples](../example_test.go), and the release check runs the programs
as printed here. Detailed rules live in the [API contracts](api.md).

## Generate help from annotations

A configuration library can describe its supported environment variables and
defaults by inspecting the type. It needs no Settings value and runs no loader
or decoder. Multiple fields may carry the same annotation value.

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

`Lookup` returns `(string, bool)`. Label has an explicit empty default; Debug
has no default tag. Preserve that difference when applying your own rules.
`Tags()` returns the entire raw `reflect.StructTag`, so the same field can also
supply descriptions, validation rules or other library annotations.

## Read one column from many rows

A database helper needs to find the field named by a column and read it from
many rows. `WithTaggedFields("db")` supplies that convention: explicitly named,
exported fields become columns, and each column is read as one whole value.
Tags inside its payload do not contribute more columns.

`AccessName` returns a cached `(*Accessor, error)`. `Get` returns `(Value, error)`;
`Value.Interface()` below is the selected row's `int`. Keep the engine across
calls in your library, and keep the reader for repeated access.

```go
package main

import (
	"fmt"
	"github.com/ashtonian/pkg/gowalker"
)

func main() {
	type Row struct {
		ID int `db:"id"`
	}
	e, err := gowalker.New(gowalker.WithTaggedFields("db"))
	if err != nil {
		panic(err)
	}
	schema, err := e.Compile[Row]()
	if err != nil {
		panic(err)
	}
	id, err := schema.AccessName("id")
	if err != nil {
		panic(err)
	}
	for _, row := range []Row{{ID: 10}, {ID: 20}} {
		value, err := id.Get(row)
		if err != nil {
			panic(err)
		}
		fmt.Println(value.Interface())
	}
}
```

Output:

```text
10
20
```

For a nested path, use `schema.ResolveText("Database.Port")` instead of
`AccessName`. Keep the returned accessor: arbitrary path resolution is not
cached. Use `accessor.GetAs[int](row)` when you want `(int, error)` directly.

Compile `Row` for ordinary entity access: reads accept `Row` or `*Row`, and
writes require `*Row`. Compile `*Row` only when the root pointer itself is part
of the value you want to read or replace; writes then require `**Row`.

## Choose your library's field rules

Use the default engine when tags are annotations. Use `WithTaggedFields` when
explicit tag names identify whole fields, as in the column example. The preset:

- Uses the name before the first comma and ignores the suffix.
- Excludes private fields and missing, empty or `"-"` names.
- Preserves whitespace and literal punctuation in names.
- Rejects duplicate names; does not promote embedded fields.
- Treats each selected field as a leaf, including struct and pointer payloads.

These rules are deliberately specific. For nested configuration sections or
another tag convention, `WithFieldResolver` receives a `reflect.StructField`
and returns `(FieldRule, error)`. A rule can name a field, supply aliases,
exclude it with Skip, expose its children with Inline, or keep it whole with
Atomic. Use `StructTag.Lookup` in the callback when absent and empty differ.
The [custom config-style example](../example_test.go) demonstrates these rules.
The last WithFieldResolver or WithTaggedFields option supplies the resolver.

For example, `validate:"required,min=1"` is a list of rules, not a field name.
Read it with `Tags()` and let the validation library interpret it. A generic
name preset cannot reproduce every library's embedding and tag conventions.

## Read and write a discovered field

After compiling a type, `schema.AccessName("Port")` selects one root field.
`GetAs[int](settings)` returns its current value as `(int, error)`, and
`Set(&settings, 9000)` assigns it. Names and aliases follow the same rules as
field discovery. `AccessName("id.part")` selects that literal name;
`ResolveText("Database.Port")` selects a nested location instead.

If you are already iterating `Fields()`, `schema.AccessField(field)` reuses the
same accessor cache. Both methods follow root pointers. AccessField requires a
field from this schema's visible root fields, rather than a nested or foreign
field. For metadata on a pointer root, follow `Root().Elem()` to its struct.

## Distinguish zero, nil and missing

`Get` reports presence independently of the Go value. In this program,
`Count` exists and holds zero, `Child` exists and is nil, and the other two
locations cannot be reached. All four reads return a nil error.

Each output line is the path followed by `Present`, `Nil` and `Missing` tests:

```go
package main

import (
	"fmt"
	"github.com/ashtonian/pkg/gowalker"
)

func main() {
	type Row struct {
		Count  int
		Child  *Row
		Labels map[string]string
	}
	row := Row{}
	for _, location := range []string{"Count", "Child", "Child.Count", `Labels["absent"]`} {
		v, err := gowalker.Get(&row, location)
		if err != nil {
			panic(err)
		}
		fmt.Println(location, v.State == gowalker.Present, v.State == gowalker.Nil, v.State == gowalker.Missing)
	}
}
```

Output:

```text
Count true false false
Child false true false
Child.Count false false true
Labels["absent"] false false true
```

`GetAs[int]` on `Child.Count` would return `0` and an error matching
`gowalker.ErrMissing`. Reading an unknown field returns `ErrPath`; reading an
out-of-bounds index returns `ErrIndex`. Use `errors.Is`, because errors carry
operation and location details. See [results and errors](api.md#results-and-errors).

## Read the concrete value inside an interface

A terminal declared `any` retains that declaration. Even when it holds an int,
`GetAs[int]` on the interface location returns `ErrType`: assigning an `any`
location to an `int` requires selecting its concrete contents first.

An explicit `path.Deref()` selects those contents. The first output line confirms
the declared-interface read failed with `ErrType`; the second is the concrete int.

```go
package main

import (
	"errors"
	"fmt"
	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/gowalker/path"
)

func main() {
	row := struct{ Value any }{Value: 42}
	e, err := gowalker.New()
	if err != nil {
		panic(err)
	}
	s, err := e.Compile[struct{ Value any }]()
	if err != nil {
		panic(err)
	}
	declared, err := s.ResolveText("Value")
	if err != nil {
		panic(err)
	}
	_, err = declared.GetAs[int](&row)
	fmt.Println(errors.Is(err, gowalker.ErrType))
	concrete, err := s.Resolve(path.Of(path.Field("Value"), path.Deref()))
	if err != nil {
		panic(err)
	}
	value, err := concrete.GetAs[int](&row)
	if err != nil {
		panic(err)
	}
	fmt.Println(value)
}
```

Output:

```text
true
42
```

Intermediate pointers and interfaces are followed automatically. For example,
`"Payload.Name"` can reach `Name` inside an interface holding a struct. Explicit
`Deref` matters when the pointer/interface is the terminal you selected.

## Handle a failed write

`Set` follows Go assignment rules. Allocation and lossless numeric conversion
require options. Here, 256 cannot fit in a `uint8`, so the write returns
`ErrConversion`. The optional `Row` pointer remains nil after that failure.

```go
package main

import (
	"errors"
	"fmt"
	"github.com/ashtonian/pkg/gowalker"
)

func main() {
	type Row struct{ Count uint8 }
	var cfg struct{ Row *Row }
	err := gowalker.Set(&cfg, "Row.Count", 256,
		gowalker.WithPointerAllocation(), gowalker.WithCheckedNumbers(),
	)
	fmt.Println(errors.Is(err, gowalker.ErrConversion), cfg.Row == nil)
}
```

Output:

```text
true true
```

A successful write returns nil. Writes through references update shared storage;
assigning a terminal reference can replace it. Map-held structs and
interface-held values receive the required writeback.
The [write contract](api.md#writes) covers typed nils, map insertion and options.

## Inspect or walk a graph

Use `schema.Root().Fields()` for the visible root-field catalog. The returned
slice is a copy; each field exposes its name, declared type and Go tags.
`DeclaredView()` also includes inline owners, and `DeclaredFields()` includes
raw private/skipped fields without granting access to their values.

Use `schema.WalkType` when only types matter. Use `schema.WalkValue` when you
need actual locations, such as all integers in a map. Both accept a visitor
returning `(Control, error)` and return an error themselves; `Stop` is successful
and returns nil. A type walk reports recursion with `Reference`; a value walk
reports active cycles with `Cycle`.

The [walk-and-update example](../example_test.go) collects concrete paths during
a walk, then resolves and updates them afterward. Do the updates after walking
so traversal does not depend on a graph changing underneath it. See the
[walking contract](api.md#type-and-value-walking) for ordering and limits.

[API contracts](api.md) · [Architecture](architecture.md) ·
[Scope](scope.md) · [Performance](performance.md)
