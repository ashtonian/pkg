package config

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Value is one side of a [Change]: whether a value is there, and its text
// when it may be shown.
type Value struct {
	// Present is false when the key does not exist on this side: an entry
	// not in the map, an element past the list's end, a field beneath a nil
	// struct pointer.
	Present bool `json:"present"`
	// Nil reports a nil pointer, map, slice or interface.
	Nil bool `json:"nil,omitempty"`
	// Type is the value's Go type: the dynamic type for an interface.
	Type string `json:"type,omitempty"`
	// Text is the value's %v text. It is empty for a structural change, and
	// "[REDACTED]" when Redacted.
	Text string `json:"text,omitempty"`
	// Redacted reports a value PrintRedacted would redact.
	Redacted bool `json:"redacted,omitempty"`
}

// Change is one key whose value differs between two configs.
type Change struct {
	// Key is the runtime key (db.host, items[0].port, named["primary"].port).
	Key string `json:"key"`
	// Structural reports a change to a struct, map, list or interface itself
	// rather than to a value inside it: it appeared, disappeared, became nil
	// or non-nil, or an interface's value changed type. Old and New then
	// describe presence and type only; the keys beneath it that changed are
	// changes of their own.
	Structural bool `json:"structural,omitempty"`
	// Field is the declared field Key belongs to, as [FieldFor] reports it.
	// Its Tag is metadata for code, not for output.
	Field Field `json:"-"`
	Old   Value `json:"old"`
	New   Value `json:"new"`
}

// Diff compares two configs of type T, a struct, and returns every key whose
// value differs, in walk order: fields as declared, map entries by sorted
// key, list elements by index. A nil config has no keys. It compares decoded
// values, never their text, and neither input is changed.
//
//   - Lists compare by index: removing the first of three elements changes
//     the first two and removes the third. Map entries compare by key; an
//     added or removed entry is a change of every key beneath it.
//   - A struct, map, list or interface that appears, disappears or turns
//     nil, a nil collection becoming empty, or an interface's value changing
//     type is a Structural change at its key. An interface whose value
//     changes type is compared whole.
//   - Values decoded whole — scalars, self-decoding structs, maps keyed by
//     non-strings — compare with reflect.DeepEqual: no Equal or String
//     method decides, so two time.Time values for one instant in different
//     locations differ, and NaN never equals itself.
//   - A value PrintRedacted would redact is redacted on both sides; a
//     sensitive map, list or interface is compared whole, so the keys it
//     holds, which may themselves be secrets, are never listed.
//   - A pointer, map or slice that leads back to one being compared is
//     compared whole there, so a cycle ends.
//
// A T that is not a struct, a struct whose keys cannot be told apart, and a
// non-nil func, channel or unsafe pointer anywhere in either config are
// ErrSchema errors.
func Diff[T any](old, next *T) ([]Change, error) {
	root, err := rootSchema(reflect.TypeFor[T]())
	if err != nil {
		return nil, err
	}
	a, err := flatten(root, rootValue(old))
	if err != nil {
		return nil, err
	}
	b, err := flatten(root, rootValue(next))
	if err != nil {
		return nil, err
	}
	var changes []Change
	for _, c := range compareEntries(a, b) {
		changes = append(changes, c.Change)
	}
	return changes, nil
}

// rootValue is the struct a config points at, or the invalid value for nil:
// a missing config has no keys.
func rootValue[T any](cfg *T) reflect.Value {
	if cfg == nil {
		return reflect.Value{}
	}
	return reflect.ValueOf(cfg).Elem()
}

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

// ordSeg is one step of a walk position: a field's declaration position, an
// element's index, or an entry's key.
type ordSeg struct {
	n int
	s string
}

func compareOrd(a, b []ordSeg) int {
	for i := range min(len(a), len(b)) {
		if c := cmp.Compare(a[i].n, b[i].n); c != 0 {
			return c
		}
		if c := strings.Compare(a[i].s, b[i].s); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}

// within reports whether position ord is at or beneath prefix.
func within(ord, prefix []ordSeg) bool {
	return len(ord) >= len(prefix) && slices.Equal(ord[:len(prefix)], prefix)
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

// composite reports a side holding a struct, map or list.
func composite(s side) bool {
	if !s.v.IsValid() {
		return false
	}
	switch s.v.Kind() {
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array:
		return true
	}
	return false
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

// side is a value as found: present or absent, and when present, nil or not.
type side struct {
	v       reflect.Value // the dereferenced value; invalid when absent or nil
	present bool
	isNil   bool
}

// resolve dereferences v, a value of some declared type, through pointers and
// interfaces.
func resolve(v reflect.Value) side {
	if !v.IsValid() {
		return side{}
	}
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return side{present: true, isNil: true}
		}
		v = v.Elem()
	}
	if (v.Kind() == reflect.Map || v.Kind() == reflect.Slice) && v.IsNil() {
		return side{v: v, present: true, isNil: true}
	}
	return side{v: v, present: true}
}

func (s side) typeName() string {
	if s.v.IsValid() {
		return s.v.Type().String()
	}
	return ""
}

func sameValue(a, b side) bool {
	if a.present != b.present || a.isNil != b.isNil {
		return false
	}
	if !a.v.IsValid() || !b.v.IsValid() {
		return a.v.IsValid() == b.v.IsValid()
	}
	return a.v.Type() == b.v.Type() && reflect.DeepEqual(a.v.Interface(), b.v.Interface())
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

// uncheckedValue reports a side whose text would show what the walk did not
// inspect: see uncheckedHolder.
func uncheckedValue(s side) bool {
	return s.v.IsValid() && !s.isNil && uncheckedHolder(s.v)
}

// shape describes a struct, map, list or interface by presence and type.
func shape(s side) Value {
	v := Value{Present: s.present, Nil: s.isNil, Type: s.typeName()}
	if s.present && !s.isNil && s.v.IsValid() {
		switch s.v.Kind() {
		case reflect.Map:
			if s.v.Len() == 0 {
				v.Text = "{}"
			}
		case reflect.Slice, reflect.Array:
			if s.v.Len() == 0 {
				v.Text = "[]"
			}
		}
	}
	return v
}

// cycleSide describes a container compared whole because it leads back to
// itself.
func cycleSide(s side) Value {
	if !s.present {
		return Value{}
	}
	return Value{Present: true, Nil: s.isNil, Type: s.typeName(), Text: cycleValue}
}

// describe describes a value, its text redacted when redact.
func describe(s side, redact bool) Value {
	v := Value{Present: s.present, Nil: s.isNil, Type: s.typeName()}
	switch {
	case !s.present, s.isNil && !s.v.IsValid():
	case redact:
		v.Text, v.Redacted = redactedValue, true
	case s.isNil:
	default:
		v.Text = fmt.Sprintf("%v", s.v.Interface())
	}
	return v
}

// cloneField copies f so a caller changing its Index cannot reach another
// change's.
func cloneField(f Field) Field {
	f.Index = slices.Clone(f.Index)
	return f
}
