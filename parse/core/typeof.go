package core

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"
	"uuid"
)

var (
	timeType          = reflect.TypeFor[time.Time]()
	rawJSONType       = reflect.TypeFor[json.RawMessage]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

// IsUUIDType reports a UUID type: a named 16-byte array whose zero value
// writes the nil UUID's text, 00000000-0000-0000-0000-000000000000, as the
// standard library's uuid.UUID, github.com/google/uuid and
// github.com/gofrs/uuid do. A 16-byte array that writes other text, such as
// a ULID or a hexadecimal hash, is a text value (TypeOf), and one that
// writes none is binary.
func IsUUIDType(t reflect.Type) bool { return byteArrayKindOf(t) == uuidBytes }

// byteArrayKind is what a named 16-byte array holds.
type byteArrayKind uint8

const (
	notByteArray byteArrayKind = iota
	binaryBytes
	uuidBytes
	textBytes
)

// nilUUIDText is the text of the zero UUID.
const nilUUIDText = "00000000-0000-0000-0000-000000000000"

// byteArrayKindOf classifies t once by what its zero value writes.
func byteArrayKindOf(t reflect.Type) byteArrayKind {
	if t.Kind() != reflect.Array || t.Len() != 16 || t.Elem().Kind() != reflect.Uint8 || t.Name() == "" {
		return notByteArray
	}
	if known, ok := byteArrayKinds.Load(t); ok {
		kind, _ := known.(byteArrayKind) // the only type stored
		return kind
	}
	kind := binaryBytes
	if text, ok := zeroText(t); ok {
		kind = textBytes
		if text == nilUUIDText {
			kind = uuidBytes
		}
	}
	byteArrayKinds.Store(t, kind)
	return kind
}

var byteArrayKinds sync.Map // reflect.Type -> byteArrayKind

// zeroText is the text t's zero value marshals to, through the value's or
// the pointer's MarshalText; ok is false when it has none or it fails.
func zeroText(t reflect.Type) (string, bool) {
	tm, ok := textMarshalerOf(reflect.Zero(t).Interface())
	if !ok {
		return "", false
	}
	text, err := tm.MarshalText()
	return string(text), err == nil
}

// UUIDText is v's canonical text when v is a UUID (IsUUIDType); ok is false
// for any other value.
func UUIDText(v any) (text string, ok bool) {
	if u, isStd := v.(uuid.UUID); isStd {
		return u.String(), true
	}
	if v == nil || !IsUUIDType(reflect.TypeOf(v)) {
		return "", false
	}
	if s, isStringer := v.(fmt.Stringer); isStringer {
		return s.String(), true // a UUID type's String is its canonical text
	}
	tm, ok := textMarshalerOf(v)
	if !ok {
		return "", false
	}
	b, err := tm.MarshalText()
	if err != nil {
		return "", false
	}
	return string(b), true
}

// TypeInfo is how a Go field type is advertised and stored.
type TypeInfo struct {
	Type FieldType
	// SQLType is the explicit storage declaration, empty for Go-only inference.
	SQLType SQLType
	// Elem is the element type of a FieldTypeArray; empty otherwise.
	Elem FieldType
	// NumberKind is the Go numeric kind of Type, or of Elem for an array;
	// reflect.Invalid when not numeric.
	NumberKind reflect.Kind
}

// TypeOf classifies the Go type of a field's column (pointers stripped).
// The classification is the storage model every backend shares:
//
//	string / int / float / bool kinds   string, int, float, bool
//	a 16-byte array writing other text  string, compared as its text
//	time.Time, a UUID type (IsUUIDType) timestamp, uuid
//	Date                                date
//	LocalTime, Interval                 time, interval
//	Inet, CIDR, MACAddr                 inet, cidr, macaddr
//	a decimal type (IsDecimalType)      decimal — PostgreSQL numeric; NUMERIC or REAL on SQLite
//	a nullable wrapper (NullWrapperOf)  its value's type: sql.NullInt64 int, sql.NullTime timestamp
//	slice or array of a scalar above,
//	other than decimal                  array — a PostgreSQL native array (TEXT[], BIGINT[], INET[], …); JSON text on SQLite
//	map, interface, json.RawMessage,
//	slice of anything else              json — a document: PostgreSQL jsonb; JSON text on SQLite
//	struct                              object — a JSON document with a fixed shape
//	[]byte, chan, func, …               any — no filter or patch semantics
//
// A document's members and elements are typed by MemberTypeOf.
func TypeOf(t reflect.Type) TypeInfo {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	w, wrapped := NullWrapperOf(t)
	if wrapped {
		return TypeOf(w.ValueType())
	}
	return MemberTypeOf(t)
}

// MemberTypeOf classifies the Go type of a JSON document's member or
// element as TypeOf classifies a column's, except that a nullable wrapper is
// the struct it is: a driver stores the wrapper's value through its Valuer,
// which encoding/json does not call, and a document holds what encoding/json
// writes ({"Int64":1,"Valid":true} for an sql.NullInt64).
func MemberTypeOf(t reflect.Type) TypeInfo {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if ft, ok := scalarType(t); ok {
		return TypeInfo{Type: ft, NumberKind: NumericKind(t)}
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		if t == rawJSONType {
			return TypeInfo{Type: FieldTypeJSON}
		}
		if t.Elem().Kind() == reflect.Uint8 {
			return TypeInfo{Type: FieldTypeAny} // binary
		}
		// A slice of decimals stays a document: their wire form is a string
		// per element, which no native array column holds as numbers.
		if elem, ok := scalarType(t.Elem()); ok && elem != FieldTypeDecimal {
			return TypeInfo{Type: FieldTypeArray, Elem: elem, NumberKind: NumericKind(t.Elem())}
		}
		return TypeInfo{Type: FieldTypeJSON}
	case reflect.Map, reflect.Interface:
		return TypeInfo{Type: FieldTypeJSON}
	case reflect.Struct:
		return TypeInfo{Type: FieldTypeObject}
	}
	return TypeInfo{Type: FieldTypeAny}
}

// scalarType maps the scalar Go types; pointers are not stripped.
func scalarType(t reflect.Type) (FieldType, bool) {
	if ft, ok := valueTypes[t]; ok {
		return ft, true
	}
	if t == timeType {
		return FieldTypeTimestamp, true
	}
	switch byteArrayKindOf(t) {
	case uuidBytes:
		return FieldTypeUUID, true
	case textBytes:
		return FieldTypeString, true
	}
	if IsDecimalType(t) {
		return FieldTypeDecimal, true
	}
	switch t.Kind() {
	case reflect.String:
		return FieldTypeString, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return FieldTypeInt, true
	case reflect.Float32, reflect.Float64:
		return FieldTypeFloat, true
	case reflect.Bool:
		return FieldTypeBool, true
	}
	return "", false
}

// valueTypes are this package's value types, each with one canonical text.
var valueTypes = map[reflect.Type]FieldType{
	reflect.TypeFor[Date]():      FieldTypeDate,
	reflect.TypeFor[LocalTime](): FieldTypeTime,
	reflect.TypeFor[Interval]():  FieldTypeInterval,
	reflect.TypeFor[Inet]():      FieldTypeInet,
	reflect.TypeFor[CIDR]():      FieldTypeCIDR,
	reflect.TypeFor[MACAddr]():   FieldTypeMACAddr,
}

// IsDocument reports whether values of ft are JSON documents (json, object).
func IsDocument(ft FieldType) bool {
	return ft == FieldTypeJSON || ft == FieldTypeObject
}
