package config

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// A comparison lists each config's keys in walk order — flatten — and then
// walks the two lists together — compareEntries — so a key's value on one
// side can come from a config the other side never saw: Diff compares two
// configs, and the reloader's appliers compare what each acknowledged with
// what was published.

// entryKind is how one key compares.
type entryKind uint8

const (
	entryLeaf      entryKind = iota // a value decoded whole: compared by value
	entryContainer                  // a struct, map or list: compared by presence; its keys follow it
	entryWhole                      // a sensitive map, list or interface: compared whole, its keys never listed
	entryDynamic                    // an interface: the keys of a struct, map or list it holds follow it
	entryCycle                      // a struct, map or list met again inside itself: compared whole
)

// flatEntry is one key of one config.
type flatEntry struct {
	path      keyPath
	ord       []ordSeg // the walk position, which orders the keys of two configs as one walk
	kind      entryKind
	side      side
	owner     Field
	sensitive bool
	node      *schemaNode // entryDynamic: the schema of the value it holds; nil when it holds none
}

type flattener struct {
	entries  []flatEntry
	visiting map[visitKey]bool
	err      error
}

// visitKey identifies a struct, map or slice on the walk's way down, so one
// that leads back to itself is listed once.
type visitKey struct {
	addr uintptr
	typ  reflect.Type
}

func visitKeyOf(s side) (visitKey, bool) {
	if !s.v.IsValid() || s.isNil {
		return visitKey{}, false
	}
	switch {
	case s.v.Kind() == reflect.Map, s.v.Kind() == reflect.Slice:
		return visitKey{s.v.Pointer(), s.v.Type()}, true
	case s.v.CanAddr():
		return visitKey{s.v.Addr().Pointer(), s.v.Type()}, true
	}
	return visitKey{}, false
}

// flatten lists every key of v — a config's struct, or the invalid value for
// a missing config — in walk order: fields as declared, map entries by
// sorted key, list elements by index, each key before the keys beneath it.
// A value that cannot be compared — a non-nil func, channel or unsafe
// pointer, or an interface holding a type whose keys cannot be told apart —
// is an ErrSchema error, so two lists always compare.
func flatten(root *schemaNode, v reflect.Value) ([]flatEntry, error) {
	f := &flattener{visiting: map[visitKey]bool{}}
	f.children(cursor{}, nil, root, resolve(v))
	return f.entries, f.err
}

// node lists the key at c, of n's type and holding v, then the keys beneath
// it. A key with no value on this side is not listed.
func (f *flattener) node(c cursor, ord []ordSeg, n *schemaNode, v reflect.Value) {
	s := resolve(v)
	if !s.present || f.err != nil {
		return
	}
	e := flatEntry{path: c.path, ord: ord, side: s, owner: c.owner, sensitive: c.sensitive()}
	if s.v.IsValid() && !deepComparable(s.v) {
		f.err = fmt.Errorf("%w: %s holds a %s, which cannot be compared", ErrSchema, c.path.orRoot(), s.v.Type())
		return
	}
	switch {
	case n.kind == nodeLeaf:
		e.kind = entryLeaf
	case c.sensitive() && n.kind != nodeBranch:
		e.kind = entryWhole
	case n.kind == nodeDynamic:
		e.kind = entryDynamic
		if !s.isNil {
			var err error
			if e.node, err = schemaOf(s.v.Type()); err != nil {
				f.err = err
				return
			}
		}
		f.entries = append(f.entries, e)
		if e.node != nil && e.node.composite() {
			// The value's keys sit beneath the interface's own key.
			f.children(c.intoDynamic(), ord, e.node, s)
		}
		return
	default:
		e.kind = entryContainer
		if key, ok := visitKeyOf(s); ok && f.visiting[key] {
			e.kind = entryCycle
			f.entries = append(f.entries, e)
			return
		}
		f.entries = append(f.entries, e)
		f.children(c, ord, n, s)
		return
	}
	f.entries = append(f.entries, e)
}

// children lists the keys beneath s, a struct, map or list of n's type.
func (f *flattener) children(c cursor, ord []ordSeg, n *schemaNode, s side) {
	if !s.present || s.isNil || !s.v.IsValid() {
		return
	}
	if key, ok := visitKeyOf(s); ok {
		f.visiting[key] = true
		defer delete(f.visiting, key)
	}
	switch n.kind {
	case nodeBranch:
		for i, fld := range n.fields {
			v, err := s.v.FieldByIndexErr(fld.index)
			if err != nil {
				continue // behind a nil embedded pointer: no value on this side
			}
			f.node(c.intoField(fld), extendOrd(ord, ordSeg{n: i}), fld.node, v)
		}
	case nodeMap:
		keys := s.v.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
		for _, k := range keys {
			name := k.String()
			f.node(c.intoEntry(segment{kind: segKey, name: name}, n.typ.Elem()), extendOrd(ord, ordSeg{s: name}), n.elem, s.v.MapIndex(k))
		}
	case nodeList:
		for i := range s.v.Len() {
			f.node(c.intoEntry(segment{kind: segIndex, index: i}, n.typ.Elem()), extendOrd(ord, ordSeg{n: i}), n.elem, s.v.Index(i))
		}
	}
}

func extendOrd(ord []ordSeg, s ordSeg) []ordSeg {
	return append(slices.Clip(ord), s)
}

// entryChange is a change as a comparison found it, with its key's path and
// walk position.
type entryChange struct {
	Change
	path keyPath
	ord  []ordSeg
	// subtree reports a key compared whole: the keys beneath it, on either
	// side, are part of this change and have none of their own.
	subtree bool
}

// compareEntries returns every key whose value differs between a and b, two
// entry lists in walk order, in walk order.
func compareEntries(a, b []flatEntry) []entryChange {
	var out []entryChange
	var skip []ordSeg // a key compared whole: the entries beneath it are part of it
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		if skip != nil {
			switch {
			case i < len(a) && within(a[i].ord, skip):
				i++
				continue
			case j < len(b) && within(b[j].ord, skip):
				j++
				continue
			}
			skip = nil
		}
		var ea, eb *flatEntry
		switch {
		case j >= len(b) || i < len(a) && compareOrd(a[i].ord, b[j].ord) < 0:
			ea = &a[i]
			i++
		case i >= len(a) || compareOrd(a[i].ord, b[j].ord) > 0:
			eb = &b[j]
			j++
		default:
			ea, eb = &a[i], &b[j]
			i++
			j++
		}
		c, whole := compareEntry(ea, eb)
		if c != nil {
			out = append(out, *c)
		}
		if whole {
			skip = cmp.Or(ea, eb).ord
		}
	}
	return out
}

// compareEntry compares one key's entries, either of which may be missing.
// whole reports that the keys beneath it are part of the comparison.
func compareEntry(ea, eb *flatEntry) (c *entryChange, whole bool) {
	e := cmp.Or(ea, eb)
	var sa, sb side
	ka, kb := e.kind, e.kind
	if ea != nil {
		sa, ka = ea.side, ea.kind
	}
	if eb != nil {
		sb, kb = eb.side, eb.kind
	}
	switch {
	case ka != kb || ka == entryCycle:
		// A container met again inside itself on either side: compared whole.
		if sameValue(sa, sb) {
			return nil, true
		}
		return e.change(sa.present != sb.present, cycleSide(sa), cycleSide(sb), true), true
	case ka == entryLeaf:
		return e.leafChange(sa, sb), false
	case ka == entryWhole:
		if sameValue(sa, sb) {
			return nil, false
		}
		structural := sa.present != sb.present || sa.isNil != sb.isNil
		return e.change(structural, describe(sa, true), describe(sb, true), true), false
	case ka == entryDynamic:
		return e.dynamicChange(ea, sa, sb)
	}
	if sa.present != sb.present || sa.isNil != sb.isNil {
		return e.change(true, shape(sa), shape(sb), false), false
	}
	return nil, false
}

// dynamicChange compares the values two interfaces hold: one type's keys
// compare one by one, and anything else whole.
func (e *flatEntry) dynamicChange(ea *flatEntry, sa, sb side) (*entryChange, bool) {
	if sa.present && sb.present && !sa.isNil && !sb.isNil && sa.v.Type() == sb.v.Type() {
		if ea.node.kind == nodeLeaf {
			return e.leafChange(sa, sb), false
		}
		return nil, false // its keys follow
	}
	if !composite(sa) && !composite(sb) {
		return e.leafChange(sa, sb), true
	}
	return e.change(true, shape(sa), shape(sb), true), true
}

// leafChange compares two values decoded whole.
func (e *flatEntry) leafChange(sa, sb side) *entryChange {
	if sameValue(sa, sb) {
		return nil
	}
	redact := e.sensitive || uncheckedValue(sa) || uncheckedValue(sb)
	return e.change(false, describe(sa, redact), describe(sb, redact), false)
}

func (e *flatEntry) change(structural bool, old, next Value, subtree bool) *entryChange {
	return &entryChange{
		Change: Change{
			Key:        e.path.String(),
			Structural: structural,
			Field:      cloneField(e.owner),
			Old:        old,
			New:        next,
		},
		path:    e.path,
		ord:     e.ord,
		subtree: subtree,
	}
}

// deepComparable reports a value DeepEqual can compare by content: a func,
// channel or unsafe pointer is compared by identity, which a config cannot
// mean.
func deepComparable(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return v.IsNil()
	}
	return true
}
