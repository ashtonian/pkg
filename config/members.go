package config

import (
	"iter"
	"reflect"
	"slices"
)

// A branch's keys are its members: its declared fields, then the keys its
// koanf:",remain" field collects, each directly beneath the branch
// (plugins.foo) and owned by the remain field. Every walk steps beneath a
// branch through its members, so each sees the same keys, owned and made
// sensitive the same way. A sensitive remain field is one member,
// section[*], compared and shown whole: the keys it collects may
// themselves be secrets, as a sensitive map's may.

type memberKind uint8

const (
	memberField     memberKind = iota // a declared field
	memberCollected                   // one key the remain field collects
	memberRemain                      // every key the remain field collects, as one value
)

// member is one key directly beneath a branch.
type member struct {
	kind   memberKind
	seg    segment
	ord    ordSeg       // its walk position; set by members alone
	field  *schemaField // memberField
	branch *schemaNode  // the branch whose remain field holds it: memberCollected, memberRemain
}

// remainSegment names a sensitive remain field's member: [*], the pattern
// of every key it collects.
var remainSegment = segment{kind: segAny}

// memberAt is the member of n, a branch, that s names in a key Diff
// reports: a field by its key, a key the remain field collects, or [*],
// which a key holds only for a remain field compared whole.
func (n *schemaNode) memberAt(s segment) (member, bool) {
	if s.kind == segField {
		if f := n.byKey[s.name]; f != nil {
			return member{kind: memberField, seg: s, field: f}, true
		}
	}
	switch {
	case n.collects(s):
		return member{kind: memberCollected, seg: memberSegment(s.name), branch: n}, true
	case s.kind == segAny && n.remain != nil:
		return member{kind: memberRemain, seg: remainSegment, branch: n}, true
	}
	return member{}, false
}

// remainMember is the remain field of n, a branch whose keys are sensitive
// or not, as a pattern: the field whole when it is one value
// (remainField.whole), otherwise any one key it collects.
func (n *schemaNode) remainMember(sensitive bool) member {
	m := member{kind: memberRemain, seg: remainSegment, branch: n}
	if !n.remainWhole(sensitive) {
		m.kind = memberCollected
	}
	return m
}

// remainWhole reports whether n, a branch whose keys are sensitive or not,
// has a remain field that is one value (remainField.whole) to a walk that
// withholds sensitive values.
func (n *schemaNode) remainWhole(sensitive bool) bool {
	return n.remain != nil && n.remain.whole(sensitive)
}

// members yields each member of n, a branch, that a or b, two of its
// values, holds, with its value on each side, in walk order: the fields as
// declared, then the keys the remain field collects by name. With whole —
// the remain field is one value, see remainWhole — the remain field is one
// member instead, there while it collects a key on either side; a walk that
// withholds nothing, as a merge, passes false.
func (n *schemaNode) members(a, b side, whole bool) iter.Seq2[member, [2]reflect.Value] {
	return func(yield func(member, [2]reflect.Value) bool) {
		for i, f := range n.fields {
			m := member{kind: memberField, seg: segment{kind: segField, name: f.key}, ord: ordSeg{n: i}, field: f}
			if !yield(m, [2]reflect.Value{fieldOf(a, f), fieldOf(b, f)}) {
				return
			}
		}
		if n.remain == nil {
			return
		}
		ord := len(n.fields)
		if whole {
			vals := [2]reflect.Value{n.collectedMap(a), n.collectedMap(b)}
			if vals[0].IsValid() || vals[1].IsValid() {
				yield(member{kind: memberRemain, seg: remainSegment, ord: ordSeg{n: ord}, branch: n}, vals)
			}
			return
		}
		for name, vals := range joinEntries(n.collected(a), n.collected(b)) {
			m := member{kind: memberCollected, seg: memberSegment(name), ord: ordSeg{n: ord, s: name}, branch: n}
			if !yield(m, vals) {
				return
			}
		}
	}
}

// collected is what the remain field of n, a branch, holds in s, a value
// of n's type: its entries no field reads, in key order. A key a field
// reads is never the remain field's: decoding gives it to the field.
func (n *schemaNode) collected(s side) mapRead {
	held := resolve(n.remainIn(s))
	if !held.v.IsValid() {
		return mapRead{}
	}
	r := readMap(n.remain.keys, held)
	r.keys = slices.DeleteFunc(r.keys, func(k mapKey) bool { return !n.collects(memberSegment(k.name)) })
	return r
}

// remainIn is the remain field of n, a branch, in s, a value of n's type:
// invalid when s holds no struct or a nil embedded pointer stands in the
// way.
func (n *schemaNode) remainIn(s side) reflect.Value {
	if !s.v.IsValid() || s.isNil {
		return reflect.Value{}
	}
	v, err := s.v.FieldByIndexErr(n.remain.index)
	if err != nil {
		return reflect.Value{}
	}
	return v
}

// collectedMap is what the remain field of n, a branch, collects in s, a
// value of n's type, as one map of the field's type, or invalid for none:
// a remain field compared whole holds a value only while it collects a key,
// so an empty one and a nil one are alike, and an entry a field reads is
// not part of it.
func (n *schemaNode) collectedMap(s side) reflect.Value {
	held := resolve(n.remainIn(s))
	if !held.v.IsValid() || held.isNil || held.v.Len() == 0 {
		return reflect.Value{}
	}
	out := reflect.MakeMapWithSize(held.v.Type(), held.v.Len())
	for it := held.v.MapRange(); it.Next(); {
		if n.collects(memberSegment(entryText(n.remain.keys, it.Key()))) {
			out.SetMapIndex(it.Key(), it.Value())
		}
	}
	if out.Len() == 0 {
		return reflect.Value{}
	}
	return out
}

// joinEntries yields the entries of a and b, two maps read in key order, by
// name, with the value each holds: invalid on a side without the entry.
func joinEntries(a, b mapRead) iter.Seq2[string, [2]reflect.Value] {
	return func(yield func(string, [2]reflect.Value) bool) {
		i, j := 0, 0
		for i < len(a.keys) || j < len(b.keys) {
			var name string
			var vals [2]reflect.Value
			switch {
			case j >= len(b.keys) || i < len(a.keys) && a.keys[i].name < b.keys[j].name:
				name, vals[0] = a.keys[i].name, a.at(i)
				i++
			case i >= len(a.keys) || a.keys[i].name > b.keys[j].name:
				name, vals[1] = b.keys[j].name, b.at(j)
				j++
			default:
				name, vals[0], vals[1] = a.keys[i].name, a.at(i), b.at(j)
				i++
				j++
			}
			if !yield(name, vals) {
				return
			}
		}
	}
}

// in is m's value in s, a value of its branch's type: invalid when s holds
// none.
func (m member) in(s side) reflect.Value {
	switch m.kind {
	case memberField:
		return fieldOf(s, m.field)
	case memberCollected:
		return entryOfSide(m.branch.remain.keys, resolve(m.branch.remainIn(s)), m.seg.name)
	}
	return m.branch.collectedMap(s)
}

// slot is where m's value is set in v, an addressable value of its
// branch's type: the field, or the remain field's map with the key of m's
// entry in it, since a map entry is not addressable. ok is false when a nil
// embedded pointer stands in the way.
func (m member) slot(v reflect.Value) (slot reflect.Value, via keyPath, ok bool) {
	if m.kind == memberField {
		fv, err := m.field.value(v)
		return fv, nil, err == nil
	}
	rv, err := v.FieldByIndexErr(m.branch.remain.index)
	if err != nil {
		return reflect.Value{}, nil, false
	}
	if m.kind == memberCollected {
		via = keyPath{{kind: segKey, name: m.seg.name}}
	}
	return rv, via, true
}

// declared is m's declared Go type: its field's, the remain field's
// element type, or for the remain field whole its map type.
func (m member) declared() reflect.Type {
	switch m.kind {
	case memberField:
		return m.field.sf.Type
	case memberCollected:
		return m.branch.remain.sf.Type.Elem()
	}
	return m.branch.remain.sf.Type
}

// node is the node of m's declared type, read through tier for a key the
// remain field holds (see remainField.node).
func (m member) node(tier func(reflect.Type) (*schemaNode, error)) (*schemaNode, error) {
	if m.kind == memberField {
		return m.field.node, nil
	}
	n, err := m.branch.remain.node(tier)
	if err != nil || m.kind == memberRemain {
		return n, err
	}
	return n.elem, nil
}

// slotNode is the node of slot's value: m's own, or the remain field's map.
func (m member) slotNode(tier func(reflect.Type) (*schemaNode, error)) (*schemaNode, error) {
	if m.kind == memberField {
		return m.field.node, nil
	}
	return m.branch.remain.node(tier)
}

// frame is f, the frame at m's branch, stepped into m.
func (m member) frame(f frame) frame {
	if m.kind == memberField {
		return f.field(m.field)
	}
	r := m.branch.remain
	f.tagged = f.tagged || r.tagged
	f.covered = f.covered || r.named
	if m.seg.kind == segAny {
		return f
	}
	return f.entry(m.seg.name, r.sf.Type.Elem())
}

// site is s, where m's branch sits, stepped into m: what a printed entry
// inherits, as frame is for a comparison. A key the remain field collects
// sits as a map entry does, named like a credential or not by its key.
func (m member) site(s site) site {
	if m.kind == memberField {
		return site{
			named:            m.field.named,
			desc:             m.field.sf.Tag.Get("desc"),
			sensitive:        s.sensitive || m.field.tagged,
			beneathInterface: s.beneathInterface,
		}
	}
	r := m.branch.remain
	next := site{
		desc:             r.sf.Tag.Get("desc"),
		sensitive:        s.sensitive || r.tagged || r.named,
		beneathInterface: s.beneathInterface,
	}
	if m.seg.kind != segAny {
		next.named = isSensitiveName(m.seg.name) && textCapable(r.sf.Type.Elem())
		next.keyed = true
	}
	return next
}

// cursor is c, the cursor at m's branch, stepped into m. A remain member
// named [*] — the field whole, or the pattern of any key it collects — is
// owned by the remain field and spelled section[*].
func (m member) cursor(c cursor) cursor {
	if m.kind == memberField {
		return c.intoField(m.field)
	}
	r := m.branch.remain
	if m.seg.kind == segAny {
		next := c.intoRemain(r)
		next.path = c.path.any()
		return next
	}
	return c.intoRemain(r).intoCollected(m.seg.name, r.sf.Type.Elem())
}
