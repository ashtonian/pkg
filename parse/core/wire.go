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

// stringOption is encoding/json's field-name tag option that quotes a
// scalar field.
const stringOption = "string"

// Quoted reports whether encoding/json quotes the field: its field-name tag
// carries the string option, and its type — through one unnamed pointer,
// as encoding/json follows — is a bool, integer, float or string that does
// not marshal itself (json.Marshaler, encoding.TextMarshaler, on the value
// or its pointer). The option is read from the generator's field-name tag,
// the tag parse names fields by.
func (f *Field) Quoted() bool {
	if !f.HasFieldNameOption(stringOption) {
		return false
	}
	t := f.Type
	if t.Name() == "" && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	for _, m := range []reflect.Type{jsonMarshalerType, textMarshalerType} {
		if t.Implements(m) || reflect.PointerTo(t).Implements(m) {
			return false
		}
	}
	switch t.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.String:
		return true
	}
	return false
}

// Wire is the field's wire form: WireString for a bool, integer or float
// field encoding/json quotes (Quoted), else the zero Wire. A quoted string
// field, which encoding/json quotes a second time, keeps the string form:
// parse compares its text.
func (f *Field) Wire() Wire {
	if !f.Quoted() {
		return ""
	}
	t := f.Type
	if t.Name() == "" && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.String {
		return ""
	}
	return WireString
}

// MemberWire is the wire form of the struct member path names inside a
// document of Go type t: each segment a member's field-name, through nested
// structs and pointers to them. The zero Wire when the path leaves structs
// (a map or any member) or names no member, which a document's own rules
// then judge.
func (g *SchemaGenerator) MemberWire(t reflect.Type, path []string) (Wire, error) {
	for i, seg := range path {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return "", nil
		}
		walk, err := g.StructFields(t)
		if err != nil {
			return "", err
		}
		var member *Field
		for _, f := range walk.Fields {
			if f.External == seg {
				member = f
				break
			}
		}
		if member == nil {
			return "", nil
		}
		if i == len(path)-1 {
			return member.Wire(), nil
		}
		t = member.Type
	}
	return "", nil
}

// jsonNumberLiteral is a JSON number literal (RFC 8259 §6), whole.
var jsonNumberLiteral = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

// FromWire is a request's value for a field of data type t in wire form w,
// ready for NormalizeScalar. The zero Wire passes v through. Under
// WireString:
//
//   - a JSON string holds the value's JSON text: a JSON number for an int or
//     float field (returned as json.Number), true or false for a bool. The
//     syntax is JSON's, stricter than encoding/json's quoted decoding (no
//     "+1", "01", " 1", "NaN"); an integer may be any integral spelling
//     ("1.0", "1e3"), as parse reads integers anywhere;
//   - a bare JSON number (json.Number, which request bodies decode to) is an
//     error naming the expected form;
//   - a Go number or bool, as code and policy pass, is the value itself;
//   - nil passes through, for the caller's null rules.
func FromWire(w Wire, t FieldType, v any) (any, error) {
	if w != WireString || v == nil {
		return v, nil
	}
	switch x := v.(type) {
	case string:
		return unquote(t, x)
	case json.Number:
		return nil, fmt.Errorf("expected the value as a JSON string holding it (json:\",string\"), got the bare number %s", x)
	case bool,
		int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return v, nil
	}
	return nil, fmt.Errorf("expected the value as a JSON string holding it (json:\",string\"), got %s", jsonKind(v))
}

func unquote(t FieldType, s string) (any, error) {
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
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	}
	return fmt.Sprintf("%T", v)
}
