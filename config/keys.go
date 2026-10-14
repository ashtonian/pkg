package config

import (
	"encoding"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/go-viper/mapstructure/v2"
)

// keyWalk rewrites one source's tree so every key naming a struct field is
// spelled as the field is. Sources merge key by key, and mapstructure
// prefers a key spelled exactly like the field over one that matches only in
// letter case; without the rewrite, a higher-precedence "Port" would lose to
// a default "port". It changes spelling only: values and their types are
// kept, decode hooks are not run, and a value a custom decoder takes whole —
// an UnmarshalMapstructure or TextUnmarshaler type, or an interface — is left
// as written, as is every map key. It records a source that spells one field
// two ways.
type keyWalk struct {
	twice []string
}

// tree rewrites the top level of a source.
func (w *keyWalk) tree(root reflect.Type, data map[string]any) map[string]any {
	out, ok := w.value(root, data, nil).(map[string]any)
	if !ok {
		return data
	}
	return out
}

var (
	mapstructureUnmarshaler = reflect.TypeFor[mapstructure.Unmarshaler]()
	textUnmarshaler         = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// opaque reports a type decoding hands its input whole: it decodes itself,
// or it is an interface.
func opaque(t reflect.Type) bool {
	if t.Kind() == reflect.Interface {
		return true
	}
	p := reflect.PointerTo(t)
	return p.Implements(mapstructureUnmarshaler) || t.Kind() == reflect.Struct && p.Implements(textUnmarshaler)
}

func (w *keyWalk) value(t reflect.Type, data any, path keyPath) any {
	if data == nil {
		return nil
	}
	t = derefType(t)
	if opaque(t) {
		return data
	}
	v := reflect.ValueOf(data)
	switch {
	case t.Kind() == reflect.Struct && v.Kind() == reflect.Map:
		return w.structMap(t, v, path)
	case t.Kind() == reflect.Map && v.Kind() == reflect.Map:
		return w.mapValues(t.Elem(), v, path)
	case t.Kind() == reflect.Map && isList(v):
		return w.elements(t, v, path) // mapstructure merges a list of maps into one map
	case (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) && isList(v):
		return w.elements(t.Elem(), v, path)
	case (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) && v.Kind() == reflect.Map:
		return w.value(t.Elem(), data, path.index(0)) // weakly typed: a map is a one-element slice
	}
	return data
}

func isList(v reflect.Value) bool {
	return v.Kind() == reflect.Slice || v.Kind() == reflect.Array
}

// structMap rewrites the keys of map v, decoded into struct type t. A map
// keyed by anything but strings is keyed by their text, as koanf keys it.
func (w *keyWalk) structMap(t reflect.Type, v reflect.Value, path keyPath) any {
	out := reflect.MakeMapWithSize(stringKeyed(v.Type()), v.Len())
	fields := structKeys(t)
	written := make(map[string]reflect.Value, v.Len())
	for _, k := range v.MapKeys() {
		written[fmt.Sprint(k.Interface())] = v.MapIndex(k)
	}
	claimed := make(map[string]string, len(written)) // field key → the key written
	for _, k := range slices.Sorted(maps.Keys(written)) {
		key, ft, ok := matchKey(fields, k)
		if !ok {
			out.SetMapIndex(reflect.ValueOf(k).Convert(out.Type().Key()), written[k])
			continue
		}
		if prev, dup := claimed[key]; dup {
			w.twice = append(w.twice, fmt.Sprintf("%s (as %q and %q)", path.field(key), prev, k))
			continue
		}
		claimed[key] = k
		out.SetMapIndex(reflect.ValueOf(key).Convert(out.Type().Key()), w.elem(out.Type().Elem(), ft, written[k], path.field(key)))
	}
	return out.Interface()
}

// mapValues follows the values of map v into element type elem; its keys are
// the caller's and stay as written.
func (w *keyWalk) mapValues(elem reflect.Type, v reflect.Value, path keyPath) any {
	out := reflect.MakeMapWithSize(v.Type(), v.Len())
	for _, k := range v.MapKeys() {
		out.SetMapIndex(k, w.elem(v.Type().Elem(), elem, v.MapIndex(k), path.key(fmt.Sprint(k.Interface()))))
	}
	return out.Interface()
}

// elements follows each element of list v into type elem.
func (w *keyWalk) elements(elem reflect.Type, v reflect.Value, path keyPath) any {
	out := reflect.New(v.Type()).Elem()
	if v.Kind() == reflect.Slice {
		out = reflect.MakeSlice(v.Type(), v.Len(), v.Len())
	}
	for i := range v.Len() {
		out.Index(i).Set(w.elem(v.Type().Elem(), elem, v.Index(i), path.index(i)))
	}
	return out.Interface()
}

// elem rewrites one held value, decoded into t, and returns it as a value
// of the holding container's element type held.
func (w *keyWalk) elem(held, t reflect.Type, v reflect.Value, path keyPath) reflect.Value {
	if v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	if !v.IsValid() {
		return reflect.Zero(held)
	}
	r := reflect.ValueOf(w.value(t, v.Interface(), path))
	if !r.IsValid() || !r.Type().AssignableTo(held) {
		return v // the rewrite changed its type; keep it as written
	}
	return r
}

// stringKeyed is map type m keyed by strings: m itself when its keys are,
// else map[string]any.
func stringKeyed(m reflect.Type) reflect.Type {
	if m.Key().Kind() == reflect.String {
		return m
	}
	return reflect.TypeFor[map[string]any]()
}

// structKeys returns the keys struct type t decodes, with their types,
// looking through squashed structs.
func structKeys(t reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	var collect func(t reflect.Type)
	collect = func(t reflect.Type) {
		for kf := range keyedFields(t) {
			switch {
			case kf.key == "" && derefType(kf.field.Type).Kind() == reflect.Struct:
				collect(derefType(kf.field.Type))
			case kf.key != "":
				fields[kf.key] = kf.field.Type
			}
		}
	}
	collect(t)
	return fields
}

// matchKey finds the field a data key decodes into: spelled exactly, else
// matching in letter case, as mapstructure matches it.
func matchKey(fields map[string]reflect.Type, k string) (string, reflect.Type, bool) {
	if ft, ok := fields[k]; ok {
		return k, ft, true
	}
	for key, ft := range fields {
		if strings.EqualFold(key, k) {
			return key, ft, true
		}
	}
	return "", nil, false
}

// textKeyed is data with each map keyed by anything but strings, held in
// a map or list of it, keyed by its keys' text — koanf's normalization of a
// source before it merges, applied to a copy so both the merge and the
// origins see one tree. What needs no change is shared.
func textKeyed(data map[string]any) map[string]any {
	out, _ := stringKeysIn(data)
	return out.(map[string]any)
}

func stringKeysIn(v any) (any, bool) {
	switch x := v.(type) {
	case map[string]any:
		var out map[string]any
		for k, e := range x {
			if n, changed := stringKeysIn(e); changed {
				if out == nil {
					out = maps.Clone(x)
				}
				out[k] = n
			}
		}
		if out == nil {
			return x, false
		}
		return out, true
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			n, _ := stringKeysIn(e)
			out[fmt.Sprintf("%v", k)] = n
		}
		return out, true
	case []any:
		// koanf looks at a list's maps only, not at lists inside it.
		var out []any
		for i, e := range x {
			switch e.(type) {
			case map[string]any, map[any]any:
				if n, changed := stringKeysIn(e); changed {
					if out == nil {
						out = slices.Clone(x)
					}
					out[i] = n
				}
			}
		}
		if out == nil {
			return x, false
		}
		return out, true
	}
	return v, false
}
