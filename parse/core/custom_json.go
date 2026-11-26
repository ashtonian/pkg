package core

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"reflect"
)

// CustomJSON reports a type that encodes or decodes itself as JSON: it has
// encoding/json's MarshalJSON or UnmarshalJSON, or encoding/json/v2's
// MarshalJSONTo or UnmarshalJSONFrom, which encoding/json honours too, on
// its value or its pointer. Its JSON need not follow its Go fields, so what
// a document of it holds is the type's own business. A nil type is false.
func CustomJSON(t reflect.Type) bool {
	return MarshalsJSON(t) ||
		implementsOnValueOrPointer(t, reflect.TypeFor[json.Unmarshaler]()) ||
		implementsOnValueOrPointer(t, reflect.TypeFor[jsonv2.UnmarshalerFrom]())
}

// MarshalsJSON reports a type that encodes itself as JSON: CustomJSON's
// marshaling half, MarshalJSON or MarshalJSONTo on its value or its
// pointer. A nil type is false.
func MarshalsJSON(t reflect.Type) bool {
	return implementsOnValueOrPointer(t, reflect.TypeFor[json.Marshaler]()) ||
		implementsOnValueOrPointer(t, reflect.TypeFor[jsonv2.MarshalerTo]())
}

// implementsOnValueOrPointer reports t or *t implementing iface, as
// encoding/json looks for a method on an addressable value's pointer.
func implementsOnValueOrPointer(t, iface reflect.Type) bool {
	if t == nil {
		return false
	}
	return t.Implements(iface) || reflect.PointerTo(t).Implements(iface)
}
