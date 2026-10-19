# Performance and allocation costs

Reuse an engine per set of field rules, a `TypeCache` for your own per-type
metadata, and an accessor per frequently used path. The one-call helpers (`Get`,
`GetAs`, `Set`, `WalkValue`) reuse the default engine's cached schemas but parse
and resolve their text path on every call; keep an accessor for a loop.

## Cost model

- Compilation builds the raw graph and named view. Raw inspection includes
  skipped and atomic fields so their declared structure remains available.
- The engine caches at most 256 root schemas by default, including dynamic types
  encountered through interfaces. `WithCacheCapacity` changes that count; zero
  disables retention. Existing entries stay cached when capacity is reached;
  new types compile uncached. Capacity bounds roots, not total bytes or Go's own
  runtime type metadata.
- `Schema.AccessName` and `Schema.AccessField` share cached accessors for visible root fields. General path
  resolution is uncached; retain its result.
- Resolved reads avoid repeated field discovery. Reading a struct from a map can
  allocate because reflection materializes the map entry. Boxing a scalar through
  `Interface()` or binding a declared interface root can also allocate.
- Writes through existing pointers, maps and slices update shared storage.
  Assigning a terminal reference can replace it. Map-held structs, arrays and
  boxed interface values can require detached owners and writeback. Creating
  optional pointers/maps and checked numeric conversion have their own allocation costs.
- Walkers return retainable paths/routes, track active ancestors and order map
  keys. These operations allocate. Prune a container when its contents are not
  needed; sorted maps larger than the remaining node budget fail before key
  materialization.
- `Field.Tag(key)` and `Field.Tags()` read the original tag string without
  copying an index. Use them when you only need annotations; `GoField()` copies
  its index slice.
- A `TypeCache` hit is a read-locked map lookup. Its first request per type
  compiles (or reuses) the schema and runs your build function.
- Metadata slice getters return defensive copies. Keep the returned catalog for
  your operation instead of calling the same getter in an inner loop.

Returned maps, slices and pointers borrow caller storage. Avoiding a deep copy
does not provide a snapshot or synchronization.

## Current measurement baseline

Measurement environment: Go 1.27.2, darwin/arm64, Apple M2 Max. These are
allocation snapshots of [core](../benchmark_test.go) and
[extended fixtures](../extended_benchmark_test.go). They describe these
fixtures, not every root shape.

| Reused operation | B/op | allocs/op |
|---|---:|---:|
| Cached compilation | 0 | 0 |
| Cached direct-field accessor, `AccessField` | 0 | 0 |
| Cached literal-name accessor, `AccessName` | 0 | 0 |
| Read a tag, `Tags().Lookup("db")` | 0 | 0 |
| Parse a tag and test an option, `Tag("db")` then `Has("pk")` | 0 | 0 |
| Cached per-type metadata, `TypeCache.For[T]` | 0 | 0 |
| One-call read with the default engine, `gowalker.Get(&row, "Child.N")` | 480 | 5 |
| Read through a pointer, `Child.N` | 0 | 0 |
| Read from a map of structs, `Rows["one"].N` | 8 | 1 |
| Read through an interface holding a pointer, `Dynamic.N` | 0 | 0 |
| Typed pointer read, `GetAs[*child]` | 0 | 0 |
| Typed scalar read, `GetAs[int]` | 8 | 1 |
| Missing read with a one-step reached prefix | 48 | 1 |
| Exact write through a pointer, `Child.N` | 0 | 0 |
| Exact write to a map-held struct, `Rows["one"].N` | 128 | 3 |
| Exact write to an interface-boxed struct, `Dynamic.N` | 128 | 3 |
| Type walk | 672 | 22 |
| Value walk | 1088 | 17 |

Reads use a shared accessor over a root passed by pointer. Write and walk
fixtures include a pointer field, a one-entry map of structs and a boxed struct.
Those results do not describe every root shape or prove a throughput advantage.
Current timing samples vary with other work on the machine; no general latency
or speedup claim follows from this table.

Resolution also allocates and belongs outside a hot loop: the three-step typed
fixture measured 4 allocations and the text equivalent 8. Pointer allocation
measured 128 B/3 allocations; checked int64-to-int assignment measured 400 B/12.
Checked conversion uses exact rationals to preserve numeric correctness.

A sorted walk over a 1024-entry string/int map measured 109280 B/4100 allocations.
Each callback receives a retainable path; ordering and reflection materialize
keys/values. A type/value walk is not a zero-allocation selection mechanism.

The cache limit counts root schemas rather than bytes. Each retained schema
includes its raw graph and any direct-field accessors requested from it. Choose
a smaller capacity for large or dynamically constructed types.

## Reproduce and profile

```sh
go test . -run '^$' -bench . -benchmem -benchtime=300ms -count=6 -cpu=1
```

Compare the same fixture, Go version, CPU setting and build flags. Interleave
before/after runs; keep profiling separate from race tests and benchmark timing.
Use a full sample set and statistical comparison before drawing timing conclusions.

For an allocation profile, keep the output binary outside the checkout:

```sh
go test . -run '^$' -bench '^BenchmarkSet/Child.N$' -benchtime=2s -cpu=1   -memprofile=/tmp/gowalker-write.pprof -o /tmp/gowalker-profile.test
go tool pprof -top -alloc_objects /tmp/gowalker-write.pprof
```

Correctness remains the constraint on optimization: failed writes must preserve
values and optional allocations, aliases must observe successful reference writes,
and published metadata must remain immutable. Measure consumer operations too;
a zero-allocation accessor does not imply a zero-allocation decoder or application.
