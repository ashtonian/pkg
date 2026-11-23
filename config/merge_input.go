package config

import (
	"maps"
	"reflect"
	"slices"

	kmaps "github.com/knadh/koanf/maps"
)

// Every source's tree is deep-copied as it merges (koanf's maps.Copy). The
// copy keeps a struct only through its exported fields unless it knows the
// type, as it knows time.Time, so a netip.Addr would come out zero. A value
// whose state the copy would lose therefore enters the merge as its
// MarshalText output, and every container the rewrite widens to hold it
// keeps the merge semantics of the typed value it came from.

// A source's tree says two things about each map: its entries, and whether
// it is the source's whole statement of that map. A map[string]any — what a
// file, the environment or a nested provider sets — merges into the map
// beneath key by key. A typed map or section — map[string]Server, a Server
// value, a struct a Defaulter set — arrives as a wholeMap: it replaces what
// the layers beneath set there, as its typed value would. Once merged, every
// map is a map[string]any again, so a higher source sets one key of it as it
// sets one key of any other: mergeTrees is that rule, and the origins, the
// shadowed inputs and key edits follow it.

// wholeMap is a map that replaces the map beneath it rather than merging
// into it by key: a typed map or section of a source, keyed as the merge
// keys it.
type wholeMap map[string]any

var mapStringAnyType = reflect.TypeFor[map[string]any]()

// keepWhole is out, the rewrite of map v: a wholeMap unless v merges by key.
func keepWhole(v, out reflect.Value) any {
	m, ok := out.Interface().(map[string]any)
	switch {
	case !ok:
		return out.Interface()
	case v.Type() == mapStringAnyType:
		return m
	}
	return wholeMap(m)
}

// mergeTrees merges src, a source's rewritten tree, into dest, the merge so
// far: a map[string]any into a map key by key, a listEdit into a list
// element by element, anything else in place of what dest holds.
func mergeTrees(src, dest map[string]any) {
	for k, v := range src {
		dest[k] = mergeValue(dest[k], v)
	}
}

// mergeValue is v merged over dest, which it may change.
func mergeValue(dest, v any) any {
	switch in := v.(type) {
	case map[string]any:
		if into, ok := dest.(map[string]any); ok {
			mergeTrees(in, into)
			return into
		}
	case listEdit:
		return in.applyTo(dest)
	}
	return settled(v)
}

// listEdit sets elements of the list beneath it by index, each merged into
// the element there as mergeValue merges; an index at the list's end
// appends. Only key edits make one, and editTree checks its indexes against
// the list it merges over: those past the end follow one another.
type listEdit map[int]any

// applyTo is the list dest holds with e applied, a fresh list.
func (e listEdit) applyTo(dest any) []any {
	list := elementsOf(dest)
	for _, i := range slices.Sorted(maps.Keys(e)) {
		if i < len(list) {
			list[i] = mergeValue(list[i], e[i])
		} else {
			list = append(list, mergeValue(nil, e[i]))
		}
	}
	return list
}

// settled is v as the merge holds it: every wholeMap within it a
// map[string]any, so a later source merges into it by key, and every
// listEdit the list it makes.
func settled(v any) any {
	switch x := v.(type) {
	case wholeMap:
		return settledMap(x)
	case map[string]any:
		return settledMap(x)
	case listEdit:
		return x.applyTo(nil)
	case []any:
		for i, e := range x {
			x[i] = settled(e)
		}
		return x
	}
	return v
}

// mergedMap is v when the merge holds it as a map whose keys are inputs of
// their own: any map of a source's tree, whole or not.
func mergedMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case wholeMap:
		return m, true
	case map[string]any:
		return m, true
	}
	return nil, false
}

func settledMap(m map[string]any) map[string]any {
	for k, e := range m {
		m[k] = settled(e)
	}
	return m
}

// mergeText is the text v enters the merge as when v, or what pointer v
// points at, is a struct that marshals to text and that the merge's copy
// would not keep whole; ok is false when v can enter as it is.
func mergeText(v reflect.Value) (text string, ok bool, err error) {
	v = derefValue(v)
	if v.Kind() != reflect.Struct || !marshalsText(v.Type()) || copiedWhole(v) {
		return "", false, nil
	}
	m, _ := textMarshaler(v)
	b, err := m.MarshalText()
	return string(b), true, err
}

// copiedWhole reports whether the merge's copy keeps v whole.
func copiedWhole(v reflect.Value) bool {
	in := v.Interface()
	return reflect.DeepEqual(kmaps.Copy(map[string]any{"v": in})["v"], in)
}

// dynamicInput is data, held by a field of interface type at path, as the
// merge carries it: data itself, except that each struct whose state the
// merge's copy would lose, there or inside a map, list or pointer, is its
// text. failed is called with the path of each whose MarshalText fails.
func dynamicInput(data any, path keyPath, failed func(keyPath)) any {
	out, _ := dynamicValue(reflect.ValueOf(data), path, failed)
	return out
}

// dynamicValue is v as dynamicInput carries it; changed reports a
// replacement within it.
func dynamicValue(v reflect.Value, path keyPath, failed func(keyPath)) (out any, changed bool) {
	if !v.IsValid() {
		return nil, false
	}
	if !mayHoldText(v.Type()) {
		return v.Interface(), false
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return v.Interface(), false
		}
		return dynamicValue(v.Elem(), path, failed)
	case reflect.Pointer, reflect.Struct:
		text, ok, err := mergeText(v)
		switch {
		case !ok:
			return v.Interface(), false
		case err != nil:
			failed(path)
			return v.Interface(), false
		}
		return text, true
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return v.Interface(), false
		}
		var list []any
		for i := range v.Len() {
			e, ok := dynamicValue(v.Index(i), path.index(i), failed)
			if ok && list == nil {
				list = make([]any, v.Len())
				for j := range i {
					list[j] = v.Index(j).Interface()
				}
			}
			if list != nil {
				list[i] = e
			}
		}
		if list == nil {
			return v.Interface(), false
		}
		return list, true
	case reflect.Map:
		var m reflect.Value
		for it := v.MapRange(); it.Next(); {
			e, ok := dynamicValue(it.Value(), path.key(entryName(it.Key())), failed)
			if ok && !m.IsValid() {
				m = reflect.MakeMapWithSize(reflect.MapOf(v.Type().Key(), reflect.TypeFor[any]()), v.Len())
				for prev := v.MapRange(); prev.Next(); {
					m.SetMapIndex(prev.Key(), prev.Value())
				}
			}
			if m.IsValid() {
				m.SetMapIndex(it.Key(), reflect.ValueOf(&e).Elem())
			}
		}
		if !m.IsValid() {
			return v.Interface(), false
		}
		return keepWhole(v, m), true
	}
	return v.Interface(), false
}

// mayHoldText reports whether a value of t can hold a struct: anything else
// passes through the copy whole.
func mayHoldText(t reflect.Type) bool {
	for seen := map[reflect.Type]bool{}; !seen[t]; t = t.Elem() {
		seen[t] = true
		switch t.Kind() {
		case reflect.Interface, reflect.Struct:
			return true
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		default:
			return false
		}
	}
	return false // a type built of itself, such as type T []T, holds no struct
}
