package config

import (
	"fmt"
	"reflect"
	"strconv"

	"github.com/ashtonian/pkg/config/internal/deepcopy"
)

// A typed section a source sets — a Server from WithDefaults or a provider —
// enters the merge as the map of its keys (see keyWalk.section), so its
// exported fields tagged koanf:"-", which no key reaches, have no place in
// any tree. The load carries them beside the merge instead: each source's
// walk records the sections it sets whole, the load keeps for each section
// the record of the highest source that set it whole, and the decoded
// config gets the kept records' fields. A higher source that replaces the
// section, or anything holding it, drops the record beneath it, as it drops
// the section's keys; one that sets keys inside the section keeps it.

// unkeyedFields is the koanf:"-" fields of one typed section a source set:
// the section as the source holds it, and its node's unkeyed fields.
type unkeyedFields struct {
	path    keyPath
	section reflect.Value // past pointers; read only, copied when applied
	fields  [][]int
}

// unkeyedSections is, by key path, the unkeyedFields a load keeps: each
// section's highest source's, while no higher source replaced it.
type unkeyedSections map[string]unkeyedFields

// merge updates s for one source: tree, its tree as it merges, and set, the
// sections it sets whole.
func (s *unkeyedSections) merge(tree map[string]any, set []unkeyedFields) {
	for k, u := range *s {
		if replaces(tree, u.path) {
			delete(*s, k)
		}
	}
	if len(set) == 0 {
		return
	}
	if *s == nil {
		*s = make(unkeyedSections, len(set))
	}
	for _, u := range set {
		(*s)[u.path.String()] = u
	}
}

// replaces reports whether tree, merged by mergeTrees, replaces the value at
// p rather than leaving it or merging into it. The merge so far holds a map
// at each of p's field and key segments and a list at each index segment —
// a source that replaced one dropped the record — so tree replaces it when
// it holds anything on p but a map[string]any there, or a listEdit before an
// index.
func replaces(tree map[string]any, p keyPath) bool {
	var at any = tree
	for _, seg := range p {
		var (
			next any
			ok   bool
		)
		switch seg.kind {
		case segField, segKey:
			m, merges := at.(map[string]any)
			if !merges {
				return true
			}
			next, ok = m[seg.name]
		case segIndex:
			e, edits := at.(listEdit)
			if !edits {
				return true
			}
			next, ok = e[seg.index]
		default:
			return true
		}
		if !ok {
			return false
		}
		at = next
	}
	_, merges := at.(map[string]any)
	return !merges
}

// apply sets each kept section's unkeyed fields on cfg, the decoded config
// of root's type, addressable.
func (s unkeyedSections) apply(root *schemaNode, cfg reflect.Value) {
	if len(s) == 0 {
		return
	}
	keys := mapKeyIndex{}
	for _, u := range s {
		u.setAt(cfg, root, u.path, keys)
	}
}

// setAt sets u's fields on the section at p beneath v, a value of node n. A
// map entry is not addressable, so it is copied, set and stored back. A
// path the decoded config does not reach is left alone: nothing decoded
// there for the section to belong to.
func (u unkeyedFields) setAt(v reflect.Value, n *schemaNode, p keyPath, keys mapKeyIndex) {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	if len(p) == 0 {
		if v.Type() == u.section.Type() {
			u.setOn(v)
		}
		return
	}
	switch seg := p[0]; {
	case seg.kind == segField && n.kind == nodeBranch:
		f := n.byKey[seg.name]
		if f == nil {
			return
		}
		fv, err := v.FieldByIndexErr(f.index)
		if err != nil {
			return
		}
		u.setAt(fv, f.node, p[1:], keys)
	case seg.kind == segKey && v.Kind() == reflect.Map && n.elem != nil:
		k, ok := keys.lookup(v, seg.name)
		if !ok {
			return
		}
		e := reflect.New(v.Type().Elem()).Elem()
		e.Set(v.MapIndex(k))
		u.setAt(e, n.elem, p[1:], keys)
		v.SetMapIndex(k, e)
	case seg.kind == segIndex && (v.Kind() == reflect.Slice || v.Kind() == reflect.Array) && n.elem != nil:
		if seg.index < v.Len() {
			u.setAt(v.Index(seg.index), n.elem, p[1:], keys)
		}
	}
}

// setOn sets each of u's fields on section v, addressable, to a copy of the
// source's: what a field reached through a nil embedded pointer holds is
// its zero value.
func (u unkeyedFields) setOn(v reflect.Value) {
	for _, index := range u.fields {
		from, err := u.section.FieldByIndexErr(index)
		if err != nil {
			from = reflect.Zero(u.section.Type().FieldByIndex(index).Type)
		}
		to, ok := fieldForSet(v, index, !from.IsZero())
		if ok {
			to.Set(deepcopy.Value(from))
		}
	}
}

// fieldForSet is the field of struct v at index, allocating the embedded
// pointers on the way when alloc is set; ok is false when a nil one stands
// in the way and alloc is not set, or it cannot be allocated.
func fieldForSet(v reflect.Value, index []int, alloc bool) (reflect.Value, bool) {
	for i, x := range index {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				if !alloc || !v.CanSet() {
					return reflect.Value{}, false
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	return v, v.CanSet()
}

// mapKeyIndex finds a decoded map's key by the text a source tree keyed
// it by. A key type keysAsText accepts converts from its text; any other map
// is indexed once by each key's fmt.Sprint text, as keyWalk.mapValues keys
// its paths.
type mapKeyIndex map[uintptr]map[string]reflect.Value

func (idx mapKeyIndex) lookup(m reflect.Value, text string) (reflect.Value, bool) {
	kt := m.Type().Key()
	if keysAsText(kt) {
		k, err := keyFromText(kt, text)
		if err != nil || !m.MapIndex(k).IsValid() {
			return reflect.Value{}, false
		}
		return k, true
	}
	byText, ok := idx[m.Pointer()]
	if !ok {
		byText = make(map[string]reflect.Value, m.Len())
		for _, k := range m.MapKeys() {
			byText[fmt.Sprint(k.Interface())] = k
		}
		idx[m.Pointer()] = byText
	}
	k, ok := byText[text]
	return k, ok
}

// keyFromText is the key of type t, a kind keysAsText accepts, that keyText
// writes as text.
func keyFromText(t reflect.Type, text string) (reflect.Value, error) {
	k := reflect.New(t).Elem()
	switch t.Kind() {
	case reflect.String:
		k.SetString(text)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(text, 10, t.Bits())
		if err != nil {
			return reflect.Value{}, err
		}
		k.SetInt(n)
	default:
		n, err := strconv.ParseUint(text, 10, t.Bits())
		if err != nil {
			return reflect.Value{}, err
		}
		k.SetUint(n)
	}
	return k, nil
}
