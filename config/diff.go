package config

import (
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
	d := &differ{visiting: map[visitPair]bool{}}
	d.node(cursor{}, root, rootValue(old), rootValue(next))
	if d.err != nil {
		return nil, d.err
	}
	return d.changes, nil
}

// rootValue is the struct a config points at, or the invalid value for nil:
// a missing config has no keys.
func rootValue[T any](cfg *T) reflect.Value {
	if cfg == nil {
		return reflect.Value{}
	}
	return reflect.ValueOf(cfg).Elem()
}

type differ struct {
	changes  []Change
	visiting map[visitPair]bool
	err      error
}

// visitPair identifies a pair of containers being compared, for cycles.
type visitPair struct {
	a, b uintptr
	typ  reflect.Type
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

// node compares a and b, two values of n's type, at c.
func (d *differ) node(c cursor, n *schemaNode, a, b reflect.Value) {
	if d.err != nil {
		return
	}
	sa, sb := resolve(a), resolve(b)
	if !sa.present && !sb.present {
		return
	}
	switch {
	case n.kind == nodeLeaf:
		d.leaf(c, sa, sb)
	case c.sensitive() && n.kind != nodeBranch:
		// A map, list or interface whose keys may be secrets: compared
		// whole, never listed.
		d.whole(c, sa, sb)
	case n.kind == nodeDynamic:
		d.dynamic(c, sa, sb)
	default:
		d.composite(c, n, sa, sb)
	}
}

// composite compares two structs, maps or lists.
func (d *differ) composite(c cursor, n *schemaNode, sa, sb side) {
	if len(c.path) > 0 && (sa.present != sb.present || sa.isNil != sb.isNil) {
		d.emit(c, true, d.shape(sa), d.shape(sb))
	}
	if pair, ok := pairOf(sa, sb); ok {
		if d.visiting[pair] {
			d.compareWhole(c, sa, sb)
			return
		}
		d.visiting[pair] = true
		defer delete(d.visiting, pair)
	}
	switch n.kind {
	case nodeBranch:
		for _, f := range n.fields {
			d.node(c.intoField(f), f.node, fieldOf(sa, f), fieldOf(sb, f))
		}
	case nodeMap:
		for _, k := range mapKeys(sa, sb) {
			d.node(c.intoEntry(segment{kind: segKey, name: k}, n.typ.Elem()), n.elem, entryOf(sa, k), entryOf(sb, k))
		}
	case nodeList:
		for i := range max(listLen(sa), listLen(sb)) {
			d.node(c.intoEntry(segment{kind: segIndex, index: i}, n.typ.Elem()), n.elem, elementOf(sa, i), elementOf(sb, i))
		}
	}
}

// dynamic compares the values two interfaces hold.
func (d *differ) dynamic(c cursor, sa, sb side) {
	if sa.present && sb.present && !sa.isNil && !sb.isNil && sa.v.Type() == sb.v.Type() {
		n, err := schemaOf(sa.v.Type())
		if err != nil {
			d.err = err
			return
		}
		d.node(c.intoDynamic(), n, sa.v, sb.v)
		return
	}
	// One side missing, nil, or holding another type: compared whole.
	if !composite(sa) && !composite(sb) {
		d.leaf(c, sa, sb)
		return
	}
	d.emit(c, true, d.shape(sa), d.shape(sb))
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

// leaf compares two values decoded whole.
func (d *differ) leaf(c cursor, sa, sb side) {
	for _, s := range []side{sa, sb} {
		if s.v.IsValid() && !deepComparable(s.v) {
			d.err = fmt.Errorf("%w: %s holds a %s, which cannot be compared", ErrSchema, c.path.orRoot(), s.v.Type())
			return
		}
	}
	if sameValue(sa, sb) {
		return
	}
	redact := c.sensitive() || uncheckedValue(sa) || uncheckedValue(sb)
	d.emit(c, false, d.value(sa, redact), d.value(sb, redact))
}

// whole compares two sensitive containers without listing their keys.
func (d *differ) whole(c cursor, sa, sb side) {
	if sameValue(sa, sb) {
		return
	}
	structural := sa.present != sb.present || sa.isNil != sb.isNil
	d.emit(c, structural, d.value(sa, true), d.value(sb, true))
}

// compareWhole compares two containers met again on a cycle.
func (d *differ) compareWhole(c cursor, sa, sb side) {
	if sameValue(sa, sb) {
		return
	}
	d.emit(c, false, Value{Present: true, Type: sa.typeName(), Text: cycleValue}, Value{Present: true, Type: sb.typeName(), Text: cycleValue})
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
func (d *differ) shape(s side) Value {
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

// value describes a value, its text redacted when redact.
func (d *differ) value(s side, redact bool) Value {
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

func (d *differ) emit(c cursor, structural bool, old, next Value) {
	d.changes = append(d.changes, Change{
		Key:        c.path.String(),
		Structural: structural,
		Field:      cloneField(c.owner),
		Old:        old,
		New:        next,
	})
}

// cloneField copies f so a caller changing its Index cannot reach another
// change's.
func cloneField(f Field) Field {
	f.Index = slices.Clone(f.Index)
	return f
}

// pairOf identifies two map or slice values, or the structs they are, by
// address, for the cycle guard; a pair that cannot lead back to itself has
// none.
func pairOf(a, b side) (visitPair, bool) {
	if !a.v.IsValid() || !b.v.IsValid() || a.isNil || b.isNil {
		return visitPair{}, false
	}
	addr := func(v reflect.Value) (uintptr, bool) {
		switch {
		case v.Kind() == reflect.Map, v.Kind() == reflect.Slice:
			return v.Pointer(), true
		case v.CanAddr():
			return v.Addr().Pointer(), true
		}
		return 0, false
	}
	pa, okA := addr(a.v)
	pb, okB := addr(b.v)
	if !okA || !okB {
		return visitPair{}, false
	}
	return visitPair{a: pa, b: pb, typ: a.v.Type()}, true
}

// fieldOf is f's value in a struct side, or invalid when the side has no
// struct or a pointer on the way is nil.
func fieldOf(s side, f *schemaField) reflect.Value {
	if !s.v.IsValid() || s.isNil {
		return reflect.Value{}
	}
	v, err := s.v.FieldByIndexErr(f.index)
	if err != nil {
		return reflect.Value{}
	}
	return v
}

func mapKeys(a, b side) []string {
	seen := map[string]bool{}
	for _, s := range []side{a, b} {
		if s.v.IsValid() && !s.isNil {
			for _, k := range s.v.MapKeys() {
				seen[k.String()] = true
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, strings.Compare)
	return keys
}

func entryOf(s side, key string) reflect.Value {
	if !s.v.IsValid() || s.isNil {
		return reflect.Value{}
	}
	return s.v.MapIndex(reflect.ValueOf(key).Convert(s.v.Type().Key()))
}

func listLen(s side) int {
	if !s.v.IsValid() || s.isNil {
		return 0
	}
	return s.v.Len()
}

func elementOf(s side, i int) reflect.Value {
	if i >= listLen(s) {
		return reflect.Value{}
	}
	return s.v.Index(i)
}
