package config

import (
	"fmt"
	"reflect"

	kmaps "github.com/knadh/koanf/maps"
)

// Every source's tree is deep-copied as it merges (koanf's maps.Copy). The
// copy keeps a struct only through its exported fields unless it knows the
// type, as it knows time.Time, so a netip.Addr would come out zero. A value
// whose state the copy would lose therefore enters the merge as its
// MarshalText output, and every container the rewrite widens to hold it
// keeps the merge semantics of the typed value it came from.

// wholeMap is a map koanf merges whole rather than by key, as it does any
// typed map: it sees through map[string]any only, so an entry this map
// lacks does not survive from a layer beneath.
type wholeMap map[string]any

var mapStringAnyType = reflect.TypeFor[map[string]any]()

// keepWhole is out, the rewrite of map v. A typed map is one value to koanf,
// replacing what a layer beneath holds; widened to map[string]any it would
// merge by key instead, so it is a wholeMap.
func keepWhole(v, out reflect.Value) any {
	if m, ok := out.Interface().(map[string]any); ok && v.Type() != mapStringAnyType {
		return wholeMap(m)
	}
	return out.Interface()
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
			e, ok := dynamicValue(it.Value(), path.key(fmt.Sprint(it.Key().Interface())), failed)
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
