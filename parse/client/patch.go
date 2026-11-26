package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// ErrAbsentMember reports an absent Nullable that reached encoding: its
// struct member is not tagged omitzero. Encoding it as null would clear the
// field, a write the caller never asked for.
var ErrAbsentMember = errors.New("client: an absent Nullable has no encoding; tag its struct member omitzero")

// Nullable is a member of a merge patch (RFC 7396) struct. The zero value is
// absent and, with the omitzero tag option, leaves the field alone; Null
// clears it; Set writes a value. Every Nullable member is tagged omitzero:
// encoding an absent one is ErrAbsentMember.
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

// Set writes v to a field. A nil slice or map writes the empty one; a nil
// pointer or interface, which encodes as null, is Null.
func Set[T any](v T) Nullable[T] {
	rv := reflect.ValueOf(&v).Elem()
	switch rv.Kind() {
	case reflect.Slice:
		if rv.IsNil() {
			rv.Set(reflect.MakeSlice(rv.Type(), 0, 0))
		}
	case reflect.Map:
		if rv.IsNil() {
			rv.Set(reflect.MakeMap(rv.Type()))
		}
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return Null[T]()
		}
	}
	return Nullable[T]{value: v, state: nullableSet}
}

// IsZero reports whether n is absent, for the omitzero tag option.
func (n Nullable[T]) IsZero() bool { return n.state == nullableAbsent }

// IsNull reports whether n clears its field.
func (n Nullable[T]) IsNull() bool { return n.state == nullableNull }

// Get returns the value n writes and whether it writes one.
func (n Nullable[T]) Get() (T, bool) { return n.value, n.state == nullableSet }

// MarshalJSON encodes a set value, and null for Null. An absent member is
// left out by its struct's omitzero option; one that reaches MarshalJSON is
// ErrAbsentMember.
func (n Nullable[T]) MarshalJSON() ([]byte, error) {
	switch n.state {
	case nullableSet:
		return json.Marshal(n.value)
	case nullableNull:
		return []byte("null"), nil
	}
	return nil, ErrAbsentMember
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

// The RFC 6902 operations the builders write, and parse's array_append
// extension.
const (
	patchAdd         = "add"
	patchRemove      = "remove"
	patchReplace     = "replace"
	patchMove        = "move"
	patchCopy        = "copy"
	patchTest        = "test"
	patchArrayAppend = "array_append"
)

// Operation is one JSON Patch (RFC 6902) operation; a []Operation is a JSON
// Patch document. Build one with Add, Remove, Replace, Move, Copy, Test,
// Append, an Array's Append or a Relation's membership methods.
type Operation struct {
	op    string
	path  string
	from  string
	value string // encoded JSON; empty for operations without a value
	err   error
}

// Err reports an operand encoding failure encountered while building the operation.
func (o Operation) Err() error { return o.err }

func valueOperation(op, path string, value any) Operation {
	raw, err := json.Marshal(value)
	if err != nil {
		return Operation{err: fmt.Errorf("%w: %s %s: %w", ErrOperand, op, path, err)}
	}
	return Operation{op: op, path: path, value: string(raw)}
}

// Add adds value at path.
func Add(path string, value any) Operation {
	return valueOperation(patchAdd, path, value)
}

// Remove removes the value at path.
func Remove(path string) Operation { return Operation{op: patchRemove, path: path} }

// Replace replaces the value at path with value.
func Replace(path string, value any) Operation {
	return valueOperation(patchReplace, path, value)
}

// Move moves the value at from to path.
func Move(from, path string) Operation { return Operation{op: patchMove, path: path, from: from} }

// Copy copies the value at from to path.
func Copy(from, path string) Operation { return Operation{op: patchCopy, path: path, from: from} }

// Test checks that the value at path equals value.
func Test(path string, value any) Operation {
	return valueOperation(patchTest, path, value)
}

// Append appends element to the array at path, creating the array when it
// is missing or null: parse's array_append extension, which a server
// compiles to one atomic SQL expression on an array column.
func Append(path string, element any) Operation {
	return valueOperation(patchArrayAppend, path, element)
}

// MarshalJSON encodes the operation with the members its op takes: value
// for add, replace, test and array_append (null included) and for a
// relation's remove, from for move and copy.
func (o Operation) MarshalJSON() ([]byte, error) {
	if o.err != nil {
		return nil, o.err
	}
	w := struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		From  string          `json:"from,omitempty"`
		Value json.RawMessage `json:"value,omitempty"`
	}{Op: o.op, Path: o.path, From: o.from, Value: json.RawMessage(o.value)}
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
