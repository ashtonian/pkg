package client

import "encoding/json"

// Array is an array of scalars of type E whose elements compare for
// equality and membership: keys, enums, booleans. Contains and the NULL
// tests take the array whole; Some and None test its elements, one at a
// time, against a condition built from Element.
//
// An array declared as a document member holds, as its value, its elements
// of type E: elements of another JSON kind are no elements, and a member
// with none is null.
type Array[E any] struct{ name string }

// NewArray returns the array named name.
func NewArray[E any](name string) Array[E] { return Array[E]{name: name} }

// Name is the field's name in the grammar.
func (a Array[E]) Name() string { return a.name }

// Element is the array's element. Its comparisons, combined by And, Or and
// Not, build the conditions Some and None take, and only those.
func (a Array[E]) Element() Field[E] { return Field[E]{} }

// Contains matches rows whose array holds every one of es. Contains of
// nothing matches every row whose array is not null.
func (a Array[E]) Contains(es ...E) Filter { return condition(a.name, "$contains", list(es)) }

// Null matches rows whose array is null.
func (a Array[E]) Null() Filter { return isNull(a.name) }

// NotNull matches rows whose array is not null.
func (a Array[E]) NotNull() Filter { return notNull(a.name) }

// Some matches rows with an element c holds for, every comparison in c
// holding for that one element. The empty filter matches rows with any
// element.
func (a Array[E]) Some(c Filter) Filter { return elements(a.name, "$some", c) }

// None matches rows with no element c holds for. The empty filter matches
// rows with no element.
func (a Array[E]) None(c Filter) Filter { return elements(a.name, "$none", c) }

// OrderedArray is an Array whose elements have an order: numbers,
// decimals, timestamps.
type OrderedArray[E any] struct{ Array[E] }

// NewOrderedArray returns the ordered array named name.
func NewOrderedArray[E any](name string) OrderedArray[E] {
	return OrderedArray[E]{NewArray[E](name)}
}

// Element is the array's element; see Array.Element.
func (a OrderedArray[E]) Element() Ordered[E] { return Ordered[E]{} }

// TextArray is an Array of strings, whose elements match by pattern and
// substring as well.
type TextArray struct{ OrderedArray[string] }

// NewTextArray returns the text array named name.
func NewTextArray(name string) TextArray { return TextArray{NewOrderedArray[string](name)} }

// Element is the array's element; see Array.Element.
func (a TextArray) Element() Text { return Text{} }

// elements is a condition whose operand is a condition on an array's
// element: c may hold no field's condition.
func elements(field, op string, c Filter) Filter {
	if c.err != nil {
		return c
	}
	if _, named := c.n.conditions(); named {
		return Filter{err: ErrElementCondition}
	}
	return condition(field, op, elementCondition{c.n})
}

// elementCondition encodes a condition on an element: its comparisons
// name no field.
type elementCondition struct{ n *node }

func (c elementCondition) MarshalJSON() ([]byte, error) { return json.Marshal(c.n.document()) }
