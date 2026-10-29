package client

// The field types follow the operators each value type can declare on the
// server. A field the server declares with fewer operators refuses the
// others (E2072), as it refuses any operator it does not allow.

// Field is a filterable field whose values have type V, compared for
// equality, membership and null: a key, an enum, a boolean.
type Field[V any] struct{ name string }

// NewField returns the field named name.
func NewField[V any](name string) Field[V] { return Field[V]{name: name} }

// Name is the field's name in the grammar.
func (f Field[V]) Name() string { return f.name }

// Eq matches rows whose field equals v.
func (f Field[V]) Eq(v V) Filter { return condition(f.name, "$eq", v) }

// Ne matches rows whose field differs from v.
func (f Field[V]) Ne(v V) Filter { return condition(f.name, "$ne", v) }

// In matches rows whose field is one of vs.
func (f Field[V]) In(vs ...V) Filter { return condition(f.name, "$in", list(vs)) }

// Nin matches rows whose field is none of vs.
func (f Field[V]) Nin(vs ...V) Filter { return condition(f.name, "$nin", list(vs)) }

// Null matches rows whose field is null.
func (f Field[V]) Null() Filter { return isNull(f.name) }

// NotNull matches rows whose field is not null.
func (f Field[V]) NotNull() Filter { return notNull(f.name) }

func isNull(name string) Filter  { return condition(name, "$null", true) }
func notNull(name string) Filter { return condition(name, "$not_null", true) }

// Ordered is a field whose values have an order: a number, a decimal (any
// type that marshals as its exact text, such as shopspring's), a timestamp.
type Ordered[V any] struct{ Field[V] }

// NewOrdered returns the ordered field named name.
func NewOrdered[V any](name string) Ordered[V] { return Ordered[V]{Field[V]{name: name}} }

// Gt matches rows whose field is greater than v.
func (f Ordered[V]) Gt(v V) Filter { return condition(f.name, "$gt", v) }

// Gte matches rows whose field is at least v.
func (f Ordered[V]) Gte(v V) Filter { return condition(f.name, "$gte", v) }

// Lt matches rows whose field is less than v.
func (f Ordered[V]) Lt(v V) Filter { return condition(f.name, "$lt", v) }

// Lte matches rows whose field is at most v.
func (f Ordered[V]) Lte(v V) Filter { return condition(f.name, "$lte", v) }

// Text is a string field, matched by pattern and substring as well.
type Text struct{ Ordered[string] }

// NewText returns the text field named name.
func NewText(name string) Text { return Text{NewOrdered[string](name)} }

// Like matches rows whose field matches the SQL LIKE pattern (% any run, _
// one character, \ escapes).
func (f Text) Like(pattern string) Filter { return condition(f.name, "$like", pattern) }

// ILike is Like, ignoring case.
func (f Text) ILike(pattern string) Filter { return condition(f.name, "$ilike", pattern) }

// Contains matches rows whose field contains s.
func (f Text) Contains(s string) Filter { return condition(f.name, "$contains", s) }

// Time is a timestamp field. Its operands are Moments: an absolute time, or
// one relative to the server's clock.
type Time = Ordered[Moment]

// NewTime returns the timestamp field named name.
func NewTime(name string) Time { return NewOrdered[Moment](name) }

// SortKey sorts by a field. A sort-only field is a SortKey; the Sortable
// field types embed one.
type SortKey struct{ key string }

// NewSortKey returns the sort key of the field named name.
func NewSortKey(name string) SortKey { return SortKey{key: name} }

// Asc sorts by the field, ascending.
func (k SortKey) Asc() Order { return Order{field: k.key} }

// Desc sorts by the field, descending.
func (k SortKey) Desc() Order { return Order{field: k.key, desc: true} }

// SortableField is a Field the server also sorts by.
type SortableField[V any] struct {
	Field[V]
	SortKey
}

// NewSortableField returns the sortable field named name.
func NewSortableField[V any](name string) SortableField[V] {
	return SortableField[V]{NewField[V](name), NewSortKey(name)}
}

// SortableOrdered is an Ordered field the server also sorts by.
type SortableOrdered[V any] struct {
	Ordered[V]
	SortKey
}

// NewSortableOrdered returns the sortable ordered field named name.
func NewSortableOrdered[V any](name string) SortableOrdered[V] {
	return SortableOrdered[V]{NewOrdered[V](name), NewSortKey(name)}
}

// SortableText is a Text field the server also sorts by.
type SortableText struct {
	Text
	SortKey
}

// NewSortableText returns the sortable text field named name.
func NewSortableText(name string) SortableText {
	return SortableText{NewText(name), NewSortKey(name)}
}

// SortableTime is a Time field the server also sorts by.
type SortableTime = SortableOrdered[Moment]

// NewSortableTime returns the sortable timestamp field named name.
func NewSortableTime(name string) SortableTime { return NewSortableOrdered[Moment](name) }
