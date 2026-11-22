package core

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
)

// Wire is how a field's values travel in JSON when that differs from its
// data type's own form. The zero Wire is the data type's form.
type Wire string

// WireString is the form encoding/json gives a bool, integer or float field
// tagged json:",string": a JSON string holding the value's JSON text, so a
// 64-bit integer survives a client whose numbers are doubles. Requests give
// such a field's values in the same form: "9007199254740993", not
// 9007199254740993.
const WireString Wire = "string"

var jsonMarshalerType = reflect.TypeFor[json.Marshaler]()

// Wire is the field's wire form, as encoding/json decides it: WireString for
// a bool, integer or float field whose field-name tag carries the string
// option, else the zero Wire. A type that marshals itself
// (json.Marshaler, encoding.TextMarshaler) keeps its own form, since
// encoding/json ignores the option for it. A string field's option, which
// encoding/json applies by quoting the string a second time, changes nothing
// parse reads.
func (f *Field) Wire() Wire {
	if !f.HasFieldNameOption("string") {
		return ""
	}
	t := f.Type
	if t.Name() == "" && t.Kind() == reflect.Pointer {
		t = t.Elem() // encoding/json follows one unnamed pointer
	}
	for _, m := range []reflect.Type{jsonMarshalerType, textMarshalerType} {
		if t.Implements(m) || reflect.PointerTo(t).Implements(m) {
			return ""
		}
	}
	switch t.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return WireString
	}
	return ""
}

// jsonNumberLiteral is a JSON number literal (RFC 8259 §6), whole.
var jsonNumberLiteral = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

// FromWire is the value a request gave in wire form w for a field of data
// type t, ready for NormalizeScalar. The zero Wire passes v through. Under
// WireString, v must be a JSON string holding the value's JSON text,
// exactly as encoding/json reads a quoted field: a JSON number for an int or
// float field (json.Number), true or false for a bool. A JSON number or
// boolean given bare is an error naming the expected form; nil passes
// through, for the caller's null rules.
func FromWire(w Wire, t FieldType, v any) (any, error) {
	if w != WireString || v == nil {
		return v, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("expected the value as a JSON string (json:\",string\"), got %s", jsonKind(v))
	}
	switch t {
	case FieldTypeInt, FieldTypeFloat:
		if !jsonNumberLiteral.MatchString(s) {
			return nil, fmt.Errorf("%q does not hold a JSON number", s)
		}
		return json.Number(s), nil
	case FieldTypeBool:
		switch s {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return nil, fmt.Errorf("%q does not hold true or false", s)
	}
	return nil, fmt.Errorf("a %s field has no string wire form", t)
}

// ToWire is the canonical value v (int64, uint64, float64 or bool, as
// NormalizeScalar returns them) in wire form w: under WireString, the JSON
// string encoding/json writes for it, so a document holding it decodes back
// into the field. The zero Wire and nil pass v through.
func ToWire(w Wire, v any) (any, error) {
	if w != WireString || v == nil {
		return v, nil
	}
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(x), nil
	}
	return nil, fmt.Errorf("%T has no string wire form", v)
}

// jsonKind names a decoded JSON value's kind for messages.
func jsonKind(v any) string {
	switch v.(type) {
	case json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "a number"
	case bool:
		return "a boolean"
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	}
	return fmt.Sprintf("%T", v)
}
