package core

import (
	"database/sql"
	"database/sql/driver"
	"reflect"
	"sync"
)

// IsNullableType reports whether a field of type t holds SQL NULL: a
// pointer, slice, map or interface, whose nil is NULL, or a type whose zero
// value is NULL: it writes NULL (Value() returns nil), scans NULL (Scan(nil)
// succeeds) and writes NULL after it, as sql.NullInt64, sql.NullTime,
// sql.Null[T] and a driver's nullable types do. A type that scans NULL into
// a value it writes back, such as a UUID type that scans NULL as the nil
// UUID, that refuses NULL, as the decimal types and this package's value
// types do, or whose zero value writes a value, is not nullable: a patch
// clears a field to its zero value where SQL writes NULL, and the two must
// agree.
//
// It is the one rule for whether a column can be NULL as its field sees it:
// a sort key orders NULL greatest, a soft-delete column marks a live row
// with NULL, and a patch path accepts null.
func IsNullableType(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface:
		return true
	}
	held, ok := nullScanners.Load(t)
	if ok {
		is, _ := held.(bool) // only bools are stored
		return is
	}
	is := scansNull(t)
	nullScanners.Store(t, is)
	return is
}

// nullScanners holds scansNull's answer per type: reflect.Type -> bool.
var nullScanners sync.Map //nolint:gochecknoglobals // process-wide cache of immutable per-type data, bounded by the program's types

// scansNull reports whether t's zero value writes NULL, scans NULL, and
// writes NULL again. A Scan or Value that panics on the zero value is one
// database/sql would panic in as well: such a type holds no NULL.
func scansNull(t reflect.Type) (held bool) {
	ptr := reflect.PointerTo(t)
	if !ptr.Implements(reflect.TypeFor[sql.Scanner]()) || !ptr.Implements(reflect.TypeFor[driver.Valuer]()) {
		return false
	}
	defer func() {
		if recover() != nil {
			held = false
		}
	}()
	zero := reflect.New(t).Interface()
	scanner, _ := zero.(sql.Scanner) // ptr implements both
	valuer, _ := zero.(driver.Valuer)
	if !writesNull(valuer) || scanner.Scan(nil) != nil {
		return false
	}
	return writesNull(valuer)
}

// writesNull reports whether v's value is NULL.
func writesNull(v driver.Valuer) bool {
	value, err := v.Value()
	return err == nil && value == nil
}

// NullWrapper is the layout of a nullable wrapper: a struct of exactly two
// exported fields, a value and a bool named Valid, whose zero value is NULL
// (IsNullableType) and which writes a value once Valid is set, as
// database/sql's Null types, sql.Null[T], uuid.NullUUID,
// decimal.NullDecimal and pgx's two-field pgtype types are. Its column
// holds the value, NULL while Valid is false, so the column is typed as the
// value's type (TypeOf).
type NullWrapper struct {
	t            reflect.Type
	value, valid int
}

// NullWrapperOf is t's layout when t is a nullable wrapper.
func NullWrapperOf(t reflect.Type) (NullWrapper, bool) {
	if t.Kind() != reflect.Struct || t.NumField() != 2 || !IsNullableType(t) {
		return NullWrapper{}, false
	}
	w := NullWrapper{t: t, value: 0, valid: 1}
	if t.Field(0).Name == "Valid" {
		w.value, w.valid = 1, 0
	}
	value, valid := t.Field(w.value), t.Field(w.valid)
	if !value.IsExported() || valid.Name != "Valid" || valid.Type.Kind() != reflect.Bool || !w.writesValue() {
		return NullWrapper{}, false
	}
	return w, true
}

// writesValue reports whether a wrapper with Valid set writes no NULL, so
// that Valid is the flag its NULL hangs on: it writes its zero value, or
// refuses it as one no column holds (core.Date's). A Value that panics is
// one database/sql would panic in too.
func (w NullWrapper) writesValue() (writes bool) {
	defer func() {
		if recover() != nil {
			writes = false
		}
	}()
	held := reflect.New(w.t)
	held.Elem().Field(w.valid).SetBool(true)
	valuer, _ := held.Interface().(driver.Valuer) // the wrapper is nullable: its pointer is a Valuer
	value, err := valuer.Value()
	return err != nil || value != nil
}

// ValueType is the type of the value the wrapper holds.
func (w NullWrapper) ValueType() reflect.Type { return w.t.Field(w.value).Type }

// Held is the field of v, a wrapper, that holds its value, and false while
// v is NULL.
func (w NullWrapper) Held(v reflect.Value) (reflect.Value, bool) {
	return v.Field(w.value), v.Field(w.valid).Bool()
}

// Set marks v, a settable wrapper, as holding a value, and returns the field
// that holds it.
func (w NullWrapper) Set(v reflect.Value) reflect.Value {
	v.Field(w.valid).SetBool(true)
	return v.Field(w.value)
}
