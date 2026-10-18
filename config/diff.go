package config

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
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
	// Type is the value's Go type, with struct tags omitted: the dynamic
	// type for an interface.
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
	// or non-nil, or an interface's value changed to or from a struct, map or
	// list of another type. Old and New then describe presence and type
	// only; the keys beneath it that changed are changes of their own.
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
//     nil, or a nil collection becoming empty, is a Structural change at its
//     key, followed by the keys beneath it that changed. An interface whose
//     value changes type, or turns nil, is compared whole: one change at its
//     key, Structural when either value is a struct, map or list.
//   - Values decoded whole — scalars, self-decoding structs, maps keyed by
//     non-strings — compare with reflect.DeepEqual: no Equal or String
//     method decides, so two time.Time values for one instant in different
//     locations differ, and NaN never equals itself.
//   - A value PrintRedacted would redact is redacted on both sides; a
//     sensitive map, list or interface is compared whole, so the keys it
//     holds, which may themselves be secrets, are never listed.
//   - A pointer, map or slice that leads back to one it lies within, on
//     either side, is compared whole there, so a cycle ends.
//
// A T that is not a struct, a struct whose keys cannot be told apart, an
// interface holding such a struct, and a non-nil func, channel or unsafe
// pointer anywhere in either config — inside a value compared whole
// included — are ErrSchema errors.
func Diff[T any](old, next *T) ([]Change, error) {
	root, err := rootSchema(reflect.TypeFor[T]())
	if err != nil {
		return nil, err
	}
	var changes []Change
	err = compare(root, rootValue(old), rootValue(next), func(d *delta) {
		changes = append(changes, d.change(root))
	})
	if err != nil {
		return nil, err
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

// compare walks a and b, two values of root's type, side by side and calls
// emit for every key whose value differs, in walk order. Nothing is
// allocated for a key that compares equal beyond what reflection needs to
// read a map. The delta emit receives is the walk's own: valid only during
// the call.
func compare(root *schemaNode, a, b reflect.Value, emit func(*delta)) error {
	w := &comparer{emit: emit}
	w.node(root, frame{}, a, b)
	return w.err
}

// compareFrom compares a and b, the values at key at of two configs: n is
// the node of the key's declared type and f the frame there. A key whose
// node is unknown — its path cannot be followed on the new side — is
// compared whole and redacted.
func compareFrom(at keyRef, n *schemaNode, f frame, a, b side, emit func(*delta)) error {
	w := &comparer{emit: emit, path: slices.Clone(at.path), ord: slices.Clone(at.ord)}
	if n == nil {
		if !w.checked(a) || !w.checked(b) || sameValue(a, b) {
			return w.err
		}
		w.report(deltaValue, a.present != b.present || a.isNil != b.isNil, true, true, a, b)
		return w.err
	}
	w.resolved(n, f, a, b)
	return w.err
}

// compareEvery walks v, a config of root's schema, and calls emit for every
// key it holds, equal to itself: a delta whose two sides are one value, by
// the rules a comparison follows.
func compareEvery(root *schemaNode, v reflect.Value, emit func(*delta)) error {
	w := &comparer{emit: emit, every: true}
	w.node(root, frame{}, v, v)
	return w.err
}

// comparer is one side-by-side walk. Its path and walk position are stacks
// the walk pushes and pops; a delta copies nothing until its receiver does.
type comparer struct {
	emit func(*delta)
	// every reports every key, equal or not.
	every bool
	path  keyPath
	ord   []ordSeg
	// above holds, per side, the maps, slices and struct pointers the walk
	// is inside, so one met again is compared whole rather than walked
	// forever.
	above [2][]ancestor
	d     delta
	err   error
}

// ancestor identifies a container by address, type and, for a slice,
// length: a struct and its first field share an address, and two slices
// over one array are different values.
type ancestor struct {
	addr uintptr
	len  int
	typ  reflect.Type
}

func ancestorOf(s side) (ancestor, bool) {
	if !s.v.IsValid() || s.isNil {
		return ancestor{}, false
	}
	switch {
	case s.v.Kind() == reflect.Map:
		return ancestor{addr: s.v.Pointer(), typ: s.v.Type()}, true
	case s.v.Kind() == reflect.Slice:
		return ancestor{addr: s.v.Pointer(), len: s.v.Len(), typ: s.v.Type()}, true
	case s.v.CanAddr():
		return ancestor{addr: s.v.Addr().Pointer(), typ: s.v.Type()}, true
	}
	return ancestor{}, false
}

// frame is what makes the keys at a walk position sensitive, the rule
// cursor.sensitive applies: a field or squashed struct tagged sensitive, or
// a credential-like field name or map key above a value.
type frame struct {
	tagged  bool
	covered bool
}

func (f frame) sensitive() bool { return f.tagged || f.covered }

func (f frame) field(fld *schemaField) frame {
	return frame{
		tagged:  f.tagged || fld.tagged,
		covered: f.covered || fld.named && fld.node.kind != nodeBranch,
	}
}

func (f frame) entry(name string) frame {
	if !f.covered {
		f.covered = isSensitiveName(name)
	}
	return f
}

// deltaMode is how a delta's two sides are described.
type deltaMode uint8

const (
	deltaValue deltaMode = iota // a value compared by content: its text
	deltaShape                  // a struct, map, list or interface: presence and type
	deltaCycle                  // a container met again within itself on a side
)

// delta is one key whose value differs, as a walk found it. path and ord
// are the walk's buffers; a receiver that keeps them copies them.
type delta struct {
	path       keyPath
	ord        []ordSeg
	mode       deltaMode
	structural bool
	// whole reports a key compared whole: nothing beneath it, on either
	// side, is reported.
	whole     bool
	sensitive bool
	cycle     [2]bool // deltaCycle: which side leads back to itself
	sides     [2]side
}

// value describes side i (0 old, 1 new) of d.
func (d *delta) value(i int) Value {
	s := d.sides[i]
	switch d.mode {
	case deltaShape:
		return shape(s)
	case deltaCycle:
		if d.cycle[i] {
			return cycleSide(s)
		}
		return shape(s)
	}
	return describe(s, d.sensitive || uncheckedValue(d.sides[0]) || uncheckedValue(d.sides[1]))
}

// change is d as a [Change] of a config whose schema is root.
func (d *delta) change(root *schemaNode) Change {
	return Change{
		Key:        d.path.String(),
		Structural: d.structural,
		Field:      ownerOf(root, d.path),
		Old:        d.value(0),
		New:        d.value(1),
	}
}

// ownerOf is the declared field that owns key p of a config whose schema is
// root, as [FieldFor] reports it. p is a key a walk found, so it resolves.
func ownerOf(root *schemaNode, p keyPath) Field {
	c, _, err := root.walkTo(cursor{}, p)
	if err != nil {
		return Field{}
	}
	return c.owner
}

func (w *comparer) push(s segment, o ordSeg) {
	w.path = append(w.path, s)
	w.ord = append(w.ord, o)
}

func (w *comparer) pop() {
	w.path = w.path[:len(w.path)-1]
	w.ord = w.ord[:len(w.ord)-1]
}

func (w *comparer) report(mode deltaMode, structural, whole, sensitive bool, sa, sb side) {
	w.d = delta{
		path:       w.path,
		ord:        w.ord,
		mode:       mode,
		structural: structural,
		whole:      whole,
		sensitive:  sensitive,
		sides:      [2]side{sa, sb},
	}
	w.emit(&w.d)
}

func (w *comparer) reportCycle(cycle [2]bool, sa, sb side) {
	w.d = delta{
		path:       w.path,
		ord:        w.ord,
		mode:       deltaCycle,
		structural: sa.present != sb.present || sa.isNil != sb.isNil,
		whole:      true,
		cycle:      cycle,
		sides:      [2]side{sa, sb},
	}
	w.emit(&w.d)
}

func (w *comparer) fail(err error) {
	if w.err == nil {
		w.err = err
	}
}

// node compares a and b, two values of n's declared type, at the walk's
// position, which f makes sensitive or not.
func (w *comparer) node(n *schemaNode, f frame, a, b reflect.Value) {
	w.resolved(n, f, resolve(a), resolve(b))
}

func (w *comparer) resolved(n *schemaNode, f frame, sa, sb side) {
	if w.err != nil || !sa.present && !sb.present {
		return
	}
	switch {
	case n.kind == nodeLeaf:
		w.leaf(n, f, sa, sb)
	case f.sensitive() && n.kind != nodeBranch:
		// A map, list or interface whose keys may be secrets: compared
		// whole, never listed.
		w.whole(sa, sb)
	case n.kind == nodeDynamic:
		w.dynamic(f, sa, sb)
	default:
		w.container(n, f, sa, sb)
	}
}

// leaf compares two values of n's type, decoded whole.
func (w *comparer) leaf(n *schemaNode, f frame, sa, sb side) {
	if n.opaqueUnsafe && (!w.checked(sa) || !w.checked(sb)) || w.same(sa, sb) {
		return
	}
	w.report(deltaValue, false, false, f.sensitive(), sa, sb)
}

// whole compares two sensitive maps, lists or interfaces without listing
// what they hold.
func (w *comparer) whole(sa, sb side) {
	if !w.checked(sa) || !w.checked(sb) || w.same(sa, sb) {
		return
	}
	w.report(deltaValue, sa.present != sb.present || sa.isNil != sb.isNil, true, true, sa, sb)
}

// dynamic compares the values two interfaces hold: values of one type key
// by key, anything else whole.
func (w *comparer) dynamic(f frame, sa, sb side) {
	var nodes [2]*schemaNode
	for i, s := range [2]side{sa, sb} {
		if s.v.IsValid() {
			n, err := schemaOf(s.v.Type())
			if err != nil {
				w.fail(err)
				return
			}
			nodes[i] = n
		}
	}
	if nodes[0] != nil && nodes[0] == nodes[1] {
		w.resolved(nodes[0], f, sa, sb)
		return
	}
	// One side missing or nil, or the two of different types.
	if !w.checked(sa) || !w.checked(sb) || w.same(sa, sb) {
		return
	}
	if !composite(sa) && !composite(sb) {
		w.report(deltaValue, false, true, f.sensitive(), sa, sb)
		return
	}
	w.report(deltaShape, true, true, false, sa, sb)
}

// container compares two structs, maps or lists of n's type: their shape
// at their key, then the keys beneath them.
func (w *comparer) container(n *schemaNode, f frame, sa, sb side) {
	ka, okA := ancestorOf(sa)
	kb, okB := ancestorOf(sb)
	cycle := [2]bool{okA && slices.Contains(w.above[0], ka), okB && slices.Contains(w.above[1], kb)}
	if cycle[0] || cycle[1] {
		if !w.checked(sa) || !w.checked(sb) || w.same(sa, sb) {
			return
		}
		w.reportCycle(cycle, sa, sb)
		return
	}
	if len(w.path) > 0 && (w.every || sa.present != sb.present || sa.isNil != sb.isNil) {
		w.report(deltaShape, sa.present != sb.present || sa.isNil != sb.isNil, false, false, sa, sb)
	}
	if okA {
		w.above[0] = append(w.above[0], ka)
		defer func() { w.above[0] = w.above[0][:len(w.above[0])-1] }()
	}
	if okB {
		w.above[1] = append(w.above[1], kb)
		defer func() { w.above[1] = w.above[1][:len(w.above[1])-1] }()
	}
	switch n.kind {
	case nodeBranch:
		for i, fld := range n.fields {
			w.push(segment{kind: segField, name: fld.key}, ordSeg{n: i})
			w.node(fld.node, f.field(fld), fieldOf(sa, fld), fieldOf(sb, fld))
			w.pop()
		}
	case nodeMap:
		ma, mb := readMap(sa), readMap(sb)
		i, j := 0, 0
		for i < len(ma.keys) || j < len(mb.keys) {
			var a, b reflect.Value
			var name string
			switch {
			case j >= len(mb.keys) || i < len(ma.keys) && ma.keys[i].name < mb.keys[j].name:
				name, a = ma.keys[i].name, ma.at(i)
				i++
			case i >= len(ma.keys) || ma.keys[i].name > mb.keys[j].name:
				name, b = mb.keys[j].name, mb.at(j)
				j++
			default:
				name, a, b = ma.keys[i].name, ma.at(i), mb.at(j)
				i++
				j++
			}
			w.push(segment{kind: segKey, name: name}, ordSeg{s: name})
			w.node(n.elem, f.entry(name), a, b)
			w.pop()
		}
	case nodeList:
		for i := range max(listLen(sa), listLen(sb)) {
			w.push(segment{kind: segIndex, index: i}, ordSeg{n: i})
			w.node(n.elem, f, elementOf(sa, i), elementOf(sb, i))
			w.pop()
		}
	}
}

// same reports two sides the walk need not report: equal ones, unless it
// reports every key.
func (w *comparer) same(a, b side) bool {
	return !w.every && sameValue(a, b)
}

// checked reports whether s can be compared by content, recording the
// error when it cannot.
func (w *comparer) checked(s side) bool {
	if !s.v.IsValid() || s.isNil {
		return true
	}
	if t := uncomparableIn(s.v); t != nil {
		w.fail(fmt.Errorf("%w: %s holds a %s, which cannot be compared", ErrSchema, w.path.orRoot(), typeText(t)))
		return false
	}
	return true
}

// side is a value as found: present or absent, and when present, nil or not.
type side struct {
	v       reflect.Value // the dereferenced value; invalid when absent or nil
	present bool
	isNil   bool
}

// resolve dereferences v, a value of some declared type, through pointers and
// interfaces. The bound stops a pointer that points at itself.
func resolve(v reflect.Value) side {
	if !v.IsValid() {
		return side{}
	}
	for range maxPointerDepth {
		if v.Kind() != reflect.Pointer && v.Kind() != reflect.Interface {
			break
		}
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
		return typeText(s.v.Type())
	}
	return ""
}

// fieldOf is f's value in a struct side, or invalid when the side has no
// struct or an embedded pointer on the way is nil.
func fieldOf(s side, f *schemaField) reflect.Value {
	if !s.v.IsValid() || s.isNil {
		return reflect.Value{}
	}
	v, err := f.value(s.v)
	if err != nil {
		return reflect.Value{}
	}
	return v
}

// mapRead is a map side's entries sorted by key, each value copied out once
// into one slice, so reading an entry allocates nothing.
type mapRead struct {
	keys []mapKey
	vals reflect.Value // a slice of the map's element type
}

type mapKey struct {
	name string
	i    int // the entry's value in vals
}

func readMap(s side) mapRead {
	if !s.v.IsValid() || s.isNil || s.v.Len() == 0 {
		return mapRead{}
	}
	n := s.v.Len()
	t := s.v.Type()
	r := mapRead{keys: make([]mapKey, 0, n), vals: reflect.MakeSlice(reflect.SliceOf(t.Elem()), n, n)}
	k := reflect.New(t.Key()).Elem()
	var it reflect.MapIter
	it.Reset(s.v)
	for i := 0; it.Next(); i++ {
		k.SetIterKey(&it)
		r.vals.Index(i).SetIterValue(&it)
		r.keys = append(r.keys, mapKey{name: k.String(), i: i})
	}
	slices.SortFunc(r.keys, func(a, b mapKey) int { return strings.Compare(a.name, b.name) })
	return r
}

// at is the value of the i-th entry in key order.
func (r mapRead) at(i int) reflect.Value { return r.vals.Index(r.keys[i].i) }

// locate finds key p in v, a config of root's schema: the value there, the
// node of its declared type, and the frame that makes it sensitive. A key
// beneath a missing or nil container is absent, its node nil when the path
// cannot be followed.
func locate(root *schemaNode, v reflect.Value, p keyPath) (side, *schemaNode, frame) {
	n, f, s := root, frame{}, resolve(v)
	for _, seg := range p {
		if n.kind == nodeDynamic {
			if !s.v.IsValid() {
				return side{}, nil, f
			}
			dn, err := schemaOf(s.v.Type())
			if err != nil {
				return side{}, nil, f
			}
			n = dn
		}
		switch n.kind {
		case nodeBranch:
			fld := n.byKey[seg.name]
			if fld == nil {
				return side{}, nil, f
			}
			s, n, f = resolve(fieldOf(s, fld)), fld.node, f.field(fld)
		case nodeMap:
			s, n, f = resolve(newEntries(s).of(seg.name)), n.elem, f.entry(seg.name)
		case nodeList:
			s, n = resolve(elementOf(s, seg.index)), n.elem
		default:
			return side{}, nil, f
		}
	}
	return s, n, f
}

// entries looks up a map side's entries by key text.
type entries struct {
	m   reflect.Value // invalid when the side has no map
	key reflect.Value // scratch, of the map's key type
}

func newEntries(s side) entries {
	if !s.v.IsValid() || s.isNil || s.v.Kind() != reflect.Map {
		return entries{}
	}
	return entries{m: s.v, key: reflect.New(s.v.Type().Key()).Elem()}
}

func (e entries) of(name string) reflect.Value {
	if !e.m.IsValid() {
		return reflect.Value{}
	}
	e.key.SetString(name)
	return e.m.MapIndex(e.key)
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

// sameValue reports whether two sides hold equal values: by kind for a
// scalar, by reflect.DeepEqual for anything else.
func sameValue(a, b side) bool {
	if a.present != b.present || a.isNil != b.isNil {
		return false
	}
	if !a.v.IsValid() || !b.v.IsValid() {
		return a.v.IsValid() == b.v.IsValid()
	}
	if a.v.Type() != b.v.Type() {
		return false
	}
	switch a.v.Kind() {
	case reflect.Bool:
		return a.v.Bool() == b.v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return a.v.Int() == b.v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return a.v.Uint() == b.v.Uint()
	case reflect.Float32, reflect.Float64:
		return a.v.Float() == b.v.Float()
	case reflect.Complex64, reflect.Complex128:
		return a.v.Complex() == b.v.Complex()
	case reflect.String:
		return a.v.String() == b.v.String()
	}
	return reflect.DeepEqual(a.v.Interface(), b.v.Interface())
}

// uncomparableIn returns the type of a non-nil func, channel or unsafe
// pointer anywhere in v — reflect.DeepEqual compares those by identity,
// which a config cannot mean — or nil when there is none.
func uncomparableIn(v reflect.Value) reflect.Type {
	if !mayHoldUncomparable(v.Type()) {
		return nil
	}
	return (&uncomparableSearch{}).value(v)
}

// uncomparableSearch looks through one value, each map, slice and pointer
// once, so a cycle ends.
type uncomparableSearch struct {
	seen map[ancestor]bool
}

func (s *uncomparableSearch) value(v reflect.Value) reflect.Type {
	if !mayHoldUncomparable(v.Type()) {
		return nil
	}
	switch v.Kind() {
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		if v.IsNil() {
			return nil
		}
		return v.Type()
	case reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return s.value(v.Elem())
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if v.IsNil() || s.visited(v) {
			return nil
		}
		switch v.Kind() {
		case reflect.Pointer:
			return s.value(v.Elem())
		case reflect.Map:
			for it := v.MapRange(); it.Next(); {
				if t := cmp.Or(s.value(it.Key()), s.value(it.Value())); t != nil {
					return t
				}
			}
			return nil
		}
		fallthrough
	case reflect.Array:
		for i := range v.Len() {
			if t := s.value(v.Index(i)); t != nil {
				return t
			}
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if t := s.value(v.Field(i)); t != nil {
				return t
			}
		}
	}
	return nil
}

func (s *uncomparableSearch) visited(v reflect.Value) bool {
	key := ancestor{addr: v.Pointer(), typ: v.Type()}
	if v.Kind() == reflect.Slice {
		key.len = v.Len()
	}
	if s.seen == nil {
		s.seen = map[ancestor]bool{}
	}
	if s.seen[key] {
		return true
	}
	s.seen[key] = true
	return false
}

var uncomparableTypes sync.Map // reflect.Type → bool

// mayHoldUncomparable reports whether a value of type t can hold a func,
// channel or unsafe pointer: one that is, or holds an interface that could.
func mayHoldUncomparable(t reflect.Type) bool {
	if v, ok := uncomparableTypes.Load(t); ok {
		return v.(bool)
	}
	may := typeMayHoldUncomparable(t, map[reflect.Type]bool{})
	uncomparableTypes.Store(t, may)
	return may
}

func typeMayHoldUncomparable(t reflect.Type, visiting map[reflect.Type]bool) bool {
	if visiting[t] {
		return false // whatever the type holds is judged where it was first met
	}
	visiting[t] = true
	switch t.Kind() {
	case reflect.Func, reflect.Chan, reflect.UnsafePointer, reflect.Interface:
		return true
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return typeMayHoldUncomparable(t.Elem(), visiting)
	case reflect.Map:
		return typeMayHoldUncomparable(t.Key(), visiting) || typeMayHoldUncomparable(t.Elem(), visiting)
	case reflect.Struct:
		for i := range t.NumField() {
			if typeMayHoldUncomparable(t.Field(i).Type, visiting) {
				return true
			}
		}
	}
	return false
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
// one it lies within.
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
