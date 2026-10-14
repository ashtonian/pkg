package config

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
)

// The schema graph is the one reading of a config type every walk shares:
// which fields are keys, which values decode whole, and what is a map or a
// list of entries. It follows the rules documented on [Fields].

type nodeKind uint8

const (
	nodeLeaf    nodeKind = iota // decoded whole: a scalar, a self-decoding struct, a map keyed by non-strings, []byte
	nodeBranch                  // a struct whose fields are keys
	nodeMap                     // a map keyed by strings
	nodeList                    // a slice or array
	nodeDynamic                 // an interface: its shape is known only per value
)

type schemaNode struct {
	typ     reflect.Type // dereferenced
	kind    nodeKind
	fields  []*schemaField // nodeBranch, squashed fields in place, in declaration order
	byKey   map[string]*schemaField
	byAlias map[string]*schemaField // nodeBranch: the fields alias tags name
	elem    *schemaNode             // nodeMap, nodeList
	// opaqueUnsafe reports a value decoded whole that could hold a func,
	// channel or unsafe pointer: a comparison looks inside it.
	opaqueUnsafe bool
}

type schemaField struct {
	key    string
	sf     reflect.StructField // the field declaring the key
	index  []int               // from the branch's type, through squashed structs
	tagged bool                // the field, or a squashed struct holding it, is tagged sensitive:"true"
	named  bool                // its Go name looks like a credential's
	node   *schemaNode
}

type schemaEntry struct {
	node *schemaNode
	err  error
}

var schemaCache sync.Map // reflect.Type → schemaEntry

// schemaOf returns the schema of t. A struct whose keys cannot be told apart —
// a key holding a character the key grammar uses, or two fields with one key
// — is an ErrSchema error.
func schemaOf(t reflect.Type) (*schemaNode, error) {
	t = derefType(t)
	if e, ok := schemaCache.Load(t); ok {
		e := e.(schemaEntry)
		return e.node, e.err
	}
	b := &schemaBuilder{nodes: map[reflect.Type]*schemaNode{}}
	n := b.node(t)
	if b.err == nil && n.kind == nodeBranch {
		b.err = checkEnvTags(n)
	}
	e, _ := schemaCache.LoadOrStore(t, schemaEntry{node: n, err: b.err})
	return e.(schemaEntry).node, e.(schemaEntry).err
}

// rootSchema is schemaOf for a type that must be a struct.
func rootSchema(t reflect.Type) (*schemaNode, error) {
	n, err := schemaOf(t)
	if err != nil {
		return nil, err
	}
	if n.kind != nodeBranch {
		return nil, fmt.Errorf("%w: %s is not a struct", ErrSchema, typeText(t))
	}
	return n, nil
}

type schemaBuilder struct {
	nodes map[reflect.Type]*schemaNode
	err   error
}

func (b *schemaBuilder) node(t reflect.Type) *schemaNode {
	t = derefType(t)
	if n, ok := b.nodes[t]; ok {
		return n // a recursive type points back at its own node
	}
	n := &schemaNode{typ: t}
	b.nodes[t] = n
	switch k := t.Kind(); {
	case k == reflect.Interface:
		n.kind = nodeDynamic
	case isBranch(t):
		n.kind = nodeBranch
		n.byKey = map[string]*schemaField{}
		n.byAlias = map[string]*schemaField{}
		b.fields(n, t, nil, false, map[reflect.Type]bool{})
	case opaque(t):
		n.kind = nodeLeaf
	case k == reflect.Map && t.Key().Kind() == reflect.String:
		n.kind = nodeMap
		n.elem = b.node(t.Elem())
	case k == reflect.Map:
		n.kind = nodeLeaf
		n.elem = b.node(t.Elem()) // decoded whole for diagnostics, but element tags still apply
	case (k == reflect.Slice || k == reflect.Array) && t.Elem().Kind() != reflect.Uint8:
		n.kind = nodeList
		n.elem = b.node(t.Elem())
	default:
		n.kind = nodeLeaf
	}
	n.opaqueUnsafe = n.kind == nodeLeaf && mayHoldUncomparable(t)
	return n
}

// fields adds struct t's keys to branch n, squashed structs' keys in place.
func (b *schemaBuilder) fields(n *schemaNode, t reflect.Type, index []int, tagged bool, squashing map[reflect.Type]bool) {
	if squashing[t] {
		return
	}
	squashing[t] = true
	defer delete(squashing, t)
	for i := range t.NumField() {
		if f := t.Field(i); unexportedSquashedPointer(f) {
			b.fail(fmt.Errorf("%w: field %s of %s squashes a pointer to an unexported struct, which decoding cannot allocate; embed the struct itself, or export its type", ErrSchema, f.Name, typeText(t)))
		}
	}
	for kf := range keyedFields(t) {
		if err := checkTags(t, kf.field); err != nil {
			b.fail(err)
			continue
		}
		at := append(slices.Clone(index), kf.field.Index...)
		fieldTagged := tagged || taggedSensitive(kf.field.Tag)
		if kf.key == "" {
			if _, ok := kf.field.Tag.Lookup("alias"); ok {
				b.fail(fmt.Errorf("%w: field %s of %s is squashed, so it has no key an alias could stand for", ErrSchema, kf.field.Name, typeText(t)))
				continue
			}
			if ft := derefType(kf.field.Type); isBranch(ft) {
				b.fields(n, ft, at, fieldTagged, squashing)
			}
			continue // squash on a leaf contributes no key; decoding rejects it
		}
		switch {
		case !validFieldKey(kf.key):
			b.fail(fmt.Errorf("%w: field %s of %s has key %q, which holds a character keys use for structure", ErrSchema, kf.field.Name, typeText(t), kf.key))
			continue
		case n.byKey[kf.key] != nil:
			b.fail(fmt.Errorf("%w: fields %s and %s of %s both have key %q", ErrSchema, n.byKey[kf.key].sf.Name, kf.field.Name, typeText(n.typ), kf.key))
			continue
		case foldedField(n.byAlias, kf.key) != nil:
			b.fail(fmt.Errorf("%w: field %s of %s has key %q, which field %s names as an alias", ErrSchema, kf.field.Name, typeText(n.typ), kf.key, foldedField(n.byAlias, kf.key).sf.Name))
			continue
		}
		f := &schemaField{key: kf.key, sf: kf.field, index: at, tagged: fieldTagged, named: nameSensitive(kf.field), node: b.node(kf.field.Type)}
		n.fields = append(n.fields, f)
		n.byKey[kf.key] = f
		for _, a := range aliasNames(kf.field.Tag) {
			switch {
			case !validFieldKey(a) || strings.EqualFold(a, kf.key):
				b.fail(fmt.Errorf("%w: field %s of %s has alias %q; an alias is another key, of the key grammar", ErrSchema, kf.field.Name, typeText(n.typ), a))
			case foldedField(n.byKey, a) != nil:
				b.fail(fmt.Errorf("%w: field %s of %s has alias %q, which is the key of field %s", ErrSchema, kf.field.Name, typeText(n.typ), a, foldedField(n.byKey, a).sf.Name))
			case foldedField(n.byAlias, a) != nil:
				b.fail(fmt.Errorf("%w: fields %s and %s of %s both have alias %q (ignoring case)", ErrSchema, foldedField(n.byAlias, a).sf.Name, kf.field.Name, typeText(n.typ), a))
			default:
				n.byAlias[a] = f
			}
		}
	}
}

func foldedField(fields map[string]*schemaField, key string) *schemaField {
	for name, f := range fields {
		if strings.EqualFold(name, key) {
			return f
		}
	}
	return nil
}

// checkTags refuses a tag that cannot mean what it says: a sensitive value
// other than "true" or "false" — sensitive:"yes" would otherwise leave the
// field unprotected — a secret field opting out of redaction, a sep tag on
// anything but a list of values, an env tag on a section or naming no
// variable, an example its field cannot hold, and a map default that is not
// key=value entries.
func checkTags(t reflect.Type, f reflect.StructField) error {
	if v, ok := f.Tag.Lookup("sensitive"); ok {
		switch {
		case v != "true" && v != "false":
			return fmt.Errorf("%w: field %s of %s has sensitive:%q; want \"true\" or \"false\"", ErrSchema, f.Name, typeText(t), v)
		case v == "false" && taggedSensitive(f.Tag):
			return fmt.Errorf("%w: field %s of %s names a secret and is tagged sensitive:\"false\"", ErrSchema, f.Name, typeText(t))
		}
	}
	if sep, ok := f.Tag.Lookup("sep"); ok {
		lt := derefType(f.Type)
		switch {
		case lt.Kind() != reflect.Slice || lt.Elem().Kind() == reflect.Uint8 || decodesItself(lt) || isBranch(derefType(lt.Elem())):
			return fmt.Errorf("%w: field %s of %s has a sep tag, but only a list of values is split", ErrSchema, f.Name, typeText(t))
		case sep == "":
			return fmt.Errorf("%w: field %s of %s has an empty sep tag; name a separator, or none", ErrSchema, f.Name, typeText(t))
		}
	}
	if v, ok := f.Tag.Lookup("env"); ok {
		if !variableSets(f.Type) {
			return fmt.Errorf("%w: field %s of %s has an env tag, but no variable can set a %s", ErrSchema, f.Name, typeText(t), typeText(f.Type))
		}
		for _, name := range envTagNames(f.Tag) {
			if !envVarName.MatchString(name) {
				return fmt.Errorf("%w: field %s of %s has env:%q; %q is not a variable name", ErrSchema, f.Name, typeText(t), v, name)
			}
		}
	}
	if ex, ok := f.Tag.Lookup("example"); ok {
		if isBranch(derefType(f.Type)) {
			return fmt.Errorf("%w: field %s of %s has an example tag, but %s is a section; give its fields examples", ErrSchema, f.Name, typeText(t), f.Name)
		}
		// The value is not quoted: an example on a sensitive field may be one.
		if _, err := decodeTag(f.Type, f.Tag, ex); err != nil {
			return fmt.Errorf("%w: field %s of %s has an example that does not decode into %s", ErrSchema, f.Name, typeText(t), typeText(f.Type))
		}
	}
	if def, ok := f.Tag.Lookup("default"); ok && textMap(derefType(f.Type)) {
		if _, err := parseMapEntries(def); err != nil {
			return fmt.Errorf("%w: field %s of %s has a default that is not key=value entries: %w", ErrSchema, f.Name, typeText(t), err)
		}
	}
	return nil
}

// checkEnvTags refuses env tags a variable cannot address: one on a field of
// a list element or map entry, which no single key is, and a variable two
// fields name.
func checkEnvTags(root *schemaNode) error {
	owners := map[string]string{}
	type visit struct {
		node    *schemaNode
		crossed bool
	}
	walking := map[visit]bool{}
	var walk func(n *schemaNode, path keyPath, crossed bool) error
	walk = func(n *schemaNode, path keyPath, crossed bool) error {
		at := visit{n, crossed}
		if walking[at] {
			return nil
		}
		walking[at] = true
		defer delete(walking, at)
		switch {
		case (n.kind == nodeList || n.typ.Kind() == reflect.Map) && n.elem != nil:
			return walk(n.elem, path.any(), true)
		case n.kind != nodeBranch:
			return nil
		}
		for _, f := range n.fields {
			at := path.field(f.key)
			names := envTagNames(f.sf.Tag)
			if len(names) > 0 && crossed {
				return fmt.Errorf("%w: field %s of %s has an env tag, but %s is a key of every list element or map entry, which no one variable sets", ErrSchema, f.sf.Name, typeText(n.typ), at)
			}
			for _, name := range names {
				if other, dup := owners[name]; dup {
					return fmt.Errorf("%w: %s and %s both name the environment variable %s", ErrSchema, other, at, name)
				}
				owners[name] = at.String()
			}
			if err := walk(f.node, at, crossed); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root, nil, false)
}

func (b *schemaBuilder) fail(err error) {
	if b.err == nil {
		b.err = err
	}
}

// cursor is a walk's position: the key reached, its schema pattern, the
// declared field that owns it, and what makes it sensitive. It is a value;
// each step returns a new one.
type cursor struct {
	path    keyPath
	pattern keyPath
	index   []int // the owner's index sequence; nil once a collection or interface is crossed
	owner   Field // the nearest declared field: the key's own, an element's synthesized one, or the field holding an interface
	tagged  bool  // beneath a field or squashed struct tagged sensitive:"true"
	covered bool  // beneath a map, list or interface named like a credential, or a map key that is
}

// sensitive reports whether the value at c is redacted: the rule
// PrintRedacted applies.
func (c cursor) sensitive() bool { return c.owner.Sensitive || c.tagged || c.covered }

// intoField steps into field f of a branch.
func (c cursor) intoField(f *schemaField) cursor {
	next := c
	next.path = c.path.field(f.key)
	next.tagged = c.tagged || f.tagged
	named := f.named
	next.pattern = c.pattern.field(f.key)
	if c.index != nil || len(c.path) == 0 {
		next.index = append(slices.Clone(c.index), f.index...)
	}
	// A field's name covers the leaf, map, list or interface it holds, but
	// not a struct: an Auth block's fields are judged by their own names.
	if named && f.node.kind != nodeBranch {
		next.covered = true
	}
	next.owner = Field{
		Path:      next.pattern.String(),
		Tag:       f.sf.Tag,
		Type:      f.sf.Type,
		Sensitive: next.tagged || next.covered,
		Index:     next.index,
	}
	return next
}

// intoEntry steps into one entry of a map or list whose elements have type
// elem: s is its key, its index, or [*] for any.
func (c cursor) intoEntry(s segment, elem reflect.Type) cursor {
	next := c
	next.path = append(slices.Clip(c.path), s)
	if s.kind == segKey {
		next.covered = c.covered || isSensitiveName(s.name)
	}
	next.pattern = c.pattern.any()
	next.index = nil
	next.owner = Field{
		Path:      next.pattern.String(),
		Type:      elem,
		Sensitive: next.tagged || next.covered,
	}
	return next
}

// FieldFor returns the declared field a key of T names. A struct field's key
// returns its Field, Index included. A key inside a map or list —
// items[0].port, limits["/upload"] — returns a Field synthesized for every
// element or entry: Path is the pattern (items[*].port), Tag and Type the
// element's, Sensitive inherited from what holds it, and Index nil. A key
// beneath an interface returns the interface's field, which owns whatever
// the value holds. The map keys and indexes in key need not exist in any
// config; a field T does not have, a selector of the wrong kind, an index
// past a fixed array's end, or a key that does not parse is an ErrKey error.
func FieldFor[T any](key string) (Field, error) {
	root, err := rootSchema(reflect.TypeFor[T]())
	if err != nil {
		return Field{}, err
	}
	p, err := parseKeyPath(key)
	if err != nil {
		return Field{}, err
	}
	c, _, err := root.walkTo(cursor{}, p)
	if err != nil {
		return Field{}, err
	}
	return c.owner, nil
}

// walkTo follows p from n, starting at c, and returns where it ends: the
// cursor, and the node there — the interface's or the whole-decoded value's
// when p continues beneath one.
func (n *schemaNode) walkTo(c cursor, p keyPath) (cursor, *schemaNode, error) {
	for i, s := range p {
		switch n.kind {
		case nodeBranch:
			if s.kind != segField {
				return cursor{}, nil, fmt.Errorf("%w %s: %s is a struct; select one of its fields", ErrKey, p, c.path.orRoot())
			}
			f := n.byKey[s.name]
			if f == nil {
				return cursor{}, nil, fmt.Errorf("%w %s: %s has no field %q", ErrKey, p, c.path.orRoot(), s.name)
			}
			c, n = c.intoField(f), f.node
		case nodeMap:
			if s.kind != segKey && s.kind != segAny {
				return cursor{}, nil, fmt.Errorf("%w %s: %s is a map; select an entry with a quoted key", ErrKey, p, c.path.orRoot())
			}
			c, n = c.intoEntry(s, n.typ.Elem()), n.elem
		case nodeList:
			if s.kind != segIndex && s.kind != segAny {
				return cursor{}, nil, fmt.Errorf("%w %s: %s is a list; select an element by index", ErrKey, p, c.path.orRoot())
			}
			if s.kind == segIndex && n.typ.Kind() == reflect.Array && s.index >= n.typ.Len() {
				return cursor{}, nil, fmt.Errorf("%w %s: %s has %d elements", ErrKey, p, c.path.orRoot(), n.typ.Len())
			}
			c, n = c.intoEntry(s, n.typ.Elem()), n.elem
		case nodeDynamic:
			return c, n, nil // the interface's field owns everything its value holds
		default:
			if canHoldKeys(n.typ) {
				return c, n, nil // decoded whole; its field owns what is inside
			}
			return cursor{}, nil, fmt.Errorf("%w %s: %s is a %s, which has no keys beneath it", ErrKey, p, p[:i].orRoot(), typeText(n.typ))
		}
	}
	return c, n, nil
}

// canHoldKeys reports whether a value decoded whole could still have
// structure beneath it: a struct, a map or a list.
func canHoldKeys(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array:
		return true
	}
	return false
}
