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
// a default "port". Types are kept unless splitting a sep field requires a
// wider container. Decode hooks are not run, and a custom decoder's value —
// an UnmarshalMapstructure or TextUnmarshaler type, or an interface — is left
// as written, as is every map key. A key an alias tag names is rewritten to
// its field's key, a string set for a field with a sep tag is split into the
// list its separator makes, and one set for a format:"json" field is parsed
// and walked as the value it holds. It records a source that spells one
// field two ways, every alias it rewrote, and every string that is not JSON.
type keyWalk struct {
	schema  *schemaNode
	twice   []string
	aliased []string // "old.key (now new.key)"
	notJSON []string // "key (line n)"; never the text
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
	n, err := schemaOf(t)
	if err != nil {
		return v.Interface()
	}
	out := reflect.MakeMapWithSize(stringKeyed(v.Type()), v.Len())
	written := make(map[string]reflect.Value, v.Len())
	for _, k := range v.MapKeys() {
		written[fmt.Sprint(k.Interface())] = v.MapIndex(k)
	}
	claimed := make(map[string]string, len(written)) // field key → the key written
	for _, k := range slices.Sorted(maps.Keys(written)) {
		f, alias := n.lookup(k)
		if f == nil {
			out.SetMapIndex(reflect.ValueOf(k).Convert(out.Type().Key()), written[k])
			continue
		}
		key, ft, tag := f.key, f.sf.Type, f.sf.Tag
		if prev, dup := claimed[key]; dup {
			at := path.field(key)
			if safe := diagnosticPath(w.schema, at); len(safe) < len(at) {
				w.twice = append(w.twice, safe.String()+" (sensitive contents)")
			} else {
				w.twice = append(w.twice, fmt.Sprintf("%s (as %q and %q)", at, prev, k))
			}
			continue
		}
		claimed[key] = k
		if alias {
			at := path.field(key)
			if safe := diagnosticPath(w.schema, at); len(safe) < len(at) {
				w.aliased = append(w.aliased, safe.String()+" (sensitive contents)")
			} else {
				w.aliased = append(w.aliased, fmt.Sprintf("%s (now %s)", path.field(k), at))
			}
		}
		raw := written[k]
		if s, ok := stringValue(raw); ok && jsonFormat(tag) {
			parsed, err := parseJSON([]byte(s))
			if err != nil {
				w.notJSON = append(w.notJSON, w.jsonProblem(path.field(key), s, err))
				continue
			}
			raw = reflect.ValueOf(&parsed).Elem() // JSON null stays an explicit null
		}
		val := w.elem(out.Type().Elem(), ft, raw, path.field(key))
		if sep, ok := tag.Lookup("sep"); ok && val.Kind() == reflect.String {
			val = reflect.ValueOf(splitList(val.String(), sep))
		}
		out = setMapValue(out, reflect.ValueOf(key).Convert(out.Type().Key()), val)
	}
	return out.Interface()
}

// mapValues follows the values of map v into element type elem; its keys are
// the caller's and stay as written.
func (w *keyWalk) mapValues(elem reflect.Type, v reflect.Value, path keyPath) any {
	out := reflect.MakeMapWithSize(v.Type(), v.Len())
	for _, k := range v.MapKeys() {
		out = setMapValue(out, k, w.elem(v.Type().Elem(), elem, v.MapIndex(k), path.key(fmt.Sprint(k.Interface()))))
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
		val := w.elem(v.Type().Elem(), elem, v.Index(i), path.index(i))
		if !val.Type().AssignableTo(out.Type().Elem()) {
			wide := reflect.MakeSlice(reflect.TypeFor[[]any](), v.Len(), v.Len())
			for j := range i {
				wide.Index(j).Set(out.Index(j))
			}
			out = wide
		}
		out.Index(i).Set(val)
	}
	return out.Interface()
}

// elem rewrites one held value, decoded into t, and returns it as a value
// ready for its container, widened if the rewrite changed its type.
func (w *keyWalk) elem(held, t reflect.Type, v reflect.Value, path keyPath) reflect.Value {
	if v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	if !v.IsValid() {
		return reflect.Zero(held)
	}
	r := reflect.ValueOf(w.value(t, v.Interface(), path))
	if !r.IsValid() {
		return reflect.Zero(held)
	}
	return r
}

// A sep rewrite can turn a typed map's string into a list.
func setMapValue(out, key, val reflect.Value) reflect.Value {
	if !val.Type().AssignableTo(out.Type().Elem()) {
		wide := reflect.MakeMapWithSize(reflect.MapOf(out.Type().Key(), reflect.TypeFor[any]()), out.Len()+1)
		for it := out.MapRange(); it.Next(); {
			wide.SetMapIndex(it.Key(), it.Value())
		}
		out = wide
	}
	out.SetMapIndex(key, val)
	return out
}

// stringKeyed is map type m keyed by strings: m itself when its keys are,
// else map[string]any.
func stringKeyed(m reflect.Type) reflect.Type {
	if m.Key().Kind() == reflect.String {
		return m
	}
	return reflect.TypeFor[map[string]any]()
}

// stringValue is the text v holds, when it holds a string.
func stringValue(v reflect.Value) (string, bool) {
	if v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	if !v.IsValid() || v.Kind() != reflect.String {
		return "", false
	}
	return v.String(), true
}

// jsonProblem names a key whose string is not JSON, by the line the parser
// stopped on; the parser's message, which can quote the text, is left out.
func (w *keyWalk) jsonProblem(at keyPath, s string, err error) string {
	shown := diagnosticPath(w.schema, at).orRoot()
	if line := parserLine(err, []byte(s)); line > 0 {
		return fmt.Sprintf("%s (line %d)", shown, line)
	}
	return shown
}

// aliasNames returns the former keys a field's alias tag lists.
func aliasNames(tag reflect.StructTag) []string {
	v, ok := tag.Lookup("alias")
	if !ok {
		return nil
	}
	return strings.Split(v, ",")
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
