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
	unmarshalers := jsonUnmarshalers()
	return MarshalsJSON(t) || implementsOnValueOrPointer(t, unmarshalers[:]...)
}

// MarshalsJSON reports a type that encodes itself as JSON: CustomJSON's
// marshaling half, MarshalJSON or MarshalJSONTo on its value or its
// pointer. A nil type is false.
func MarshalsJSON(t reflect.Type) bool {
	marshalers := jsonMarshalers()
	return implementsOnValueOrPointer(t, marshalers[:]...)
}

// jsonMarshalers and jsonUnmarshalers are the interfaces by which a type
// encodes or decodes itself as JSON, as arrays, so a check allocates
// nothing.
func jsonMarshalers() [2]reflect.Type {
	marshalers := [2]reflect.Type{reflect.TypeFor[json.Marshaler](), reflect.TypeFor[jsonv2.MarshalerTo]()}
	return marshalers
}

func jsonUnmarshalers() [2]reflect.Type {
	unmarshalers := [2]reflect.Type{reflect.TypeFor[json.Unmarshaler](), reflect.TypeFor[jsonv2.UnmarshalerFrom]()}
	return unmarshalers
}

// customJSONInterfaces are all four, for an engine that stops at every type
// CustomJSON reports.
func customJSONInterfaces() []reflect.Type {
	marshalers, unmarshalers := jsonMarshalers(), jsonUnmarshalers()
	return append(marshalers[:], unmarshalers[:]...)
}

// implementsOnValueOrPointer reports t or *t implementing one of ifaces, as
// encoding/json looks for a method on an addressable value's pointer.
func implementsOnValueOrPointer(t reflect.Type, ifaces ...reflect.Type) bool {
	if t == nil {
		return false
	}
	for _, iface := range ifaces {
		if t.Implements(iface) || reflect.PointerTo(t).Implements(iface) {
			return true
		}
	}
	return false
}
