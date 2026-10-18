# Using gowalker

Start with [installation and a single read/write](../README.md). The examples
below are complete Go programs. Their bodies also appear in the
[executable examples](../example_test.go), and the release check runs the programs
as printed here. Detailed rules live in the [API contracts](api.md).

## Read one field from many rows

Keep one engine for a set of field rules and one schema for each root type.
This example uses `db` tags for column names and skips untagged fields.
`Atomic: true` treats each column as one value, so tags inside a column's
payload do not affect the row's column names.

`Lookup` returns a field handle and a boolean. `AccessField` returns a cached
accessor and an error. `Get` returns a `Value` and an error;
`Value.Interface()` below is an `int` from the selected row.

```go
package main

import (
	"fmt"
	"reflect"
	"strings"
	"github.com/ashtonian/pkg/gowalker"
)

func main() {
	type Row struct {
		ID int `db:"id"`
	}
	e, err := gowalker.New(gowalker.WithFieldResolver(func(field reflect.StructField) (gowalker.FieldRule, error) {
		name, _, _ := strings.Cut(field.Tag.Get("db"), ",")
		return gowalker.FieldRule{Name: name, Skip: name == "" || name == "-", Atomic: true}, nil
	}))
	if err != nil {
		panic(err)
	}
	schema, err := e.Compile[Row]()
	if err != nil {
		panic(err)
	}
	field, ok := schema.Root().Lookup("id")
	if !ok {
		panic("missing id field")
	}
	id, err := schema.AccessField(field)
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
`AccessField`. Keep the returned accessor: arbitrary path resolution is not
cached. Use `accessor.GetAs[int](row)` when you want `(int, error)` directly.

Compile `Row` for ordinary entity access: reads accept `Row` or `*Row`, and
writes require `*Row`. Compile `*Row` only when the root pointer itself is part
of the value you want to read or replace; writes then require `**Row`.

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
