package config

import (
	"fmt"
	"reflect"
	"slices"
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
	typ    reflect.Type // dereferenced
	kind   nodeKind
	fields []*schemaField // nodeBranch, squashed fields in place, in declaration order
	byKey  map[string]*schemaField
	elem   *schemaNode // nodeMap, nodeList
}

type schemaField struct {
	key    string
	sf     reflect.StructField // the field declaring the key
	index  []int               // from the branch's type, through squashed structs
	tagged bool                // the field, or a squashed struct holding it, is tagged sensitive:"true"
	node   *schemaNode
}

// composite reports a node whose value has keys of its own beneath it.
func (n *schemaNode) composite() bool {
	return n.kind == nodeBranch || n.kind == nodeMap || n.kind == nodeList
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
		return nil, fmt.Errorf("%w: %s is not a struct", ErrSchema, t)
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
		b.fields(n, t, nil, false, map[reflect.Type]bool{})
	case opaque(t):
		n.kind = nodeLeaf
	case k == reflect.Map && t.Key().Kind() == reflect.String:
		n.kind = nodeMap
		n.elem = b.node(t.Elem())
	case (k == reflect.Slice || k == reflect.Array) && t.Elem().Kind() != reflect.Uint8:
		n.kind = nodeList
		n.elem = b.node(t.Elem())
	default:
		n.kind = nodeLeaf
	}
	return n
}

// fields adds struct t's keys to branch n, squashed structs' keys in place.
func (b *schemaBuilder) fields(n *schemaNode, t reflect.Type, index []int, tagged bool, squashing map[reflect.Type]bool) {
	if squashing[t] {
		return
	}
	squashing[t] = true
	defer delete(squashing, t)
	for kf := range keyedFields(t) {
		at := append(slices.Clone(index), kf.field.Index...)
		fieldTagged := tagged || taggedSensitive(kf.field.Tag)
		if kf.key == "" {
			if ft := derefType(kf.field.Type); isBranch(ft) {
				b.fields(n, ft, at, fieldTagged, squashing)
			}
			continue // squash on a leaf contributes no key; decoding rejects it
		}
		switch {
		case !validFieldKey(kf.key):
			b.fail(fmt.Errorf("%w: field %s of %s has key %q, which holds a character keys use for structure", ErrSchema, kf.field.Name, t, kf.key))
			continue
		case n.byKey[kf.key] != nil:
			b.fail(fmt.Errorf("%w: fields %s and %s of %s both have key %q", ErrSchema, n.byKey[kf.key].sf.Name, kf.field.Name, n.typ, kf.key))
			continue
		}
		f := &schemaField{key: kf.key, sf: kf.field, index: at, tagged: fieldTagged, node: b.node(kf.field.Type)}
		n.fields = append(n.fields, f)
		n.byKey[kf.key] = f
	}
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
	dynamic bool  // beneath an interface: keys here are the value's, not the schema's
}

// sensitive reports whether the value at c is redacted: the rule
// PrintRedacted applies.
func (c cursor) sensitive() bool { return c.owner.Sensitive || c.tagged || c.covered }

// intoField steps into field f of a branch.
func (c cursor) intoField(f *schemaField) cursor {
	next := c
	next.path = c.path.field(f.key)
	next.tagged = c.tagged || f.tagged
	named := isSensitiveName(f.sf.Name)
	if c.dynamic {
		// A struct inside an interface value: its field names still judge it.
		next.covered = c.covered || named && f.node.kind != nodeBranch
		return next
	}
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
	if c.dynamic {
		return next
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

// intoDynamic steps into the value an interface holds: its keys belong to
// the value, so the interface's field stays their owner.
func (c cursor) intoDynamic() cursor {
	next := c
	next.dynamic = true
	next.index = nil
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
			return cursor{}, nil, fmt.Errorf("%w %s: %s is a %s, which has no keys beneath it", ErrKey, p, p[:i].orRoot(), n.typ)
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
