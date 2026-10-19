# API contracts

This reference explains accepted inputs, returned values, errors and ownership.
Start with the [quick start](../README.md) or [usage examples](usage.md).
The generated [Go API](https://pkg.go.dev/github.com/ashtonian/pkg/gowalker)
lists every exported declaration. Go 1.27+ is required.

## Choose an entry point

| Need | Use | Returns |
|---|---|---|
| Inspect a type using Go field names | `gowalker.Compile[T]()` / `CompileType(t)` | The `Default` engine's cached `(*Schema, error)` |
| Your own naming convention | `MustNew(WithTaggedFields(key, options...))` in a package variable | An `*Engine` whose schemas use tag names |
| Fully custom naming, aliases or validation | `MustNew(WithFieldResolver(callback))` | An `*Engine`; the callback returns `(FieldRule, error)` |
| Read a field's tags | `field.Tag(key)` / `field.Tags()` | Parsed `(Tag, bool)` / raw `reflect.StructTag` |
| Compute your metadata once per type | `NewTypeCache(engine, build)` → `cache.For[T]()` | Your `(M, error)`, built once and shared |
| Read or write one named root field repeatedly | `schema.AccessName(name)` / `AccessField(field)` | A cached `(*Accessor, error)`; the name is literal |
| Repeated nested access | `schema.ResolveText(text)` / `Resolve(path)` | An uncached `(*Accessor, error)` to retain |
| Annotations a section passes down | `schema.WalkType(visitor)` with `TypeVisit.Inherited(key)` | The nearest tag value on the route |
| Read once using Go field names | `gowalker.Get(root, text)` / `GetAs[T](root, text)` | `(Value, error)` with presence, or `(T, error)` |
| Write once using Go field names | `gowalker.Set(&root, text, input)` | `error`; nil means success |
| Work with reflection directly | `accessor.GetValue`, `accessor.SetValue`, `schema.WalkValueReflect` | The same operations on an exact `reflect.Value` root |

The package-level functions — `Compile`, `CompileType`, `Get`, `GetAs`, `Set`,
`WalkType` and `WalkValue` — share one process-wide engine, returned by
`Default()`: exported Go field names, exact matching, `DefaultLimits` and a
256-schema cache. Its rules cannot be changed, because every library in the
process shares it. A library with its own rules keeps its own engine:

    var columns = gowalker.MustNew(gowalker.WithTaggedFields("db"))

`New` returns `(*Engine, error)` for options built at run time; `MustNew` panics
on an invalid option, like `regexp.MustCompile`. The one-call helpers reuse the
default engine's schemas but resolve their text path on each call; retain an
accessor from `ResolveText` for a loop.

`AccessName` follows root pointers and looks up one name or alias. It returns
`ErrType` for a non-struct root and `ErrPath` for an unknown name, an atomic root
or a recursive pointer root. Punctuation is literal: `"a.b"` is one field name.
Use ResolveText when the dot should select a nested field. AccessName and
AccessField share the same bounded cache of visible root-field accessors.

`AccessField` is useful when a library already has a field from `Fields()` or
`Lookup()`. It accepts visible fields of this schema's root struct; for pointer
roots, follow `Elem()` to find that struct. It rejects fields from another schema,
nested owners, or inline declarations that are expanded rather than directly
visible. Repeated calls for one field return the same accessor.

Use the schema, accessor, shape and field handles returned by the API; their
nil/zero values are not usable. Engines, schemas and type caches must not be
copied. Rule and build callbacks must be deterministic and safe for concurrent
calls, including duplicate calls during concurrent cold compilation. Caches
retain metadata, never the instances passed to reads or writes.

## Generics and the Go baseline

Go 1.27 is required. Its [generic methods](https://go.dev/doc/go1.27#language)
allow `engine.Compile[Config]()`, `cache.For[Config]()` and
`accessor.GetAs[int](cfg)` without wrapper types or package-level helpers for
each operation. `CompileType` and `TypeCache.Get` handle runtime types through
the same code, using `reflect.TypeFor[T]` for typed calls.

The return type is checked for assignability at runtime because paths, field rules
and interface contents can be dynamic. Generics do not validate a string path at
compile time or eliminate the need for reflection. Named types and typed nils
retain the same contracts as ordinary reads; generic reads never decode or coerce.

## Tags and naming conventions

`field.Tag(key)` and `LookupTag(tag, key)` parse one tag in the
`name,option,option` form and return `(Tag, bool)`; the bool is false when the
key is absent, so `db:""` and a missing tag stay distinct. `Tag.Name` is the
text before the first comma and `Tag.Options` the unparsed rest; `Has(option)`
matches one comma-separated option exactly, without trimming spaces.
`ParseTag(value)` splits a value you already have. `field.Tags()` returns the
raw `reflect.StructTag` for tags with other syntax, such as `validate:"min=1"`.
Gowalker never decodes tag values.

`WithTaggedFields(key, options...)` names fields by that tag. Without options:

| Declaration | Result |
|---|---|
| `ID int` tagged `db:"id"` | Visible field named `id` |
| `ID int` tagged `db:"id,option"` | Visible field named `id`; options ignored |
| Missing tag, `db:""`, `db:",option"` | Excluded |
| `db:"-"`, `db:"-,option"` | Excluded |
| Private field, with or without a tag | Excluded |
| `db:" id "` or `db:"id.part"` | Exact name preserved, including spaces or dots |
| Tagged struct, map, list or pointer | One leaf field, read or replaced whole |
| Embedded field without an explicit name | Excluded; not promoted |
| Two selected fields with the same name | Compilation error matching ErrSchema |

Options relax those defaults:

| Option | Changes |
|---|---|
| `UntaggedName(fn)` | An exported field without a tag name, including `db:",option"`, is named `fn(goName)`; an empty result excludes it. `nil` is invalid. |
| `PromoteEmbedded()` | An embedded struct or pointer to one, without a tag name, contributes its fields as `encoding/json` does. An embedded leaf type stays one field. Promoted collisions are errors, not silent choices. |
| `NestedFields()` | Selected fields are not leaves: paths such as `server.port` and walks descend into them. |

An empty tag key, or one containing spaces, control characters, a colon or a
quote, returns ErrSchema from New, as does a nil option. Tag keys are case
sensitive; `WithCaseFoldNames` affects selected field names only. The last
`WithTaggedFields` or `WithFieldResolver` option supplies the naming rule.

The presets name fields. They do not implement omission, decoding, custom
marshalers or a codec's tie-breaking rules. Compile still retains raw payload
types and counts them against its limits, even when fields are leaves.

## Per-type metadata

`NewTypeCache(engine, build)` returns a `*TypeCache[M]` whose `For[T]()` and
`Get(t)` return `build`'s result for that exact type, computed once from the
engine's schema. Typical `M` values are a column list with accessors, parsed
tag options, or a help-text catalog. Results and errors are both retained, for
up to the engine's cache capacity of types; beyond it, or with capacity zero,
each call compiles and builds again.

Concurrent first requests for one type can each run `build`; one result is kept,
so `build` must be deterministic and free of side effects that matter. `build`
may request other types from the same cache. A recursive type must not request
itself, or it recurses forever: track the types in progress inside `build`.
A compilation error is returned unchanged; a `build` error is returned as built.
`NewTypeCache` panics on a nil engine or build function.

## Field views

The default view uses exported Go names, exact matching and declared nesting.
Embedding does not imply promotion. A resolver returns a `FieldRule` with a name,
aliases, skip, inline or leaf behavior. An empty name falls back to the Go name.
Private fields remain excluded; an explicitly inlined anonymous struct can expose
its exported descendants when safe reflection permits access. Inline pointers
are followed for reads. Writes can allocate exported inline pointers when
requested; safe reflection cannot allocate an unexported embedded pointer.

Canonical names and aliases share one collision domain. `WithCaseFoldNames`
uses Unicode simple folding, including equivalences such as S and long s.
Two different fields claiming equivalent names fail compilation; equivalent
aliases of the same field are harmless. Inline cycles also fail compilation.
Ordinary recursive children refer to existing shapes and remain valid.

Compilation validates the entire reachable named view, even if you later read
only one field. Use `FieldRule.Atomic: true` when a field's contents are outside
that view: for example, each db-tagged column in a direct-column reader. This
marks the field's shape as a leaf without changing other uses of its Go type.
“Atomic” is a traversal rule, not a synchronization guarantee.
The whole field can still be read or replaced, while paths cannot descend into
it and walks visit it as a terminal. `Atomic` and `Inline` together are invalid;
`Skip` takes precedence.

`WithAtomicType(classify)` makes every use of a type a leaf; the callback
receives exact declared types, including pointers. `WithAtomicImplementers(ifaces...)`
makes a leaf of every type implementing one of the interfaces through its value
or pointer, such as `encoding.TextUnmarshaler` or `sql.Scanner`; a non-interface
argument returns ErrSchema. Leaf rules accumulate: a type is a leaf when any rule
matches. A struct embedding a type with methods gains those methods, so
embedding `time.Time` makes the outer struct a `TextUnmarshaler` and a leaf.
Leaves retain raw type structure and its compilation-budget cost.

`Shape.Fields()` returns visible handles in declaration/inline order. Each field
provides its declared `Type`, raw `Tags`, Go `StructField`, declared `Shape`, copied physical `Index`,
original `DeclaringType`, and copied `InlineParents` for inherited consumer metadata.
`Shape.DeclaredView()` returns selected declarations before inline expansion;
`Field.Inline()` identifies inline owners, including empty ones whose tags a
consumer still needs to validate. Skipped fields are absent from both views.
Use `StructTag.Lookup` for absent versus present-empty tags; tag option syntax belongs to the resolver.

Schema collisions wrap `*NameConflict`, available with `errors.As`. Its owner,
conflicting name and two field handles let adapters build their own diagnostics.
Default error text omits those names.

`Shape.DeclaredFields()` preserves raw fields, including private fields. Pointer,
collection and key relationships remain inspectable through `Elem()` and `Key()`.
Named views follow visible value relationships. Types reached only as map keys,
through skipped fields or below leaves retain raw structure; those relationships
do not cause their named views to be expanded. Atomic types stop named traversal; they can still be
selected as terminals.

The [examples](../example_test.go) demonstrate independent db-tag and config-style
field rules. These are naming examples, not complete SQL or configuration adapters.

## Root binding

| Schema | `accessor.Get` accepts | Empty path returns |
|---|---|---|
| T, a non-pointer concrete type | T or non-nil *T | T |
| *T | *T or non-nil **T | *T, including a nil pointer |
| An interface I | Values assignable to I, nil, or non-nil *I | A value declared as I |

When the schema is `*Row`, an incoming `*Row` is the root itself; an incoming
non-nil `**Row` supplies the root stored behind it. For an interface schema `I`,
a non-nil `*I` similarly supplies the stored interface. Other types must be
assignable to `I`. An incompatible root returns `ErrType`.

`GetValue` accepts only the exact schema type, with no implicit root
unwrapping: a schema of `Row` needs a `reflect.Value` whose type is `Row`.

For ordinary entity access, prefer `Compile[Row]`: both reads and writes then
accept `*Row`. Compile `*Row` when the root pointer's own value matters; writes
then need `**Row` so they can replace or allocate that pointer.

Package-level `Get` infers the root's runtime type, so an empty path on a pointer
returns that pointer. A plain nil root has no runtime type and returns `ErrType`;
compile an interface schema when a nil interface is a valid root.

## Paths

Text paths use Go-style identifier field names, quoted string keys and nonnegative
indexes: `Servers["primary.eu"].Ports[0]`, `["root key"].Name`, or `[0].Name`.
The empty path selects the root. Whitespace outside quoted keys is invalid.

Typed constructors retain actual key types and allow field names beyond the text
grammar. For example, `path.Of(path.Key(MyKey("primary")), path.Field("a.b"))`
is distinct from a text path using a plain string key or two nested fields.
`Path.MarshalText` fails if it cannot retain the meaning and key type. There are
no wildcard steps, implicit key conversion, or lossy stringification.

Intermediate pointers/interfaces are followed automatically. Terminals retain
their declared types; append `path.Deref()` to select a pointer's pointee or the
concrete value inside an interface. Dynamic tails are resolved against the actual
interface contents and can fail if their shape changes.

Non-comparable keys and non-reflexive keys such as NaN return `ErrKey`. Nil is a
valid key for an interface-keyed map. Indexes must exist; an index into a nil slice
is out of bounds. An absent map entry produces `Missing`.

## Results and errors

| State | Meaning |
|---|---|
| `Present` | A reached non-nil value, including false, zero, and empty strings |
| `Nil` | A reached nil pointer, map, slice, interface, function or channel |
| `Missing` | An absent map entry or a nil ancestor prevents reaching the terminal |

A non-nil interface containing a typed nil pointer is `Present` at the interface
location. Its `DynamicType` reports the pointer type. Explicitly dereferencing the
interface reaches a `Nil` pointer terminal.

Likewise, `GetAs[int]` on a field declared `any` returns `ErrType` even if it
currently contains an int. Resolve `path.Of(path.Field("Value"), path.Deref())`
to read its concrete int instead. The executable examples show both results.

`Value.Type` is the declared terminal type when known; a missing dynamic tail may
leave it nil. `DynamicType` describes a reached interface's concrete contents,
or equals `Type` for other reached values. `Reached` contains the consumed path
prefix; for missing locations it stops before the failed step. Errors report
the failed step separately; reaching a nil terminal succeeds with State `Nil`.

`Get` returns `(Value, error)`; both `Nil` and `Missing` normally have a nil
error. `GetAs[T]` returns `(T, error)`, using `ErrMissing` for an unreachable
location and `ErrType` for a terminal that cannot be assigned to T.

`Interface()` returns the borrowed value; check `State` to distinguish a nil
interface from a missing result. `Reflect()` exposes normal reflect capabilities,
including any mutability the underlying value already has. Neither is a snapshot.

Errors support `errors.Is` with `ErrSchema`, `ErrPath`, `ErrType`, `ErrMissing`,
`ErrNil`, `ErrAccess`, `ErrIndex`, `ErrKey`, `ErrConversion`, `ErrCallback`, and
`ErrLimit`. Use `errors.As` to obtain a
`*gowalker.Error` with operation, typed path, failing step, type and wrapped cause.

| Error | Typical cause |
|---|---|
| `ErrSchema` | Conflicting names, invalid inline rules or engine settings |
| `ErrPath` | Unknown field, malformed text path or descent into a leaf |
| `ErrType` | Wrong root type, incompatible assignment or requested result type |
| `ErrMissing` | A typed read needs an absent value; a write needs a missing map intermediate |
| `ErrNil` | A write encounters a nil pointer/map/interface it cannot allocate |
| `ErrIndex` / `ErrKey` | Out-of-bounds index or incompatible/unsupported map key |
| `ErrAccess` | A value cannot be safely read or written through reflection |
| `ErrConversion` | Requested numeric conversion would lose information |
| `ErrCallback` | A visitor returned an error; the original error remains wrapped |
| `ErrLimit` | A configured resource budget was exceeded |

Type-walk failures also carry `TypeRoute`, a detached schema route.

Error text names the operation, the Go type and the cause, such as
`gowalker: compile app.Row: invalid schema: fields ID and Owner of app.Row both
use the name "id"`. Schema errors come from declarations, so they name fields,
tags and resolver messages. Text never includes path steps, map keys or values,
which can be caller data; read `Error.Path` and `Error.Step` to show a location
under your own policy. A `*NameConflict` also exposes `Owner`, `Name`, `First`
and `Second` for a custom message. `GetAs` returns `ErrMissing` rather than
replacing missing with zero. `State` values print as `Present`, `Nil` and `Missing`.

## Writes

`Set(&root, text, input, options...)` infers T from its *T destination.
`accessor.Set(&root, input, options...)` always requires a non-nil pointer to
the schema's exact root type T. A schema of *Row therefore requires **Row, even when only a
field on Row is being changed. `SetValue(rootValue, inputValue, options...)` takes
the exact declared, settable root value directly.

Terminals keep their declared types. Setting a pointer field replaces the pointer;
append `path.Deref()` to assign its pointee. A typed nil clears a compatible
nullable terminal. An untyped nil has no declared type and returns `ErrType`.
For a nil interface itself, pass `reflect.Zero(interfaceType)` through `SetValue`.
A valid nil map entry is stored, not deleted.

Defaults permit assignable inputs and replacement/insertion of terminal map
entries in existing maps. Additional behavior is explicit:

| Option | Permits | Boundary |
|---|---|---|
| `WithPointerAllocation()` | Creating nil pointer intermediates | Cannot allocate unexported storage; cannot infer an interface's dynamic type |
| `WithMapAllocation()` | Allocating nil maps and creating missing map intermediates | Pointer elements also need pointer allocation; indexes do not grow |
| `WithCheckedNumbers()` | Lossless Go numeric conversions, including named numeric types | Finite values and signed zeros must be preserved; no decoding or coercion |

Checked conversion compares exact rational values before accepting the prepared
result. It rejects range/sign changes, fractional truncation, precision loss,
overflow, underflow and non-finite conversions. Complex-to-complex conversions
check both components. Exact assignment of NaN/Inf is still valid. No conversion
can restore precision an upstream decoder has already lost.

Preparation follows the complete route, checks access/bounds/assignment, and
constructs any required detached allocations or shallow struct/array owners.
Only then does the terminal write commit and attach owners. A core-generated
error leaves values, reference identities and optional allocations unchanged.
Panics or side effects in consumer rule callbacks are caller behavior.

Map-held structs and boxed interface values receive writeback. Writing below an
existing pointer, map or slice updates its shared storage in place, so aliases
observe that change. Assigning a pointer, map or slice as the terminal replaces
that location’s reference according to ordinary Go assignment. Following an existing reference does not rewrite copied ancestors, which
prevents stale owner copies from overwriting cyclic graphs. These are single
location writes, with no collection deletion, slice growth or batch transaction.

## Type and value walking

Use `schema.WalkType(visitor, options...)` to inspect the named type graph, or
`WalkType[T]` with the default engine. Each `TypeVisit` includes `Shape`,
`Kind`, `Depth` and a copied `Route`. A `TypeEdge` identifies a field, pointer
dereference, or possible collection element. An element route is not an accessor
path and contains no fake map key/index. Map keys are metadata and are not walked.
Interfaces and atomic types are leaves. A type already active on the ancestor
route emits `Reference`; repeated types on separate branches remain visitable.

`TypeVisit.Inherited(key)` returns the value of tag `key` on the nearest field
of the route that declares it: the visited field, then each enclosing field,
including the embedded owners an inlined field was promoted through. It returns
`("", false)` when no field on the route has the tag. Use it for annotations a
section passes down, such as `sensitive:"true"` or `reload:"hot"`, where a deeper
tag overrides an outer one.

`schema.WalkValue(root, visitor, options...)` shares Get's root binding.
`WalkValue(root, visitor, options...)` supplies the default view; the reflect entry
point is `schema.WalkValueReflect(value, visitor, options...)`. A `ValueVisit`
includes a borrowed `Value`, its `Shape`, optional field handle, and a concrete
`Path`. Pointer/interface children add an explicit `Deref` step; field names are
canonical. These paths can be resolved again after walking.

Value walks visit nil terminals without fabricating descendants. Visible fields
behind a nil inline owner are omitted. Active pointer/map identities and slice
headers detect cycles and emit `Cycle`; shared references outside the current
ancestor path are visited independently. Slice identity includes its declared
type, data pointer, length and capacity. Functions/channels/unsafe pointers are
leaves. Callbacks must not structurally mutate the graph. The
[walk example](../example_test.go) collects concrete paths and updates afterward.

Both visitors return `Continue`, `SkipChildren`, or `Stop`, plus an error.
`WithLeaveEvents()` adds paired `Leave` callbacks after ordinary entered nodes,
including pruned nodes. References and cycles have no leave event. Stop succeeds
immediately; callback errors wrap with `Unwrap`, and stopped/failed ancestor
entries can remain unpaired. Invalid controls return `ErrType`.

Struct fields follow declaration/inline order, lists follow indexes, and maps
with string-kind keys use lexical order while retaining the actual key type.
Nonempty maps with other key types require `WithMapKeyOrder(less)` or
`WithUnorderedMaps()`. Those options are mutually exclusive. Comparators must
supply a strict weak ordering and obey the callback mutation restriction.
Non-reflexive keys such as NaN remain unsupported. Before sorted traversal,
a map larger than the remaining node budget fails without materializing its
keys; prune the map node to inspect only the container.

## Resource budgets

| Setting | Default | Counting rule |
|---|---:|---|
| Cache capacity | 256 | Root schemas, including their complete raw graphs; also each `TypeCache`'s retained types |
| `MaxTypes` | 4096 | Unique declared types in one compilation |
| `MaxFields` | 65536 | Raw declared fields plus visits while expanding named views |
| `MaxDepth` | 128 | First-visit type edges, inline expansion depth, or consecutive runtime pointer/interface hops |
| `MaxPathSteps` | 256 | Explicit steps in a resolved path |
| Walk `MaxDepth` | 128 | Type edges or concrete value-path steps from root depth zero |
| Walk `MaxNodes` | 100000 | Entries, references and cycles; excludes Leave events |
| Text path size | 65536 bytes | Input to `path.Parse` or output from `MarshalText` |

Type and inline roots have depth zero. Repeated type references do not expand
again. Runtime indirection counts each pointer/interface hop. A direct two-field
struct requires four field visits: two raw fields and two named-view fields.

Use `WithCacheCapacity(0)` to disable retention; at capacity, existing entries
remain and new types compile uncached. `WithLimits` accepts a complete positive
limits value (start from `DefaultLimits()`). There is no unlimited mode. These
budgets bound traversal and metadata construction, rather than execution time
or heap bytes. `WithWalkLimits`
sets a separate complete walk budget from `DefaultWalkLimits()`. Walk depth can
be zero (root only) and node count must be positive. Limits fail before the
over-budget callback. Value paths also obey the engine's `MaxPathSteps`, so all
emitted paths remain resolvable under that engine. Cache capacity bounds retained
root schemas, not total heap bytes; each root retains its full graph. See the
[performance guide](performance.md#cost-model) for the retention tradeoff.

## Further examples

The [executable examples](../example_test.go) show tagged columns, aliases and
inlining, pointer/map allocation, interface writeback, read states and walking
before mutation. Each has expected output checked by `go test`. See
[performance](performance.md) for measured costs and reuse guidance.
