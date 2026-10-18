// Package gowalker helps libraries discover Go struct fields, read their tags,
// and reuse field metadata and accessors across operations.
//
// Create an [Engine] with [New] and compile a type without an instance using
// Engine.Compile[T]. [Schema.Root] exposes field descriptions; [Field.Tags]
// returns raw annotations such as env and default. The consuming library
// decides how to interpret and apply those annotations.
//
// For explicit tag names such as db:"id", [WithTaggedFields] selects named,
// exported fields as whole values. [Schema.AccessName] finds one literal field
// name or alias and returns a cached [Accessor] to read or write it across
// values. [WithFieldResolver] supports custom names, aliases, skip/inline rules
// and field-specific leaves. [WithAtomicType] makes every use of a type a leaf.
//
// Use [Schema.ResolveText] for nested paths such as Database.Port and retain
// the accessor for reuse. [Get], [GetAs] and [Set] are one-call helpers using Go
// field names. Get returns a [Value]: [Present] includes zero values, [Nil] is
// a reached nil location, and [Missing] means an absent map entry or nil
// ancestor. GetAs reports [ErrMissing] for that last case. Errors support
// [errors.Is] and [errors.As].
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
