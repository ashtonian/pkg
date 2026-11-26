package patch

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/ashtonian/pkg/parse/core"
)

// An operation's value is held in one of two forms. A path whose Go type
// fixes its value's shape (a scalar, a struct, a typed map or slice) holds the
// value normalized to that type. Everywhere else — members inside a
// document, an interface-typed field, a path with no Go type — it holds the
// decoded JSON document: map[string]any, []any, string, bool, json.Number
// and nil, as decodeJSONDocument reads the encoding SQL stores. Policy, SQL
// and apply read that one form, so a typed Go value (a map[string]string, a
// struct) cannot hide members from a constraint that SQL then writes.

// documentValue is v in the decoded JSON document form: v itself when it
// already is one, else v read back through its JSON encoding. A value JSON
// cannot hold (NaN, a channel) is an error.
func documentValue(v any) (any, error) {
	if isDocument(v) {
		return v, nil
	}
	return documentOf(v)
}

// isDocument reports whether v is a decoded JSON document all the way down.
func isDocument(v any) bool {
	switch x := v.(type) {
	case nil, string, bool, json.Number:
		return true
	case map[string]any:
		for _, e := range x {
			if !isDocument(e) {
				return false
			}
		}
		return true
	case []any:
		for _, e := range x {
			if !isDocument(e) {
				return false
			}
		}
		return true
	}
	return false
}

// untypedElements is the value of an array path with no Go type: a scalar
// element stays as the dialect binds it, a composite one (a map, a struct, a
// slice) becomes its document, so its members are what policy reads.
func untypedElements(v any) (any, error) {
	items, ok := core.AnySlice(v)
	if !ok {
		return untypedElement(v)
	}
	out := make([]any, len(items))
	for i, item := range items {
		e, err := untypedElement(item)
		if err != nil {
			return nil, err
		}
		out[i] = e
	}
	return out, nil
}

// untypedElement is one element of an array path with no Go type, as
// untypedElements holds it.
func untypedElement(v any) (any, error) {
	k := reflect.ValueOf(v)
	for k.Kind() == reflect.Pointer && !k.IsNil() {
		k = k.Elem()
	}
	switch k.Kind() {
	case reflect.Map, reflect.Struct, reflect.Slice, reflect.Array:
		if k.Kind() == reflect.Slice && k.Type().Elem().Kind() == reflect.Uint8 {
			return v, nil
		}
		return documentValue(v)
	}
	return v, nil
}

// documentOf is v as encoding/json decodes its encoding, numbers kept as
// written: a value the fold owns and may change.
func documentOf(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var doc any
	err = decodeJSONDocument(raw, &doc)
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// decodeJSONDocument decodes raw as a document, numbers kept as written.
func decodeJSONDocument(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(dst)
}

// asDocument is value as a decoded JSON document holds it: a
// json.RawMessage decoded, a Go slice as []any, anything else as it is.
func asDocument(value any) any {
	switch v := value.(type) {
	case map[string]any, []any, nil, string, bool:
		return v
	case json.RawMessage:
		var doc any
		dec := json.NewDecoder(bytes.NewReader(v))
		dec.UseNumber()
		if dec.Decode(&doc) != nil {
			return value
		}
		return doc
	}
	if items, ok := core.AnySlice(value); ok {
		return items
	}
	return value
}
