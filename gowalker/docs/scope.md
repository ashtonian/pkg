# Scope and limits

Gowalker supplies reusable type metadata and access to Go values. Use it when
several operations need to agree on field names, nested locations and traversal.
A consumer defines what those fields mean in its own domain.

## Supported operations

| Operation | What it provides |
|---|---|
| Inspect types | Exact declared types, raw struct fields, pointer/element/key relationships, and recursive type graphs |
| Define visible fields | Names, aliases, skip/inline rules, field-specific or type-wide leaves, exact/case-fold matching, and collision errors |
| Resolve locations | Concrete field, map-key, index and dereference steps; a text subset; an empty path for the root |
| Read | Borrowed values with distinct present, nil and missing states; optional typed results |
| Write | Go-assignable values, optional lossless numeric conversion, explicit pointer/map allocation, map/interface writeback, and existing index replacement |
| Walk | Type or value traversal with pruning, stop, errors, recursion/cycle events, ordering and finite limits |
| Reuse metadata | Concurrent schema/accessor reuse, bounded engine caches and cached accessors for visible root fields |

All Go kinds can be described. Functions, channels and unsafe pointers are
leaves: gowalker does not call functions, operate channels or follow unsafe
pointers. Private fields remain unavailable through normal accessors.

## What consumers implement

Gowalker does not decode values or interpret domain tags. A resolver that reads
`db` or `json` tags supplies field-selection rules; choosing a tag name alone
does not implement an ORM or the complete field-selection rules of a serializer.

| Consumer | Keeps responsibility for |
|---|---|
| Configuration library | Sources and precedence, defaults, decoding, validation, redaction, secrets and reload behavior |
| Database library | Columns and keys, SQL NULL, scanning, driver values and transactions |
| Serializer | Encoding, omission, custom marshalers and codec-specific embedding rules |
| Comparison or patch library | Equality, operators, matching and multi-value failure behavior |

Consumers can associate their own metadata with field handles. Gowalker does
not need to know whether a field is a secret, a primary key or a required option.
Small isolated uses of reflection, such as checking one type or allocating a
value, do not need to route through gowalker.

## Operations not provided

- Cloning, type-to-type mapping, projection or structural diff.
- Slice growth or append, map deletion, wildcard selection or batch transactions.
- Address-to-path lookup, source AST inspection or code generation.
- Implicit pointer/map allocation, string decoding or lossy numeric coercion.
- Private-field access, automatic object locking or snapshots of mutable data.

These limits keep shared structural behavior independent of consumer semantics.
The module does not promise compatibility with a codec merely because its tags
can be read.

## Failure and resource limits

Unknown fields, incompatible roots/keys, inaccessible values, invalid indexes,
ambiguous names and exceeded budgets return errors. A missing map entry or nil
ancestor is a successful `Get` with state `Missing`; `GetAs[T]` reports
`ErrMissing` for that same location. A reached nil value is distinct from both.

A failed core write leaves storage and optional allocations unchanged. Writes
through existing references update shared storage; explicitly replacing a
terminal pointer, map or slice can replace that reference. These guarantees apply to one
operation and do not undo user callback side effects. Callers synchronize live
values and avoid structural changes during a walk.

Compilation, paths and walks have separate finite budgets. Engine caches retain
a bounded number of roots, rather than a fixed number of bytes. See the
[budget table](api.md#resource-budgets) for defaults and counting rules, and
[performance](performance.md) for allocation measurements.

Gowalker requires Go 1.27 and uses only the standard library. Its generic methods
provide typed entry points; paths and assignments are still checked at runtime.
