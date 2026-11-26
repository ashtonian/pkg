package core

import (
	"database/sql/driver"
	"fmt"
	"reflect"

	"github.com/ashtonian/pkg/gowalker"
)

var valuerType = reflect.TypeFor[driver.Valuer]()

// maxValuerDepth bounds how many Valuers StoredValue reads in a chain, each
// returning the next, so a Valuer that returns itself cannot loop.
const maxValuerDepth = 8

// Deref follows pointers and interfaces to the value they hold, at any
// depth; it is nil for a nil value, pointer or interface.
func Deref(v any) any {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return nil
	}
	return rv.Interface()
}

// StoredValue is the value the database holds for v, as a driver binds it:
// v followed through pointers and interfaces (Deref), then read through its
// driver.Valuer, again while the result is a Valuer, a pointer-receiver
// Valuer included. null reports SQL NULL: a nil value, pointer or
// interface, or a Valuer whose value is nil. A Valuer that fails is an
// error.
//
// A value of a type TypeOf classifies by the type itself — time.Time, a
// UUID type (IsUUIDType), a decimal type (IsDecimalType), Date, LocalTime,
// Interval, Inet, CIDR and MACAddr — is kept as it is. Where such a type
// has a Valuer, it writes the canonical text NormalizeScalar gives the
// value, so keeping the type loses nothing, and the value still compares
// as its type: a decimal by value, a UUID as a UUID. Every other Valuer
// (sql.NullString, an application's own codec) stands for the value it
// returns. Cursors, facets, memberships and relation keys read stored
// values through this one rule.
func StoredValue(v any) (value any, null bool, err error) {
	for depth := 0; ; depth++ {
		v = Deref(v)
		if v == nil {
			return nil, true, nil
		}
		if typedScalar(reflect.TypeOf(v)) {
			return v, false, nil
		}
		valuer, ok := valuerOf(v)
		if !ok {
			return v, false, nil
		}
		if depth == maxValuerDepth {
			return nil, false, fmt.Errorf("reading the value of %s: more than %d Valuers in a chain", gowalker.TypeText(reflect.TypeOf(v)), maxValuerDepth)
		}
		next, err := valuer.Value()
		if err != nil {
			return nil, false, fmt.Errorf("reading the value of %s: %w", gowalker.TypeText(reflect.TypeOf(v)), err)
		}
		v = next
	}
}

// typedScalar reports a type TypeOf classifies by the type itself rather
// than by its kind: one whose canonical form parse defines.
func typedScalar(t reflect.Type) bool {
	if _, ok := valueTypes[t]; ok {
		return true
	}
	return t == timeType || IsUUIDType(t) || IsDecimalType(t)
}

// valuerOf is v's driver.Valuer, its pointer's for a pointer-receiver
// method: a value read from a row need not be addressable, so it is given
// a copy rather than losing its pointer methods.
func valuerOf(v any) (driver.Valuer, bool) {
	if valuer, ok := v.(driver.Valuer); ok {
		return valuer, true
	}
	rv := reflect.ValueOf(v)
	if !reflect.PointerTo(rv.Type()).Implements(valuerType) {
		return nil, false
	}
	valuer, ok := addressable(rv).Interface().(driver.Valuer)
	return valuer, ok
}
