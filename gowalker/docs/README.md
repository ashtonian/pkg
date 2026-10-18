# Gowalker documentation

Gowalker compiles a Go type and field rules into reusable metadata and
accessors. Those accessors read or update actual values; walkers inspect either
the type graph or a live object graph.

| Guide | Covers |
|---|---|
| [Quick start](../README.md) | Basic read/write, reuse, and the main guarantees |
| [Usage examples](usage.md) | Tagged columns, presence states, interface contents and failed writes |
| [API contracts](api.md) | Field rules, root binding, paths, nil/missing states, errors and budgets |
| [Architecture](architecture.md) | Type graph, immutable views, accessors, prepared writes and walkers |
| [Scope](scope.md) | Included behavior, consumer responsibilities and unsupported operations |
| [Performance](performance.md) | Reuse, allocation costs and reproducible measurements |
| [Executable examples](../example_test.go) | Complete examples tested with the implementation |

## How an operation works

1. Inspect a declared type into a finite graph. Recursive types refer to existing
   nodes instead of expanding forever.
2. Apply the engine's field names and leaf rules. Lookup, walking and writes
   then agree on the same field identity.
3. Resolve a concrete path into an accessor. Known field and key checks happen
   once; interface contents are checked at execution time.
4. Bind an instance and read, write or walk it. Reads check nils and bounds;
   writes prepare all fallible work before changing storage.

Metadata is immutable and reusable. Returned values are borrowed, and mutability
is checked against the actual instance. Start with the quick start; use the API
guide when defining a library’s field rules or handling dynamic values.
