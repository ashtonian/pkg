package gowalker

import (
	"encoding"
	"encoding/json"
	"reflect"
)

// jsonCodesItself reports t or *t having a method encoding/json calls to
// code a value of t: MarshalJSON, UnmarshalJSON, AppendText, MarshalText,
// UnmarshalText, or encoding/json/v2's MarshalJSONTo and UnmarshalJSONFrom
// (allMethodTypes in Go 1.27's encoding/json/v2). gowalker does not import
// encoding/json/v2, which Go leaves out under GOEXPERIMENT=nojsonv2, so the
// last two are matched by name and signature. t is not an interface.
func jsonCodesItself(t reflect.Type) bool {
	methods := [...]reflect.Type{
		reflect.TypeFor[json.Marshaler](),
		reflect.TypeFor[json.Unmarshaler](),
		reflect.TypeFor[encoding.TextAppender](),
		reflect.TypeFor[encoding.TextMarshaler](),
		reflect.TypeFor[encoding.TextUnmarshaler](),
	}
	for _, iface := range methods {
		if implementsText(t, iface) {
			return true
		}
	}
	return jsontextMethod(t, "MarshalJSONTo", "Encoder") || jsontextMethod(t, "UnmarshalJSONFrom", "Decoder")
}

// jsontextMethod reports t or *t having the method name with
// encoding/json/v2's signature for it: func(*jsontext.<param>) error.
func jsontextMethod(t reflect.Type, name, param string) bool {
	for _, receiver := range [...]reflect.Type{t, reflect.PointerTo(t)} {
		method, ok := receiver.MethodByName(name)
		if !ok {
			continue
		}
		// A concrete type's method takes its receiver first.
		signature := method.Type
		if signature.NumIn() != 2 || signature.NumOut() != 1 || signature.Out(0) != reflect.TypeFor[error]() {
			continue
		}
		arg := signature.In(1)
		if arg.Kind() == reflect.Pointer && arg.Elem().PkgPath() == "encoding/json/jsontext" && arg.Elem().Name() == param {
			return true
		}
	}
	return false
}

// jsonDropsPrivateEmbed reports an unexported embedded struct, which tag
// names, that encoding/json drops: it cannot call a method through an
// unexported field, so it leaves out one whose struct type codes itself,
// or has IsZero under omitzero ("not exported for method calls" in Go
// 1.27's encoding/json/v2). An untagged one still promotes its fields.
func jsonDropsPrivateEmbed(sf reflect.StructField, tag string) bool {
	t := sf.Type
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if jsonCodesItself(t) {
		return true
	}
	return jsonTagOption(tag, "omitzero") && implementsText(t, reflect.TypeFor[interface{ IsZero() bool }]())
}
