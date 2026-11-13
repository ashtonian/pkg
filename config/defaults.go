package config

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/knadh/koanf/v2"
)

// Defaulter is a config type that sets defaults in code, for what a default
// tag cannot say: a shared section that each program embedding it sets
// differently, or one type at several paths with different defaults.
//
//	type Brokers struct {
//	    Primary   Broker `koanf:"primary"`
//	    Secondary Broker `koanf:"secondary"`
//	}
//
//	// Only the primary broker has a default host.
//	func (b *Brokers) SetDefaults() { b.Primary.Host = "tcp://localhost:1883" }
//
// A load fills a fresh value with the default tags, then calls SetDefaults
// on every Defaulter in it — the root, its sections and pointer sections,
// and fields of named types — innermost first, so an enclosing struct's
// SetDefaults overrides a section's. What the calls change is the code
// defaults layer: above the tags, below [WithDefaults] and every source. A
// field SetDefaults leaves alone keeps its tag. A map's or list's element
// type that is a Defaulter runs for each entry a source creates, after its
// tags, as element tags apply. A nil pointer section whose type sets defaults
// is allocated for its SetDefaults and kept only if it then holds anything.
//
// An embedded struct's SetDefaults is promoted as Go promotes it: it runs as
// the enclosing struct's, unless the enclosing struct declares its own,
// which calls the embedded one when it should run. Two embedded Defaulters
// whose methods collide promote neither, which fails with [ErrSchema], as
// does SetDefaults with a value receiver, which could change nothing.
//
// SetDefaults is deterministic and does no I/O: it runs at every load and
// reload, and for [Usage], [JSONSchema], [PrintTemplate], [CheckKey] and
// [DecodeKey].
type Defaulter interface {
	SetDefaults()
}

var defaulterType = reflect.TypeFor[Defaulter]()

// addSchemaDefaults merges the defaults the schema declares, the tags and
// then what its Defaulters change, and returns the Defaulters' layer. o
// supplies the decode hooks the tags are decoded with; nil for none.
func (l *loader) addSchemaDefaults(o *options) (map[string]any, error) {
	if tags := defaultsFrom(fieldsOf(l.root)); len(tags) > 0 {
		if err := l.addFlat("struct-tag defaults", tags, constant(Source{Kind: SourceTag})); err != nil {
			return nil, err
		}
	}
	if !setsDefaults(derefType(l.root)) {
		return nil, nil // Raw copies the whole tree: only for a Defaulter
	}
	code, err := codeDefaults(l.root, l.schema, l.k.Raw(), o)
	if err != nil || len(code) == 0 {
		return nil, err
	}
	return code, l.merge("SetDefaults", code, constant(Source{Kind: SourceCodeDefaults}))
}

// codeDefaults is what t's Defaulters change in a value holding tags, t's
// tag defaults: a tree of the values they set, keyed as the fields are, nil
// when t has no Defaulter or they change nothing.
func codeDefaults(t reflect.Type, schema *schemaNode, tags map[string]any, o *options) (map[string]any, error) {
	t = derefType(t)
	if !setsDefaults(t) {
		return nil, nil
	}
	if err := checkDefaulters(t); err != nil {
		return nil, err
	}
	if o == nil {
		o = &options{}
	}
	before := reflect.New(t)
	if _, err := o.decodeInput(schema, tags, before.Interface(), nil); err != nil {
		return nil, fmt.Errorf("decoding the default tags of %s for SetDefaults: %w: %w", typeText(t), ErrValidation, err)
	}
	after := reflect.New(t)
	after.Elem().Set(deepCopy(before.Elem()))
	run := &defaulterRun{path: map[reflect.Type]bool{}}
	run.run(after.Elem())
	run.release()
	tree, _ := diffTree(schema, before.Elem(), after.Elem())
	m, _ := tree.(map[string]any)
	return m, nil
}

// setsDefaults reports whether a value of t sets defaults in code: t is a
// Defaulter, or a struct holding one outside its maps and lists. Element
// types set the defaults of their own entries.
func setsDefaults(t reflect.Type) bool {
	if v, ok := setsDefaultsCache.Load(t); ok {
		return v.(bool)
	}
	sets := setsDefaultsOf(t, map[reflect.Type]bool{})
	setsDefaultsCache.Store(t, sets)
	return sets
}

var setsDefaultsCache sync.Map // reflect.Type → bool

func setsDefaultsOf(t reflect.Type, walking map[reflect.Type]bool) bool {
	if reflect.PointerTo(t).Implements(defaulterType) {
		return true
	}
	if t.Kind() != reflect.Struct || walking[t] {
		return false
	}
	walking[t] = true
	defer delete(walking, t)
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() && !sf.Anonymous {
			continue
		}
		if setsDefaultsOf(derefType(sf.Type), walking) {
			return true
		}
	}
	return false
}

// checkDefaulters fails with ErrSchema for a Defaulter that cannot run: a
// SetDefaults with a value receiver, or embedded Defaulters whose methods
// collide, so neither is promoted.
func checkDefaulters(t reflect.Type) error {
	if v, ok := checkDefaultersCache.Load(t); ok {
		err, _ := v.(error)
		return err
	}
	err := checkDefaultersOf(t, map[reflect.Type]bool{})
	checkDefaultersCache.Store(t, err)
	return err
}

var checkDefaultersCache sync.Map // reflect.Type → error, nil included

func checkDefaultersOf(t reflect.Type, walking map[reflect.Type]bool) error {
	if walking[t] {
		return nil
	}
	walking[t] = true
	defer delete(walking, t)
	embedded := embeddedDefaulters(t)
	switch {
	case t.Kind() != reflect.Interface && t.Implements(defaulterType) && len(embedded) == 0:
		return fmt.Errorf("%w: %s.SetDefaults has a value receiver, so it cannot change the config; declare it on *%s", ErrSchema, typeText(t), typeText(t))
	case t.Kind() == reflect.Struct && len(embedded) > 0 && !reflect.PointerTo(t).Implements(defaulterType):
		return fmt.Errorf("%w: %s embeds %s, whose SetDefaults methods collide, so none runs; declare %s.SetDefaults calling each", ErrSchema, typeText(t), strings.Join(embedded, " and "), typeText(t))
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() && !sf.Anonymous {
			continue
		}
		if err := checkDefaultersOf(derefType(sf.Type), walking); err != nil {
			return err
		}
	}
	return nil
}

// embeddedDefaulters names the embedded fields of t, a struct, that set
// defaults through a method t would be promoted: Defaulters, or embedded
// structs that promote one of their own.
func embeddedDefaulters(t reflect.Type) []string {
	if t.Kind() != reflect.Struct {
		return nil
	}
	var names []string
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.Anonymous {
			continue
		}
		et := derefType(sf.Type)
		if et.Kind() == reflect.Interface && et.Implements(defaulterType) ||
			reflect.PointerTo(et).Implements(defaulterType) || len(embeddedDefaulters(et)) > 0 {
			names = append(names, typeText(et))
		}
	}
	return names
}

// defaulterRun calls the SetDefaults methods of a config value.
type defaulterRun struct {
	path map[reflect.Type]bool // the structs on the current path: a recursive type stops
	// allocated are the nil pointer sections allocated for a SetDefaults,
	// in the order allocated: each is kept only if it holds something once
	// every SetDefaults has run, since an enclosing one may set it.
	allocated []reflect.Value
}

// run calls the SetDefaults of v, an addressable value, after those of
// everything v holds outside maps and lists.
func (r *defaulterRun) run(v reflect.Value) {
	t := v.Type()
	if t.Kind() == reflect.Struct {
		if r.path[t] {
			return
		}
		r.path[t] = true
		defer delete(r.path, t)
		r.fields(v)
	}
	if v.CanAddr() && reflect.PointerTo(t).Implements(defaulterType) && v.Addr().CanInterface() {
		v.Addr().Interface().(Defaulter).SetDefaults()
	}
}

// fields runs the Defaulters v's fields hold. An embedded struct's own
// SetDefaults is promoted to v's, so only what it holds runs here.
func (r *defaulterRun) fields(v reflect.Value) {
	t := v.Type()
	for i := range t.NumField() {
		sf, f := t.Field(i), v.Field(i)
		if !sf.IsExported() && !sf.Anonymous {
			continue
		}
		embedded := sf.Anonymous && derefType(sf.Type).Kind() == reflect.Struct
		if f.Kind() == reflect.Pointer {
			elem := f.Type().Elem()
			if f.IsNil() {
				if !f.CanSet() || r.path[elem] || !setsDefaults(elem) {
					continue
				}
				f.Set(reflect.New(elem))
				r.allocated = append(r.allocated, f)
			}
			f = f.Elem()
		}
		if embedded {
			r.fields(f)
			continue
		}
		r.run(f)
	}
}

// release sets back to nil each allocated section that still holds
// nothing, the innermost first, so one emptied that way can empty the
// section around it.
func (r *defaulterRun) release() {
	for i := len(r.allocated) - 1; i >= 0; i-- {
		if p := r.allocated[i]; p.Elem().IsZero() {
			p.Set(reflect.Zero(p.Type()))
		}
	}
}

// diffTree is what b, the value of a key of node n after SetDefaults, sets
// that a, the value before, did not: the tree to merge above the tags, and
// false when they are equal. A struct is its changed fields, so the tags of
// the rest stay beneath; a map entry or list element of a struct type is the
// fields its SetDefaults left set, so the element's own defaults fill the
// rest; an absent a compares b with nothing.
func diffTree(n *schemaNode, a, b reflect.Value) (any, bool) {
	a, b = derefValue(a), derefValue(b)
	if absent(b) {
		return nil, !absent(a)
	}
	if !absent(a) && reflect.DeepEqual(a.Interface(), b.Interface()) {
		return nil, false
	}
	switch n.kind {
	case nodeBranch:
		if absent(a) {
			a = reflect.New(n.typ).Elem()
		}
		out := map[string]any{}
		for _, f := range n.fields {
			fa, _ := f.value(a)
			fb, _ := f.value(b)
			if child, ok := diffTree(f.node, fa, fb); ok {
				out[f.key] = child
			}
		}
		return out, true // an empty one still makes the section present
	case nodeMap:
		if b.IsNil() {
			return nil, !absent(a) && !a.IsNil()
		}
		if !absent(a) {
			for it := a.MapRange(); it.Next(); {
				if !b.MapIndex(it.Key()).IsValid() {
					return leafInput(b), true // an entry removed: the whole map replaces the tags'
				}
			}
		}
		out := map[string]any{}
		for it := b.MapRange(); it.Next(); {
			var was reflect.Value
			if !absent(a) {
				was = a.MapIndex(it.Key())
			}
			if child, ok := diffTree(n.elem, was, it.Value()); ok || !was.IsValid() {
				out[it.Key().String()] = child
			}
		}
		return out, true
	case nodeList:
		if b.Kind() == reflect.Slice && b.IsNil() {
			return nil, !absent(a) && !a.IsNil()
		}
		if n.elem.kind != nodeBranch {
			return leafInput(b), true
		}
		out := make([]any, b.Len())
		for i := range b.Len() {
			out[i], _ = diffTree(n.elem, reflect.Value{}, b.Index(i))
		}
		return out, true
	}
	return leafInput(b), true
}

func absent(v reflect.Value) bool {
	return !v.IsValid() || (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil()
}

// leafInput is v as a source value: v itself, or for a struct that
// marshals to text, its text. Merging copies a value through its exported
// fields only, which would lose such a struct's state.
func leafInput(v reflect.Value) any {
	if v.Kind() == reflect.Struct {
		m, ok := v.Interface().(encoding.TextMarshaler)
		if !ok && v.CanAddr() {
			m, ok = v.Addr().Interface().(encoding.TextMarshaler)
		}
		if ok {
			if text, err := m.MarshalText(); err == nil {
				return string(text)
			}
		}
	}
	return v.Interface()
}

type declaredDefaults struct {
	root  *schemaNode
	value reflect.Value
	tree  map[string]any
	// code is the layer t's Defaulters set; nil when they set nothing.
	code map[string]any
}

// readDefaults is t's defaults decoded, without the decode hooks a load may
// add: the tags, what its Defaulters change, and its collections' element
// defaults.
func readDefaults(t reflect.Type) (declaredDefaults, error) {
	t = derefType(t)
	root, err := rootSchema(t)
	if err != nil {
		return declaredDefaults{}, err
	}
	l := &loader{k: koanf.New("."), root: t, schema: root}
	code, err := l.addSchemaDefaults(nil)
	if err != nil {
		return declaredDefaults{}, err
	}
	tree := l.k.Raw()
	if err := newElementDefaults(nil, false).fill(t, tree); err != nil {
		return declaredDefaults{}, err
	}
	v, err := decodeDefaultTree(t, root, tree)
	if err != nil {
		err = fmt.Errorf("%w: %w", ErrValidation, err)
	}
	return declaredDefaults{root: root, value: v, tree: tree, code: code}, err
}

func decodeDefaultTree(t reflect.Type, root *schemaNode, tree map[string]any) (reflect.Value, error) {
	out := reflect.New(derefType(t))
	md, err := (&options{}).decodeInput(root, tree, out.Interface(), nil)
	if err != nil {
		return reflect.Value{}, fmt.Errorf("decoding defaults of %s: %w", typeText(t), err)
	}
	if len(md.Unused) > 0 {
		unknown := &UnknownKeysError{Type: t}
		for _, name := range md.Unused {
			unknown.Keys = append(unknown.Keys, unusedKey(root, tree, name))
		}
		return reflect.Value{}, fmt.Errorf("defaults of %s: %w: %w", typeText(t), ErrSchema, unknown)
	}
	return out.Elem(), nil
}

func (d declaredDefaults) has(path keyPath) bool {
	var value any = d.tree
	for _, s := range path {
		if s.kind != segField && s.kind != segKey {
			return false
		}
		m, ok := value.(map[string]any)
		if !ok {
			v := derefValue(reflect.ValueOf(value))
			return v.IsValid() && v.Kind() == reflect.Struct && d.field(d.root, path).IsValid()
		}
		value, ok = m[s.name]
		if !ok {
			return false
		}
	}
	return true
}

// fromCode reports whether the Defaulters' layer sets path, or a key above
// it, so that what path holds is theirs rather than its tag's.
func (d declaredDefaults) fromCode(path keyPath) bool {
	var value any = d.code
	if d.code == nil {
		return false
	}
	for _, s := range path {
		m, ok := value.(map[string]any)
		if !ok {
			return true // set whole above path
		}
		if s.kind != segField && s.kind != segKey {
			return false
		}
		if value, ok = m[s.name]; !ok {
			return false
		}
	}
	return true
}

func (d declaredDefaults) field(root *schemaNode, path keyPath) reflect.Value {
	v := d.value
	for _, s := range path {
		v = derefValue(v)
		if !v.IsValid() || s.kind != segField || root.kind != nodeBranch {
			return reflect.Value{}
		}
		f := root.byKey[s.name]
		if f == nil {
			return reflect.Value{}
		}
		v, _ = f.value(v)
		root = f.node
	}
	return v
}

func defaultText(v reflect.Value) (string, bool, error) {
	w := &valueTree{visiting: map[visit]bool{}}
	n := w.value(v, site{})
	if w.err != nil {
		return "", false, w.err
	}
	if n.hasRedaction() {
		return "", true, nil
	}
	if n.kind == outScalar {
		return scalarText(n.scalar), false, nil
	}
	b, err := json.Marshal(n.plain())
	return string(b), false, err
}
