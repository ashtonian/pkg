package config

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/go-viper/mapstructure/v2"
)

// keyWalk follows one source's tree through the config type the way
// mapstructure decodes it, and rewrites every struct key into the field's
// own spelling. Sources merge key by key, and mapstructure prefers a key
// spelled exactly like the field over one that matches only in letter case;
// without the rewrite, a higher-precedence "Port" would lose to a default
// "port". Keys beneath a map or slice element are followed into the
// element's type; map keys themselves are kept as written.
//
// The walk also records what decoding would ignore — a key no field
// decodes — and a source that sets one field under two spellings.
type keyWalk struct {
	hook    mapstructure.DecodeHookFunc
	unknown []string
	twice   []string
}

// tree rewrites the top level of a source.
func (w *keyWalk) tree(root reflect.Type, data map[string]any) map[string]any {
	out, _ := w.value(root, data, "").(map[string]any)
	return out
}

func (w *keyWalk) value(t reflect.Type, data any, path string) any {
	t = derefType(t)
	if data == nil || t.Kind() == reflect.Interface {
		return data
	}
	if !w.descends(t, data) {
		return data // a decode hook turns it into something without keys
	}
	switch t.Kind() {
	case reflect.Struct:
		if m, ok := stringMap(data); ok {
			return w.structMap(t, m, path)
		}
	case reflect.Map:
		if m, ok := stringMap(data); ok {
			out := make(map[string]any, len(m))
			for k, v := range m {
				out[k] = w.value(t.Elem(), v, path+"["+k+"]")
			}
			return out
		}
		if d, ok := data.([]any); ok { // mapstructure merges a list of maps into one map
			return w.elements(t, d, path)
		}
	case reflect.Slice, reflect.Array:
		switch d := data.(type) {
		case []any:
			return w.elements(t.Elem(), d, path)
		case map[string]any, map[any]any: // weakly typed: a map decodes as a one-element slice
			return w.value(t.Elem(), d, path+"[0]")
		}
	}
	return data
}

func (w *keyWalk) elements(elem reflect.Type, items []any, path string) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = w.value(elem, item, path+"["+strconv.Itoa(i)+"]")
	}
	return out
}

// descends reports whether decoding data into t reads keys from it: the
// decode hooks leave it a map or a slice.
func (w *keyWalk) descends(t reflect.Type, data any) bool {
	converted, err := mapstructure.DecodeHookExec(w.hook, reflect.ValueOf(data), reflect.New(t).Elem())
	if err != nil {
		return false // decoding fails here and says so
	}
	switch reflect.ValueOf(converted).Kind() {
	case reflect.Map, reflect.Slice, reflect.Array:
		return true
	}
	return false
}

// structMap rewrites the keys of a map decoded into struct type t.
func (w *keyWalk) structMap(t reflect.Type, m map[string]any, path string) map[string]any {
	fields, remain := structKeys(t)
	out := make(map[string]any, len(m))
	claimed := make(map[string]string, len(m)) // canonical key → the key written
	for _, k := range slices.Sorted(maps.Keys(m)) {
		key, ft, ok := matchKey(fields, k)
		if !ok {
			out[k] = m[k]
			if !remain {
				w.unknown = append(w.unknown, joinKey(path, k))
			}
			continue
		}
		if prev, dup := claimed[key]; dup {
			w.twice = append(w.twice, fmt.Sprintf("%s (as %s and %s)", joinKey(path, key), prev, k))
			continue
		}
		claimed[key] = k
		out[key] = w.value(ft, m[k], joinKey(path, key))
	}
	return out
}

// structKeys returns the keys struct type t decodes, with their types,
// looking through squashed structs, and whether a koanf:",remain" field
// collects the rest.
func structKeys(t reflect.Type) (map[string]reflect.Type, bool) {
	fields := map[string]reflect.Type{}
	remain := false
	var collect func(t reflect.Type)
	collect = func(t reflect.Type) {
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			_, opts, _ := strings.Cut(f.Tag.Get("koanf"), ",")
			if slices.Contains(strings.Split(opts, ","), "remain") {
				remain = true
				continue
			}
			key, skip := fieldKey(f)
			switch {
			case skip:
			case key == "" && derefType(f.Type).Kind() == reflect.Struct:
				collect(derefType(f.Type))
			case key != "":
				fields[key] = f.Type
			}
		}
	}
	collect(t)
	return fields, remain
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

// stringMap is data as a map with string keys. Parsers may produce
// map[any]any; its keys are turned into strings as koanf does.
func stringMap(data any) (map[string]any, bool) {
	switch d := data.(type) {
	case map[string]any:
		return d, true
	case map[any]any:
		out := make(map[string]any, len(d))
		for k, v := range d {
			out[fmt.Sprint(k)] = v
		}
		return out, true
	}
	return nil, false
}

func joinKey(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
