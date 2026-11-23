package config

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"sync"

	"github.com/ashtonian/pkg/gowalker"
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
	view   *gowalker.Schema // nodeBranch: shared field identities and matching
	elem   *schemaNode      // nodeMap, nodeList
	// acceptsUnknown reports a nodeBranch with a koanf:",remain" field, which
	// collects the keys no other field takes: its own, or one of a struct it
	// squashes, as decoding finds it.
	acceptsUnknown bool
	// opaqueUnsafe reports a value decoded whole that could hold a func,
	// channel or unsafe pointer: a comparison looks inside it.
	opaqueUnsafe bool
}

type schemaField struct {
	reload string
	key    string
	sf     reflect.StructField // the field declaring the key
	index  []int               // from the branch's type, through squashed structs
	tagged bool                // the field, or a squashed struct holding it, is tagged sensitive:"true"
	named  bool                // its Go name looks like a credential's
	node   *schemaNode
	access *gowalker.Accessor
}

type schemaEntry struct {
	node       *schemaNode
	err        error
	misspelt   []misspeltTagKey // across the graph; a load decides which it refuses
	check      sync.Once
	validation error
	catalog    sync.Once
	fields     []Field
	variables  []Field
}

// schemaCache keeps each type's graph for as long as configView keeps its
// schema, which is every type: a program's config types are its own fixed set.
var schemaCache = gowalker.NewTypeCache(configView, buildSchema)

// metadataOf builds structural metadata without decoding defaults or examples.
// Diagnostics can therefore inspect live values even when a tag is invalid.
func metadataOf(t reflect.Type) (*schemaNode, error) {
	e := schemaMetadata(t)
	return e.node, e.err
}

func schemaMetadata(t reflect.Type) *schemaEntry {
	t = derefType(t)
	e, err := schemaCache.Get(t)
	if err != nil {
		return &schemaEntry{err: viewError(t, err)}
	}
	return e
}

// buildSchema records a type's graph and its structural errors in its entry:
// diagnostics read the graph of a type whose reload tags are invalid. The
// graph follows view's compiled shapes, so a section's node reads the same
// shape its parent's field reached, without compiling the section's type.
func buildSchema(view *gowalker.Schema) (*schemaEntry, error) {
	b := &schemaBuilder{root: view, nodes: map[*gowalker.Shape]*schemaNode{}}
	n := b.node(view.Root())
	if b.err == nil {
		b.err = checkReloadTags(view)
	}
	e := &schemaEntry{node: n, err: b.err}
	n.eachDeclaring(func(view *gowalker.Schema) {
		e.misspelt = append(e.misspelt, misspeltTagKeys(view.Root())...)
	})
	return e, nil
}

// eachDeclaring calls visit with the view of every struct n's graph reaches,
// once per node and in a fixed order: n, its fields' nodes in declaration
// order, then its element's. A type reached by several paths, or from
// within itself, is visited once: what a declaration says does not depend on
// the path that reached it.
func (n *schemaNode) eachDeclaring(visit func(*gowalker.Schema)) {
	seen := map[*schemaNode]bool{}
	var walk func(*schemaNode)
	walk = func(n *schemaNode) {
		if n == nil || seen[n] {
			return
		}
		seen[n] = true
		if n.view != nil {
			visit(n.view)
		}
		for _, f := range n.fields {
			walk(f.node)
		}
		walk(n.elem)
	}
	walk(n)
}

// rootMetadata is structural inspection without load-time tag decoding.
func rootMetadata(t reflect.Type) (*schemaNode, error) {
	n, err := metadataOf(t)
	if err != nil {
		return nil, err
	}
	if n.kind != nodeBranch {
		return nil, fmt.Errorf("%w: %s is not a struct", ErrSchema, gowalker.TypeText(t))
	}
	return n, nil
}

// schemaOf additionally validates load-time tags once per cached root.
func schemaOf(t reflect.Type) (*schemaNode, error) {
	e := schemaMetadata(t)
	if e.err != nil {
		return e.node, e.err
	}
	e.check.Do(func() {
		b := &schemaBuilder{}
		e.node.eachDeclaring(func(view *gowalker.Schema) { b.validateTags(view.Root()) })
		if b.err == nil && e.node.kind == nodeBranch {
			b.err = checkEnvTags(e.node)
		}
		e.validation = b.err
	})
	return e.node, e.validation
}

// rootSchema is schemaOf for a type that must be a struct.
func rootSchema(t reflect.Type) (*schemaNode, error) {
	n, err := schemaOf(t)
	if err != nil {
		return nil, err
	}
	if n.kind != nodeBranch {
		return nil, fmt.Errorf("%w: %s is not a struct", ErrSchema, gowalker.TypeText(t))
	}
	return n, nil
}

// schemaBuilder builds the graph of root's type from root's compiled shapes.
type schemaBuilder struct {
	root  *gowalker.Schema
	nodes map[*gowalker.Shape]*schemaNode
	err   error
}

// node is the node of shape, a shape of root's graph, past its pointers.
func (b *schemaBuilder) node(shape *gowalker.Shape) *schemaNode {
	shape = shapeBase(shape)
	if n, ok := b.nodes[shape]; ok {
		return n // a recursive type points back at its own node
	}
	t := shape.Type()
	n := &schemaNode{typ: t}
	b.nodes[shape] = n
	switch k := t.Kind(); {
	case k == reflect.Interface:
		n.kind = nodeDynamic
	case isBranch(t):
		n.kind = nodeBranch
		n.byKey = map[string]*schemaField{}
		b.fields(n, shape)
	case opaque(t):
		n.kind = nodeLeaf
	case k == reflect.Map && t.Key().Kind() == reflect.String:
		n.kind = nodeMap
		n.elem = b.node(shape.Elem())
	case k == reflect.Map:
		n.kind = nodeLeaf
		n.elem = b.node(shape.Elem()) // decoded whole for diagnostics, but element tags still apply
	case (k == reflect.Slice || k == reflect.Array) && t.Elem().Kind() != reflect.Uint8:
		n.kind = nodeList
		n.elem = b.node(shape.Elem())
	default:
		n.kind = nodeLeaf
	}
	n.opaqueUnsafe = n.kind == nodeLeaf && mayHoldUncomparable(t)
	return n
}

// fields annotates the shared view with config policy, retaining its handles
// and compiled access routes instead of rediscovering struct fields.
func (b *schemaBuilder) fields(n *schemaNode, shape *gowalker.Shape) {
	view, err := b.root.At(shape)
	if err != nil {
		b.fail(viewError(n.typ, err))
		return
	}
	n.view = view
	n.acceptsUnknown = slices.ContainsFunc(view.Root().Declarations(), func(d gowalker.Declaration) bool {
		return koanfTag(d.GoField).Has("remain")
	})
	for _, field := range view.Root().Fields() {
		sf := field.GoField()
		access, err := view.AccessField(field)
		if err != nil {
			b.fail(fmt.Errorf("%w: %w", ErrSchema, err))
			return
		}
		f := &schemaField{key: field.Name(), reload: fieldReload(field), sf: sf, index: field.Index(), tagged: fieldSensitive(field), named: nameSensitive(sf), node: b.node(field.Shape()), access: access}
		n.fields = append(n.fields, f)
		n.byKey[f.key] = f
	}
}

// validateTags checks every field the view selects, through the structs it
// squashes, shadowed ones included.
func (b *schemaBuilder) validateTags(shape *gowalker.Shape) {
	for _, d := range shape.Declarations() {
		if d.State == gowalker.Skipped {
			continue
		}
		if err := checkTags(d.DeclaringType, d.GoField); err != nil {
			b.fail(err)
		}
	}
}

// checkTags refuses a tag that cannot mean what it says: a sensitive value
// other than "true" or "false" — sensitive:"yes" would otherwise leave the
// field unprotected — a secret field opting out of redaction, a sep tag on
// anything but a list of values, a format other than json or on a value
// JSON cannot shape, an env tag no variable can serve, a default on a
// section, and a default or example its field cannot hold.
func checkTags(t reflect.Type, f reflect.StructField) error {
	if v, ok := f.Tag.Lookup("sensitive"); ok {
		switch {
		case v != "true" && v != "false":
			return fmt.Errorf("%w: field %s of %s has sensitive:%q; want \"true\" or \"false\"", ErrSchema, f.Name, gowalker.TypeText(t), v)
		case v == "false" && taggedSensitive(f.Tag):
			return fmt.Errorf("%w: field %s of %s names a secret and is tagged sensitive:\"false\"", ErrSchema, f.Name, gowalker.TypeText(t))
		}
	}
	if sep, ok := f.Tag.Lookup("sep"); ok {
		lt := derefType(f.Type)
		switch {
		case lt.Kind() != reflect.Slice || lt.Elem().Kind() == reflect.Uint8 || decodesItself(lt) || isBranch(derefType(lt.Elem())):
			return fmt.Errorf("%w: field %s of %s has a sep tag, but only a list of values is split", ErrSchema, f.Name, gowalker.TypeText(t))
		case sep == "":
			return fmt.Errorf("%w: field %s of %s has an empty sep tag; name a separator, or none", ErrSchema, f.Name, gowalker.TypeText(t))
		}
	}
	if v, ok := f.Tag.Lookup("format"); ok {
		ft := derefType(f.Type)
		_, sep := f.Tag.Lookup("sep")
		shaped := isBranch(ft) || ft.Kind() == reflect.Map || ft.Kind() == reflect.Interface ||
			(ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array) && ft.Elem().Kind() != reflect.Uint8
		switch {
		case v != "json":
			return fmt.Errorf("%w: field %s of %s has format:%q; the one format is \"json\"", ErrSchema, f.Name, gowalker.TypeText(t), v)
		case !shaped || decodesItself(ft):
			return fmt.Errorf("%w: field %s of %s has format:\"json\", which is for a section, map, list or interface", ErrSchema, f.Name, gowalker.TypeText(t))
		case sep:
			return fmt.Errorf("%w: field %s of %s has format:\"json\" and a sep tag; JSON is not split", ErrSchema, f.Name, gowalker.TypeText(t))
		}
	}
	if v, ok := f.Tag.Lookup("env"); ok {
		if !variableSetsField(f.Tag, f.Type) {
			return fmt.Errorf("%w: field %s of %s has an env tag, but no variable can set a %s", ErrSchema, f.Name, gowalker.TypeText(t), gowalker.TypeText(f.Type))
		}
		for _, name := range envTagNames(f.Tag) {
			if !envVarName.MatchString(name) {
				return fmt.Errorf("%w: field %s of %s has env:%q; %q is not a variable name", ErrSchema, f.Name, gowalker.TypeText(t), v, name)
			}
		}
	}
	if ex, ok := f.Tag.Lookup("example"); ok {
		if isBranch(derefType(f.Type)) {
			return fmt.Errorf("%w: field %s of %s has an example tag, but %s is a section; give its fields examples", ErrSchema, f.Name, gowalker.TypeText(t), f.Name)
		}
		// The value is not quoted: an example on a sensitive field may be one.
		if _, err := decodeTag(f.Type, f.Tag, ex); err != nil {
			return fmt.Errorf("%w: field %s of %s has an example that does not decode into %s", ErrSchema, f.Name, gowalker.TypeText(t), gowalker.TypeText(f.Type))
		}
	}
	def, ok := f.Tag.Lookup("default")
	switch {
	case !ok:
	case isBranch(derefType(f.Type)):
		return fmt.Errorf("%w: field %s of %s has a default tag, but %s is a section; give its fields defaults", ErrSchema, f.Name, gowalker.TypeText(t), f.Name)
	case jsonFormat(f.Tag):
		// The value is not quoted: a default on a sensitive field may be one.
		if _, err := decodeTag(f.Type, f.Tag, def); err != nil {
			return fmt.Errorf("%w: field %s of %s has a default that does not decode into %s", ErrSchema, f.Name, gowalker.TypeText(t), gowalker.TypeText(f.Type))
		}
	case textMap(derefType(f.Type)):
		if _, err := parseMapEntries(def); err != nil {
			return fmt.Errorf("%w: field %s of %s has a default that is not key=value entries: %w", ErrSchema, f.Name, gowalker.TypeText(t), err)
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
				return fmt.Errorf("%w: field %s of %s has an env tag, but %s is a key of every list element or map entry, which no one variable sets", ErrSchema, f.sf.Name, gowalker.TypeText(n.typ), at)
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
	dynamic bool  // the declared interface owns metadata beneath its dynamic value
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
		EnvRef:    f.sf.Tag.Get("envref") == "true",
		Reload:    cmp.Or(f.reload, c.owner.Reload, ReloadRestart),
		Type:      f.sf.Type,
		Sensitive: next.tagged || next.covered,
		Index:     next.index,
	}
	if c.dynamic {
		next.owner = cloneField(c.owner)
		next.owner.Sensitive = next.tagged || next.covered || c.owner.Sensitive
		next.index = nil
	}
	return next
}

// intoEntry steps into one entry of a map or list whose elements have type
// elem: s is its key, its index, or [*] for any.
func (c cursor) intoEntry(s segment, elem reflect.Type) cursor {
	next := c
	next.path = append(slices.Clip(c.path), s)
	if s.kind == segKey {
		next.covered = c.covered || isSensitiveName(s.name) && textCapable(elem)
	}
	next.pattern = c.pattern.any()
	next.index = nil
	next.owner = Field{
		Path:      next.pattern.String(),
		Type:      elem,
		Reload:    cmp.Or(c.owner.Reload, ReloadRestart),
		Sensitive: next.tagged || next.covered,
	}
	if c.dynamic {
		next.owner = cloneField(c.owner)
		next.owner.Sensitive = next.tagged || next.covered || c.owner.Sensitive
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
// config. Inspection does not decode default/example tags. A field T does not
// have, a selector of the wrong kind, an index
// past a fixed array's end, or a key that does not parse is an ErrKey error.
func FieldFor[T any](key string) (Field, error) {
	root, err := rootMetadata(reflect.TypeFor[T]())
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
			return cursor{}, nil, fmt.Errorf("%w %s: %s is a %s, which has no keys beneath it", ErrKey, p, p[:i].orRoot(), gowalker.TypeText(n.typ))
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
