package client

// Document is a JSON document field whose values have type V.
type Document[V any] struct{ name string }

// NewDocument returns the document field named name.
func NewDocument[V any](name string) Document[V] { return Document[V]{name: name} }

// Name is the field's name in the grammar.
func (d Document[V]) Name() string { return d.name }

// Eq matches rows whose document equals v.
func (d Document[V]) Eq(v V) Filter { return condition(d.name, "$eq", v) }

// Ne matches rows whose document differs from v.
func (d Document[V]) Ne(v V) Filter { return condition(d.name, "$ne", v) }

// Contains matches rows whose document contains v: every member v encodes,
// at any depth, is in the document with the same value.
func (d Document[V]) Contains(v V) Filter { return condition(d.name, "$contains", v) }

// HasKey matches rows whose document has the top-level member key.
func (d Document[V]) HasKey(key string) Filter { return condition(d.name, "$has_key", key) }

// Null matches rows whose document is null.
func (d Document[V]) Null() Filter { return isNull(d.name) }

// NotNull matches rows whose document is not null.
func (d Document[V]) NotNull() Filter { return notNull(d.name) }

// Elements is a JSON array of objects whose element members the server
// declares; Some and None test one element against a filter over them.
type Elements[V any] struct{ Document[V] }

// NewElements returns the element array named name.
func NewElements[V any](name string) Elements[V] { return Elements[V]{NewDocument[V](name)} }

// Some matches rows with an element that f matches. f's fields are the
// element's members.
func (e Elements[V]) Some(f Filter) Filter { return nested(e.name, "$some", f) }

// None matches rows with no element that f matches.
func (e Elements[V]) None(f Filter) Filter { return nested(e.name, "$none", f) }

// Relation is a to-many relation, has-many or many-to-many, whose target
// keys have type K. Its key comparisons (Eq, In, Null, ...) ask whether a
// related row has that key; Some and None filter by the target's fields.
type Relation[K any] struct{ Field[K] }

// NewRelation returns the relation named name.
func NewRelation[K any](name string) Relation[K] { return Relation[K]{NewField[K](name)} }

// Some matches rows with a related row that f matches. f's fields are the
// target's; the empty filter matches rows with any related row.
func (r Relation[K]) Some(f Filter) Filter { return nested(r.name, "$some", f) }

// None matches rows with no related row that f matches; the empty filter
// matches rows with no related row.
func (r Relation[K]) None(f Filter) Filter { return nested(r.name, "$none", f) }

// Replace sets the relation's members to keys, as a JSON Patch operation.
func (r Relation[K]) Replace(keys ...K) Operation { return Replace(Pointer(r.name), list(keys)) }

// Add adds keys to the relation's members, as a JSON Patch operation.
func (r Relation[K]) Add(keys ...K) Operation { return Add(Pointer(r.name), list(keys)) }

// Remove removes keys from the relation's members, as a JSON Patch
// operation. Unlike Remove of a path, it carries the keys.
func (r Relation[K]) Remove(keys ...K) Operation {
	return Operation{op: "remove", path: Pointer(r.name), value: list(keys), hasValue: true}
}

// Reference is a to-one (belongs-to) relation whose target key has type K.
// Is and IsNot filter by the target's fields.
type Reference[K any] struct{ Field[K] }

// NewReference returns the reference named name.
func NewReference[K any](name string) Reference[K] { return Reference[K]{NewField[K](name)} }

// Is matches rows whose referenced row f matches. f's fields are the
// target's.
func (r Reference[K]) Is(f Filter) Filter { return nested(r.name, "$is", f) }

// IsNot matches rows whose referenced row f does not match, or that
// reference none.
func (r Reference[K]) IsNot(f Filter) Filter { return nested(r.name, "$is_not", f) }
