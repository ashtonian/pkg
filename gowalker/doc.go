// Package gowalker inspects Go types, reads and writes nested values, and walks
// type or value graphs using reusable reflection metadata.
//
// For occasional access, use [Get], [GetAs] and [Set] with text paths such as
// Database.Port or Servers["primary"].Ports[0]. Get returns a [Value]: [Present]
// includes zero values, [Nil] is a reached nil location, and [Missing] means an
// absent map entry or nil ancestor. Missing is not an error from Get; GetAs
// reports [ErrMissing] instead. Errors support [errors.Is] and [errors.As].
//
// Libraries construct an [Engine] with [New], compile one [Schema] per root
// type, and reuse [Accessor] handles for repeated paths. [WithFieldResolver]
// supplies names, aliases, skip/inline rules and field-specific leaves.
// [WithAtomicType] makes every use of an exact type a leaf. A leaf can be read
// or replaced as a whole, but paths and walkers do not descend into it.
//
// Compile Row for ordinary entity access: reads accept Row or *Row, and writes
// require *Row. Compile *Row when the pointer itself is the declared root;
// writes then require **Row. Reflected entry points use the exact declared
// root directly. All writes follow Go assignment rules unless an explicit
// option enables allocation or lossless numeric conversion.
//
// Schemas and accessors are safe to reuse concurrently with separate values.
// Callers synchronize shared values. Reads borrow references without allocating
// missing structure or invoking value methods. A failed core write leaves the
// value and optional allocations unchanged. Walks have finite budgets and
// support pruning and early stop; structurally mutate values after walking.
package gowalker
