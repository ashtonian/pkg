# Recipes

Each recipe is a complete program with its exact output; the release check runs
them against the published module. Start with the [quick start](../README.md).
Detailed rules live in the [API reference](api.md).

| Your library needs to… | Recipe |
|---|---|
| Work out its metadata once per type and reuse it | [Build per-type metadata once](#build-per-type-metadata-once) |
| Follow `json`-style naming with embedded structs | [Follow a naming convention](#follow-a-naming-convention) |
| Address nested sections by tag names | [Nested sections and paths](#nested-sections-and-paths) |
| Apply a tag on a section to every field inside it | [Tags a section passes down](#tags-a-section-passes-down) |
| Treat `time.Time`-like types as single values | [Types that decode themselves](#types-that-decode-themselves) |
| Tell zero, nil and unreachable apart | [Zero, nil and missing](#zero-nil-and-missing) |
| Read what an interface holds | [The value inside an interface](#the-value-inside-an-interface) |
| Write safely | [A failed write changes nothing](#a-failed-write-changes-nothing) |
| Report bad tags clearly | [Schema errors name the fields](#schema-errors-name-the-fields) |

## Build per-type metadata once

A database helper derives the same things for every row type: its columns, and
how to read the primary key. `NewTypeCache` runs your function once per type,
from the engine's compiled schema, and shares the result across calls and
goroutines. Errors are cached too, so a misconfigured type fails fast every time.

```go
package main

import (
	"fmt"

	"github.com/ashtonian/pkg/gowalker"
)

type table struct {
	columns []string
	pk      *gowalker.Accessor
}

var tables = gowalker.NewTypeCache(gowalker.MustNew(gowalker.WithTaggedFields("db")),
	func(s *gowalker.Schema) (table, error) {
		var t table
		for _, field := range s.Root().Fields() {
			t.columns = append(t.columns, field.Name())
			if tag, _ := field.Tag("db"); tag.Has("pk") {
				pk, err := s.AccessField(field)
				if err != nil {
					return table{}, err
				}
				t.pk = pk
			}
		}
		if t.pk == nil {
			return table{}, fmt.Errorf("%s has no db:\",pk\" column", s.Type())
		}
		return t, nil
	})

type User struct {
	ID    int    `db:"id,pk"`
	Email string `db:"email"`
	Notes string
}

func main() {
	users, err := tables.For[User]()
	if err != nil {
		panic(err)
	}
	fmt.Println(users.columns)
	for _, u := range []User{{ID: 10}, {ID: 20}} {
		id, err := users.pk.GetAs[int](u)
		if err != nil {
			panic(err)
		}
		fmt.Println(id)
	}
}
```

Output:

```text
[id email]
10
20
```

`Notes` has no `db` tag, so it is not a column. `Field.Tag` returns the parsed
`name,options` form; `Has("pk")` matches one option exactly. `AccessField` and
`AccessName` return the same cached accessor for a field, which reads `User` or
`*User` and writes through `*User`. Keep the engine and cache in package variables.

## Follow a naming convention

`WithTaggedFields` options adjust the default `db`-style rule. Here untagged
fields keep their Go names and an embedded struct's fields are promoted, as
`encoding/json` does. `Index` is the route through the embedded struct.

```go
package main

import (
	"fmt"

	"github.com/ashtonian/pkg/gowalker"
)

type Base struct {
	ID int `json:"id"`
}

type Event struct {
	Base
	Kind    string `json:"kind"`
	Payload string `json:",omitempty"`
	Secret  string `json:"-"`
}

var jsonNames = gowalker.MustNew(gowalker.WithTaggedFields("json",
	gowalker.UntaggedName(func(goName string) string { return goName }),
	gowalker.PromoteEmbedded(),
	gowalker.DashCommaName(),
), gowalker.ResolveByDepth())

func main() {
	schema, err := jsonNames.Compile[Event]()
	if err != nil {
		panic(err)
	}
	for _, field := range schema.Root().Fields() {
		tag, _ := field.Tag("json")
		fmt.Println(field.Name(), field.Index(), tag.Has("omitempty"))
	}
}
```

Output:

```text
id [0 0] false
kind [1] false
Payload [2] true
```

With these options and the engine option `ResolveByDepth`, the fields are the
members `encoding/json` writes. `ResolveByDepth` settles a name two fields claim
as `encoding/json` does: the shallowest wins, then a field whose tag names it. A
tie that remains is a compilation error, where `encoding/json` would drop both
fields. Without it every collision is an error, which suits configuration keys,
where a silent choice hides a mistake. `DashCommaName` reads `json:"-,"` as the
name `-`. Only the field naming follows the convention; omission, encoding and
custom marshalers stay with the codec.

| Option | Effect |
|---|---|
| none | Exported fields with an explicit tag name, each one whole value |
| `UntaggedName(fn)` | Exported fields without a tag name are named `fn(goName)` |
| `PromoteEmbedded()` | An untagged embedded struct, or pointer to one, contributes its fields |
| `DashCommaName()` | Only a tag of exactly `"-"` excludes; `"-,"` and `"-,omitempty"` name the field `-` |
| `ValidName(fn)` | A tag name `fn` refuses fails compilation, for names another reader parses differently |
| `NestedFields()` | Paths and walks descend into selected structs, maps and lists |

A tag name of `"-"` excludes the field, as only the exact tag `"-"` does
with `DashCommaName`; private fields are always excluded. For aliases, `,squash`-style option-driven inlining or validating
your tags, write a [field resolver](#write-your-own-field-rules).

## Nested sections and paths

With `NestedFields`, sections stay addressable by tag names. `ResolveText`
returns an accessor for a nested path; keep it, since arbitrary paths are not cached.

```go
package main

import (
	"fmt"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
)

type Server struct {
	Port int `koanf:"port"`
}

type Config struct {
	Server  Server
	Workers int
}

var keys = gowalker.MustNew(gowalker.WithTaggedFields("koanf",
	gowalker.UntaggedName(strings.ToLower),
	gowalker.NestedFields(),
))

func main() {
	schema, err := keys.Compile[Config]()
	if err != nil {
		panic(err)
	}
	port, err := schema.ResolveText("server.port")
	if err != nil {
		panic(err)
	}
	var cfg Config
	if err := port.Set(&cfg, 8080); err != nil {
		panic(err)
	}
	value, err := port.GetAs[int](cfg)
	if err != nil {
		panic(err)
	}
	fmt.Println(cfg.Server.Port, value)
}
```

Output:

```text
8080 8080
```

Paths use `.` between fields, `["key"]` for string map keys and `[0]` for
indexes. `Set` follows Go assignment rules and needs a pointer to the root.

## Tags a section passes down

Some annotations describe a whole section: everything under a field tagged
`reload:"hot"` or `sensitive:"true"` inherits it unless a deeper field says
otherwise. `TypeVisit.Inherited` returns the nearest value on the route.

```go
package main

import (
	"fmt"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
)

type TLS struct {
	Cert string
	Key  string `reload:"boot"`
}

type Listener struct {
	Port int
	TLS  TLS
}

type Config struct {
	Listener Listener `reload:"hot"`
	DataDir  string
}

func main() {
	schema, err := gowalker.Compile[Config]()
	if err != nil {
		panic(err)
	}
	err = schema.WalkType(func(v gowalker.TypeVisit) (gowalker.Control, error) {
		if len(v.Shape.Fields()) > 0 {
			return gowalker.Continue, nil // report leaves only
		}
		var names []string
		for _, edge := range v.Route {
			names = append(names, edge.Field.Name())
		}
		class, ok := v.Inherited("reload")
		if !ok {
			class = "boot"
		}
		fmt.Println(strings.Join(names, "."), class)
		return gowalker.Continue, nil
	})
	if err != nil {
		panic(err)
	}
}
```

Output:

```text
Listener.Port hot
Listener.TLS.Cert hot
Listener.TLS.Key boot
DataDir boot
```

The route also includes pointer and collection steps, whose `Field` is nil; this
example has none. `Inherited` also reads tags on embedded structs a field was
promoted through.

## Types that decode themselves

A struct that implements `encoding.TextUnmarshaler`, like `time.Time`, is one
value to its users even though it has fields. `WithAtomicImplementers` makes
every such type a leaf, whether the method is on the value or the pointer.

```go
package main

import (
	"encoding"
	"fmt"
	"reflect"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
)

type Version struct{ Major, Minor int }

func (v *Version) UnmarshalText(text []byte) error {
	_, err := fmt.Sscanf(string(text), "%d.%d", &v.Major, &v.Minor)
	return err
}

type Config struct {
	Name string
	Min  Version
}

func leaves(engine *gowalker.Engine) []string {
	schema, err := engine.Compile[Config]()
	if err != nil {
		panic(err)
	}
	var out []string
	err = schema.WalkType(func(v gowalker.TypeVisit) (gowalker.Control, error) {
		if v.Depth > 0 && (v.Shape.Atomic() || len(v.Shape.Fields()) == 0) {
			var names []string
			for _, edge := range v.Route {
				names = append(names, edge.Field.Name())
			}
			out = append(out, strings.Join(names, "."))
		}
		return gowalker.Continue, nil
	})
	if err != nil {
		panic(err)
	}
	return out
}

func main() {
	fmt.Println(leaves(gowalker.Default()))
	fmt.Println(leaves(gowalker.MustNew(
		gowalker.WithAtomicImplementers(reflect.TypeFor[encoding.TextUnmarshaler]()),
	)))
}
```

Output:

```text
[Name Min.Major Min.Minor]
[Name Min]
```

Leaf rules accumulate: combine `WithAtomicImplementers` with `WithAtomicType`
for types matched another way. Embedding promotes methods, so a struct that
embeds `time.Time` implements `TextUnmarshaler` itself and becomes a leaf;
`encoding/json` has the same behavior.

## Write your own field rules

When a preset is not enough, `WithFieldResolver` receives each
`reflect.StructField` and returns a `FieldRule`: a name, aliases, `Skip`,
`Inline` to expose an embedded struct's fields, or `Atomic` to keep this one
field whole. Set `Explicit` when the name is the field's own, such as a tag
name, so `ResolveByDepth` prefers it over a derived name at equal depth.
Return an error to reject a tag; it appears in the compile error.
The [config-style example](../example_test.go) handles `koanf` names, `,squash`,
`alias` tags and self-decoding types this way.

## Zero, nil and missing

`Get` reports whether it reached the location independently of the Go value.
`Count` exists and holds zero, `Child` exists and is nil, and the other two
locations cannot be reached. All four reads succeed.

```go
package main

import (
	"fmt"

	"github.com/ashtonian/pkg/gowalker"
)

type Row struct {
	Count  int
	Child  *Row
	Labels map[string]string
}

func main() {
	row := Row{}
	for _, location := range []string{"Count", "Child", "Child.Count", `Labels["absent"]`} {
		v, err := gowalker.Get(&row, location)
		if err != nil {
			panic(err)
		}
		fmt.Println(location, v.State)
	}
}
```

Output:

```text
Count Present
Child Nil
Child.Count Missing
Labels["absent"] Missing
```

`GetAs[int]` on `Child.Count` returns an error matching `gowalker.ErrMissing`
instead of a zero. An unknown field returns `ErrPath`; an index past the end
returns `ErrIndex`. Match errors with `errors.Is`.

## The value inside an interface

A location declared `any` keeps that type: `GetAs[int]` on it returns `ErrType`
even when it holds an int. `path.Deref()` selects what the interface, or a
pointer, holds.

```go
package main

import (
	"errors"
	"fmt"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/gowalker/path"
)

type Row struct{ Value any }

func main() {
	row := Row{Value: 42}
	schema, err := gowalker.Compile[Row]()
	if err != nil {
		panic(err)
	}
	declared, err := schema.ResolveText("Value")
	if err != nil {
		panic(err)
	}
	_, err = declared.GetAs[int](row)
	fmt.Println(errors.Is(err, gowalker.ErrType))

	concrete, err := schema.Resolve(path.Of(path.Field("Value"), path.Deref()))
	if err != nil {
		panic(err)
	}
	value, err := concrete.GetAs[int](row)
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

Pointers and interfaces in the middle of a path are followed automatically:
`Payload.Name` reaches `Name` inside an interface holding a struct. `Deref`
matters only when the pointer or interface is the location you selected.

## A failed write changes nothing

`Set` follows Go assignment rules. Creating missing pointers and lossless
numeric conversion are opt-in. Here 256 does not fit in a `uint8`, so the write
fails with `ErrConversion`, and the pointer it would have allocated stays nil.

```go
package main

import (
	"errors"
	"fmt"

	"github.com/ashtonian/pkg/gowalker"
)

type Limits struct{ Count uint8 }

type Config struct{ Limits *Limits }

func main() {
	var cfg Config
	err := gowalker.Set(&cfg, "Limits.Count", 256,
		gowalker.WithPointerAllocation(), gowalker.WithCheckedNumbers(),
	)
	fmt.Println(errors.Is(err, gowalker.ErrConversion), cfg.Limits == nil)
}
```

Output:

```text
true true
```

A successful write returns nil. Writing below an existing pointer, map or slice
changes the shared storage; assigning a pointer, map or slice as the value
replaces it. Map-held structs are copied, changed and stored back. See the
[write contract](api.md#writes).

## Schema errors name the fields

Compilation errors describe the type and fields at fault, because they come
from your users' declarations. Errors about values never include map keys or
values, which can be private data; the location is in `*gowalker.Error` instead.

```go
package main

import (
	"errors"
	"fmt"

	"github.com/ashtonian/pkg/gowalker"
)

type Row struct {
	ID    int `db:"id"`
	Owner int `db:"id"`
}

func main() {
	_, err := gowalker.MustNew(gowalker.WithTaggedFields("db")).Compile[Row]()
	fmt.Println(errors.Is(err, gowalker.ErrSchema))
	fmt.Println(err)
}
```

Output:

```text
true
gowalker: compile main.Row: invalid schema: fields ID and Owner of main.Row both use the name "id"
```

Use `errors.As` with `*gowalker.NameConflict` to build your own message from
the two field handles, and with `*gowalker.Error` for the operation, path and type.

## Walk a whole value

`schema.WalkType` visits types; `schema.WalkValue` visits the actual values,
with a path for each that can be resolved again later. Visitors return
`Continue`, `SkipChildren` or `Stop`. Collect paths during a walk and write
afterwards, so the walk never sees the value change under it. The
[walk-and-update example](../example_test.go) shows the pattern, and the
[walking contract](api.md#type-and-value-walking) covers ordering and limits.

[API reference](api.md) · [How it works](architecture.md) ·
[Scope](scope.md) · [Performance](performance.md)
