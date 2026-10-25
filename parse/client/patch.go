package client

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Nullable is a member of a merge patch (RFC 7386) struct. The zero value is
// absent and, with the omitzero tag option, leaves the field alone; Null
// clears it; Set writes a value.
//
//	type DevicePatch struct {
//		Name client.Nullable[string] `json:"name,omitzero"`
//		Note client.Nullable[string] `json:"note,omitzero"`
//	}
//	body, err := json.Marshal(DevicePatch{Name: client.Set("pump"), Note: client.Null[string]()})
//	// {"name":"pump","note":null}
type Nullable[T any] struct {
	value T
	state nullableState
}

type nullableState uint8

const (
	nullableAbsent nullableState = iota
	nullableNull
	nullableSet
)

// Null clears a field.
func Null[T any]() Nullable[T] { return Nullable[T]{state: nullableNull} }

// Set writes v to a field.
func Set[T any](v T) Nullable[T] { return Nullable[T]{value: v, state: nullableSet} }

// IsZero reports whether n is absent, for the omitzero tag option.
func (n Nullable[T]) IsZero() bool { return n.state == nullableAbsent }

// IsNull reports whether n clears its field.
func (n Nullable[T]) IsNull() bool { return n.state == nullableNull }

// Get returns the value n writes and whether it writes one.
func (n Nullable[T]) Get() (T, bool) { return n.value, n.state == nullableSet }

// MarshalJSON encodes a set value, and null otherwise. An absent member is
// left out by its struct's omitzero option, never encoded.
func (n Nullable[T]) MarshalJSON() ([]byte, error) {
	if n.state != nullableSet {
		return []byte("null"), nil
	}
	return json.Marshal(n.value)
}

// UnmarshalJSON reads null as Null and any other value as Set.
func (n *Nullable[T]) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*n = Null[T]()
		return nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*n = Set(v)
	return nil
}

// Operation is one JSON Patch (RFC 6902) operation; a []Operation is a JSON
// Patch document. Build one with Add, Remove, Replace, Move, Copy, Test or
// a Relation's membership methods.
type Operation struct {
	op       string
	path     string
	from     string
	value    any
	hasValue bool
}

// Add adds value at path.
func Add(path string, value any) Operation {
	return Operation{op: "add", path: path, value: value, hasValue: true}
}

// Remove removes the value at path.
func Remove(path string) Operation { return Operation{op: "remove", path: path} }

// Replace replaces the value at path with value.
func Replace(path string, value any) Operation {
	return Operation{op: "replace", path: path, value: value, hasValue: true}
}

// Move moves the value at from to path.
func Move(from, path string) Operation { return Operation{op: "move", path: path, from: from} }

// Copy copies the value at from to path.
func Copy(from, path string) Operation { return Operation{op: "copy", path: path, from: from} }

// Test checks that the value at path equals value.
func Test(path string, value any) Operation {
	return Operation{op: "test", path: path, value: value, hasValue: true}
}

// MarshalJSON encodes the operation with the members its op takes: value
// for add, replace and test (null included) and for a relation's remove,
// from for move and copy.
func (o Operation) MarshalJSON() ([]byte, error) {
	w := struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		From  string          `json:"from,omitempty"`
		Value json.RawMessage `json:"value,omitempty"`
	}{Op: o.op, Path: o.path, From: o.from}
	if o.hasValue {
		v, err := json.Marshal(o.value)
		if err != nil {
			return nil, err
		}
		w.Value = v
	}
	return json.Marshal(w)
}

// Pointer is the JSON Pointer (RFC 6901) to a member: tokens are member
// names, outermost first, escaped as the pointer requires.
func Pointer(tokens ...string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(pointerEscaper.Replace(t))
	}
	return b.String()
}

var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")
