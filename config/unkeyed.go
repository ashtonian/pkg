package config

import (
	"maps"
	"reflect"

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

// unkeyedFields is the koanf:"-" fields of one typed section a source set,
// copied as the source is read: the load reads a source's data only while
// it reads the source, so data the source changes afterwards, or shares
// with another goroutine, never reaches the config.
type unkeyedFields struct {
	path    keyPath
	section reflect.Type // past pointers
	fields  []unkeyedField
	// whole is the length of path's prefix naming the outermost map the
	// merge holds with its own keys (see keysAsText), which any higher
	// source setting anything there replaces whole; 0 for none.
	whole int
}

// unkeyedField is one koanf:"-" field of a section: its index and a copy of
// the value the source's section holds there, the zero value where a nil
// embedded pointer stands in the way.
type unkeyedField struct {
	index []int
	value reflect.Value
}

// recordUnkeyed records the fields at indexes of section, a struct value
// past its pointers, at path.
func recordUnkeyed(path keyPath, section reflect.Value, indexes [][]int) unkeyedFields {
	u := unkeyedFields{path: path, section: section.Type(), fields: make([]unkeyedField, 0, len(indexes))}
	for _, index := range indexes {
		from, err := section.FieldByIndexErr(index)
		if err != nil {
			from = reflect.Zero(section.Type().FieldByIndex(index).Type)
		}
		u.fields = append(u.fields, unkeyedField{index: index, value: deepcopy.Value(from)})
	}
	return u
}

// unkeyedSections is, by key path, the unkeyedFields a load keeps: each
// section's highest source's, while no higher source replaced it.
type unkeyedSections map[string]unkeyedFields

// merge updates s for one source: tree, its tree as it merges, and set, the
// sections its walk recorded. A section a filter withheld from the tree
// since (WithInvalidEntries) is not kept.
func (s *unkeyedSections) merge(tree map[string]any, set []unkeyedFields) {
	for k, u := range *s {
		if replaces(tree, u) {
			delete(*s, k)
		}
	}
	for _, u := range set {
		var placed bool
		if placed, u.whole = holdsMap(tree, u.path); !placed {
			continue
		}
		if *s == nil {
			*s = unkeyedSections{}
		}
		(*s)[u.path.String()] = u
	}
}

// over is s, what the layers beneath hold, with one source merged over it as
// merge merges one, as a new set: s is unchanged, so one set of defaults
// serves every candidate a key check decodes.
func (s unkeyedSections) over(tree map[string]any, set []unkeyedFields) unkeyedSections {
	out := maps.Clone(s)
	out.merge(tree, set)
	return out
}

// holdsMap reports whether tree, a source's tree, holds a map at p, and
// whole, the length of p's prefix naming the outermost map on the way that
// the tree holds with its own keys, or 0.
func holdsMap(tree map[string]any, p keyPath) (ok bool, whole int) {
	var at any = tree
	for i, seg := range p {
		switch seg.kind {
		case segField, segKey:
			if _, merged := mergedMap(at); !merged && whole == 0 {
				whole = i
			}
			at, ok = mapEntry(at, seg.name)
		case segIndex:
			switch list := at.(type) {
			case []any:
				if ok = seg.index < len(list); ok {
					at = list[seg.index]
				}
			case listEdit:
				at, ok = list[seg.index]
			}
		}
		if !ok {
			return false, 0
		}
	}
	_, ok = mergedMap(at)
	return ok, whole
}

// replaces reports whether tree, merged by mergeTrees, replaces u's section
// rather than leaving it or merging into it. The merge so far holds a map
// at each of the path's field and key segments and a list at each index
// segment — a source that replaced one dropped the record — so tree
// replaces it when it holds anything on the path but a map[string]any
// there, a listEdit before an index, or anything at all where the merge
// holds a map with its own keys (u.whole).
func replaces(tree map[string]any, u unkeyedFields) bool {
	var at any = tree
	for i, seg := range u.path {
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
		if i+1 == u.whole {
			return true
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
	for _, u := range s {
		u.setAt(cfg, root, u.path)
	}
}

// setAt sets u's fields on the section at p beneath v, a value of node n. A
// map entry is not addressable, so it is copied, set and stored back. A
// path the decoded config does not reach is left alone: nothing decoded
// there for the section to belong to.
func (u unkeyedFields) setAt(v reflect.Value, n *schemaNode, p keyPath) {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	if len(p) == 0 {
		if v.Type() == u.section {
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
		u.setAt(fv, f.node, p[1:])
	case seg.kind == segKey && n.kind == nodeMap && v.Kind() == reflect.Map:
		k, ok := entryKey(v, seg.name)
		if !ok {
			return
		}
		e := reflect.New(v.Type().Elem()).Elem()
		e.Set(v.MapIndex(k))
		u.setAt(e, n.elem, p[1:])
		v.SetMapIndex(k, e)
	case seg.kind == segIndex && (v.Kind() == reflect.Slice || v.Kind() == reflect.Array) && n.elem != nil:
		if seg.index < v.Len() {
			u.setAt(v.Index(seg.index), n.elem, p[1:])
		}
	}
}

// setOn sets each of u's fields on section v, addressable, to a copy of the
// recorded value: one record serves every candidate a key check decodes,
// and none shares memory with another or with the record.
func (u unkeyedFields) setOn(v reflect.Value) {
	for _, f := range u.fields {
		to, ok := fieldForSet(v, f.index, !f.value.IsZero())
		if ok {
			to.Set(deepcopy.Value(f.value))
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
