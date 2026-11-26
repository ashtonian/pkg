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
	if held, ok := nullScanners.Load(t); ok {
		is, _ := held.(bool) // only bools are stored
		return is
	}
	is := scansNull(t)
	nullScanners.Store(t, is)
	return is
}

var nullScanners sync.Map // reflect.Type -> bool

var (
	scannerType = reflect.TypeFor[sql.Scanner]()
	valuerType  = reflect.TypeFor[driver.Valuer]()
)

// scansNull reports whether t's zero value writes NULL, scans NULL, and
// writes NULL again. A Scan or Value that panics on the zero value is one
// database/sql would panic in as well: such a type holds no NULL.
func scansNull(t reflect.Type) (held bool) {
	ptr := reflect.PointerTo(t)
	if !ptr.Implements(scannerType) || !ptr.Implements(valuerType) {
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
