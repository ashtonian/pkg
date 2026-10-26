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

// IsUUIDType reports a UUID type: a named 16-byte array that marshals to
// text, as the standard library's uuid.UUID, github.com/google/uuid and
// github.com/gofrs/uuid all are. A plain [16]byte is binary.
func IsUUIDType(t reflect.Type) bool {
	if t.Kind() != reflect.Array || t.Len() != 16 || t.Elem().Kind() != reflect.Uint8 || t.Name() == "" {
		return false
	}
	if known, ok := uuidTypes.Load(t); ok {
		is, _ := known.(bool) // only bools are stored
		return is
	}
	is := t.Implements(textMarshalerType) || reflect.PointerTo(t).Implements(textMarshalerType)
	uuidTypes.Store(t, is)
	return is
}

// uuidTypes caches IsUUIDType's method-set check per 16-byte array type.
var uuidTypes sync.Map // reflect.Type -> bool

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
	// IsUUIDType admits a type whose pointer is the TextMarshaler.
	tm, ok := v.(encoding.TextMarshaler)
	if !ok {
		p := reflect.New(reflect.TypeOf(v))
		p.Elem().Set(reflect.ValueOf(v))
		if tm, ok = p.Interface().(encoding.TextMarshaler); !ok {
			return "", false
		}
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

// TypeOf classifies a Go field type (pointers stripped). The classification
// is the storage model every backend shares:
//
//	string / int / float / bool kinds   string, int, float, bool
//	time.Time, a UUID type (IsUUIDType) timestamp, uuid
//	slice or array of those scalars     array — a PostgreSQL native array (TEXT[], BIGINT[], …); JSON text on SQLite
//	map, interface, json.RawMessage,
//	slice of anything else              json — a document: PostgreSQL jsonb; JSON text on SQLite
//	struct                              object — a JSON document with a fixed shape
//	[]byte, chan, func, …               any — no filter or patch semantics
func TypeOf(t reflect.Type) TypeInfo {
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
		if elem, ok := scalarType(t.Elem()); ok {
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
	if t == reflect.TypeFor[Date]() {
		return FieldTypeDate, true
	}
	if t == timeType {
		return FieldTypeTimestamp, true
	}
	if IsUUIDType(t) {
		return FieldTypeUUID, true
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

// IsDocument reports whether values of ft are JSON documents (json, object).
func IsDocument(ft FieldType) bool {
	return ft == FieldTypeJSON || ft == FieldTypeObject
}
