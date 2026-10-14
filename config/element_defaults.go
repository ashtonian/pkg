package config

import (
	"reflect"
	"strings"
)

// fillElementDefaults applies the `default` tags of a map's or slice's
// element struct to every entry the merged tree holds, below whatever the
// sources set: an entry features.audit gets features.audit.limit = "50" from
// `Limit int default:"50"` unless a source set it. Tag defaults for keys
// outside maps and slices are loaded as the lowest source instead; entries
// exist only once a source names them, so their defaults are filled here,
// after every source has merged. data is the merged tree, keyed in the
// fields' own spelling, and finite, so walking it ends even for a recursive
// type.
//
// A value set to null is explicit and stays null — an entry, or a field on
// the way to a default — so a null that turns an optional part off is not
// turned back on. Write {} for an entry that should take its defaults. The
// collection shapes decoding accepts weakly — a map for a one-element slice,
// a list of maps for one map — are filled the same way.
func fillElementDefaults(t reflect.Type, data any) {
	t = derefType(t)
	v := reflect.ValueOf(data)
	if !v.IsValid() || opaque(t) {
		return
	}
	switch {
	case t.Kind() == reflect.Map && v.Kind() == reflect.Map:
		for _, k := range v.MapKeys() {
			fillElement(t.Elem(), v.MapIndex(k).Interface())
		}
	case t.Kind() == reflect.Map && isList(v):
		for i := range v.Len() {
			fillElementDefaults(t, v.Index(i).Interface())
		}
	case (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) && isList(v):
		for i := range v.Len() {
			fillElement(t.Elem(), v.Index(i).Interface())
		}
	case (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) && v.Kind() == reflect.Map:
		fillElement(t.Elem(), data)
	case t.Kind() == reflect.Struct && v.Kind() == reflect.Map:
		m, ok := data.(map[string]any)
		if !ok {
			return
		}
		for key, ft := range structKeys(t) {
			if child, ok := m[key]; ok {
				fillElementDefaults(ft, child)
			}
		}
	}
}

// fillElement fills one entry of element type elem with elem's defaults,
// in place; an entry that is not a map — null included — is left alone.
func fillElement(elem reflect.Type, entry any) {
	st := derefType(elem)
	m, ok := entry.(map[string]any)
	if !ok || st.Kind() != reflect.Struct || opaque(st) {
		fillElementDefaults(elem, entry)
		return
	}
	for path, def := range defaultsFrom(fieldsOf(st)) {
		setIfAbsent(m, strings.Split(path, "."), def)
	}
	fillElementDefaults(st, m)
}

// setIfAbsent sets the value at key path segs in m when nothing is there,
// creating the maps on the way. Anything already on the way — a value, or
// an explicit null — leaves m as it is.
func setIfAbsent(m map[string]any, segs []string, value any) {
	for _, seg := range segs[:len(segs)-1] {
		next, ok := m[seg]
		if !ok {
			child := map[string]any{}
			m[seg] = child
			m = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return
		}
		m = child
	}
	last := segs[len(segs)-1]
	if _, ok := m[last]; !ok {
		m[last] = value
	}
}
