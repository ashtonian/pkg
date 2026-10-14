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
func fillElementDefaults(t reflect.Type, data any) {
	t = derefType(t)
	switch t.Kind() {
	case reflect.Map:
		m, ok := data.(map[string]any)
		if !ok {
			return
		}
		for k, v := range m {
			m[k] = fillElement(t.Elem(), v)
		}
	case reflect.Slice, reflect.Array:
		s, ok := data.([]any)
		if !ok {
			return
		}
		for i, v := range s {
			s[i] = fillElement(t.Elem(), v)
		}
	case reflect.Struct:
		m, ok := data.(map[string]any)
		if !ok || !isBranch(t) {
			return
		}
		fields, _ := structKeys(t)
		for key, ft := range fields {
			if v, ok := m[key]; ok {
				fillElementDefaults(ft, v)
			}
		}
	}
}

// fillElement fills one entry of element type elem with elem's defaults and
// returns it; an entry written with no value at all becomes a map of them.
func fillElement(elem reflect.Type, v any) any {
	st := derefType(elem)
	if !isBranch(st) {
		fillElementDefaults(elem, v)
		return v
	}
	if v == nil {
		v = map[string]any{}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	for path, def := range defaultsFrom(fieldsOf(st)) {
		setIfAbsent(m, strings.Split(path, "."), def)
	}
	fillElementDefaults(st, m)
	return m
}

// setIfAbsent sets the value at key path segs in m unless something is
// there already, creating maps along the way; a non-map in the way leaves m
// as it is.
func setIfAbsent(m map[string]any, segs []string, value any) {
	for _, seg := range segs[:len(segs)-1] {
		next, ok := m[seg]
		if !ok || next == nil {
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
