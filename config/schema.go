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
	nodeLeaf    nodeKind = iota // decoded whole: a scalar, a self-decoding struct, a map whose keys have no text, []byte
	nodeBranch                  // a struct whose fields are keys
	nodeMap                     // a map whose entries are keys, named by their keys' text (see keyCodec)
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
	keys   *keyCodec        // nodeMap
	// remain is a nodeBranch's koanf:",remain" field — its own, or one of a
	// struct it squashes — which collects the keys no other field takes;
	// acceptsUnknown reports one. remainErr is why its remain declaration
	// cannot collect them.
	acceptsUnknown bool
	remain         *remainField
	remainErr      error
	// unkeyed is the index, from the branch's type through the structs it
	// squashes, of each exported field tagged koanf:"-": state a typed
	// section carries that no key reaches (see unkeyed.go).
	unkeyed [][]int
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

// remainField is a branch's koanf:",remain" field: a map keyed by strings,
// each of whose entries is a key directly beneath the branch. It owns each
// key it collects, as an interface field owns what its value holds, and
// the field's tags are that owner's.
type remainField struct {
	sf     reflect.StructField
	index  []int  // from the branch's type, through squashed structs
	tagged bool   // the field, or a squashed struct holding it, is tagged sensitive:"true"
	named  bool   // its Go name looks like a credential's
	reload string // its reload tag, or the innermost squashed struct's
	once   sync.Once
	elem   *schemaNode // the node of the map's element type; see node
	err    error
}

// node is the node of r's element type, what each key r collects holds. It
// is read from the type's own graph on first use: the field is not part of
// its branch's graph, and an element type holding the branch's own type
// would otherwise build that graph again within itself.
func (r *remainField) node() (*schemaNode, error) {
	r.once.Do(func() { r.elem, r.err = metadataOf(r.sf.Type.Elem()) })
	return r.elem, r.err
}

// collects reports whether n's remain field collects key segment s: n is a
// branch with one, s names a member — a field or a quoted map key — and no
// field of n reads s's name by any spelling, as decoding gives a key to a
// field before its remain field.
func (n *schemaNode) collects(s segment) bool {
	if n.remain == nil || s.kind != segField && s.kind != segKey {
		return false
	}
	f, _ := n.lookup(s.name)
	return f == nil
}

type schemaEntry struct {
	node       *schemaNode
	err        error
	misspelt   error // the graph's misspelt tag keys, refused by every checked entry point
	check      sync.Once
	validation error
	rules      sync.Once // the default validator's reading of the validate tags
	rulesErr   error
	catalog    sync.Once
	fields     []Field
	variables  []Field
}

// schemaCache keeps each type's graph for as long as configView keeps its
// schema, which is every type: a program's config types are its own fixed set.
var schemaCache = gowalker.NewTypeCache(configView, buildSchema)

// A type's declarations are checked in tiers, each entry point taking the
// one its output depends on:
//
//   - metadataOf: structural — the graph builds, keys are unique, reload
//     tags read. Raw inspection (Fields, FieldFor, KeyOf, Tree) takes only
//     this, so it can still diagnose a type whose tags are wrong.
//   - declaredOf: structural, and no tag key misspells one config reads.
//     Checked redaction and TreeFunc with a callback take this: a misspelt
//     sensitivity tag would print a secret, yet a live value prints without
//     its defaults decoding.
//   - schemaOf: declared, and every config tag's value decodes. Loading
//     and every output that shows defaults, examples or rules — usage, JSON
//     Schema, templates, environment listings — take this.
//   - loadSchema: schemaOf, and the default validator parses every rule it
//     reads on every validation and can take each of its dives.

// metadataOf builds structural metadata without decoding defaults or examples.
// Diagnostics can therefore inspect live values even when a tag is invalid.
func metadataOf(t reflect.Type) (*schemaNode, error) {
	e := schemaMetadata(t)
	return e.node, e.err
}

// declaredOf is metadataOf that also refuses misspelt tag keys.
func declaredOf(t reflect.Type) (*schemaNode, error) {
	e := schemaMetadata(t)
	if e.err != nil {
		return e.node, e.err
	}
	return e.node, e.misspelt
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
	var misspelt []misspeltTagKey
	foreign := map[reflect.Type][]string{}
	n.eachBranch(func(n *schemaNode) {
		misspelt = append(misspelt, misspeltTagKeys(n.view.Root(), foreign)...)
	})
	return &schemaEntry{node: n, err: b.err, misspelt: misspeltError(misspelt)}, nil
}

// eachBranch calls visit with every struct node n's graph reaches, once
// per node and in a fixed order: n, its fields' nodes in declaration order,
// then its element's. A type reached by several paths, or from within
// itself, is visited once: what a declaration says does not depend on the
// path that reached it.
func (n *schemaNode) eachBranch(visit func(*schemaNode)) {
	seen := map[*schemaNode]bool{}
	var walk func(*schemaNode)
	walk = func(n *schemaNode) {
		if n == nil || seen[n] {
			return
		}
		seen[n] = true
		if n.view != nil {
			visit(n)
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

// schemaOf additionally validates load-time tags once per cached root, and
// refuses misspelt tag keys.
func schemaOf(t reflect.Type) (*schemaNode, error) {
	e := schemaMetadata(t)
	if e.err != nil {
		return e.node, e.err
	}
	e.check.Do(func() {
		b := &schemaBuilder{}
		e.node.eachBranch(b.validateTags)
		if b.err == nil && e.node.kind == nodeBranch {
			b.err = checkEnvTags(e.node)
		}
		e.validation = b.err
	})
	if e.validation != nil {
		return e.node, e.validation
	}
	return e.node, e.misspelt
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
	case k == reflect.Map && keyCodecOf(t.Key()) != nil:
		n.kind = nodeMap
		n.keys = keyCodecOf(t.Key())
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
	remain, err := remainder(view.Root())
	if err != nil {
		n.remainErr = err
	} else if remain != nil {
		n.remain = &remainField{
			sf:     remain.GoField,
			index:  remain.Index,
			tagged: fieldSensitive(remain.GoField, remain.InlineParents),
			named:  nameSensitive(remain.GoField),
			reload: fieldReload(remain.GoField, remain.InlineParents),
		}
	}
	n.acceptsUnknown = n.remain != nil
	for _, d := range view.Root().Declarations() {
		if d.GoField.IsExported() && koanfTag(d.GoField).Name == "-" {
			n.unkeyed = append(n.unkeyed, d.Index)
		}
	}
	for _, field := range view.Root().Fields() {
		sf := field.GoField()
		access, err := view.AccessField(field)
		if err != nil {
			b.fail(fmt.Errorf("%w: %w", ErrSchema, err))
			return
		}
		parents := field.InlineParents()
		f := &schemaField{key: field.Name(), reload: fieldReload(sf, parents), sf: sf, index: field.Index(), tagged: fieldSensitive(sf, parents), named: nameSensitive(sf), node: b.node(field.Shape()), access: access}
		n.fields = append(n.fields, f)
		n.byKey[f.key] = f
	}
}

// remainder is the declaration of the field of shape, or of a struct it
// squashes, that a koanf:",remain" tag makes collect the keys no other field
// takes: nil when none is tagged, an ErrSchema when the tags name one
// decoding cannot fill. The decoder fills one map of strings to values it
// can set, so the field must be exported — promoted through an unexported
// embedded struct is fine — a map keyed by strings, the only one so tagged,
// and not squashed.
func remainder(shape *gowalker.Shape) (*gowalker.Declaration, error) {
	var found *gowalker.Declaration
	for _, d := range shape.Declarations() {
		tag := koanfTag(d.GoField)
		if !tag.Has("remain") {
			continue
		}
		f := d.GoField
		switch t := f.Type; {
		case found != nil:
			return nil, fmt.Errorf("%w: fields %s of %s and %s of %s are both tagged koanf:\",remain\"; decoding fills only one", ErrSchema, found.GoField.Name, gowalker.TypeText(found.DeclaringType), f.Name, gowalker.TypeText(d.DeclaringType))
		case tag.Has("squash"):
			return nil, fmt.Errorf("%w: field %s of %s is tagged both squash and remain", ErrSchema, f.Name, gowalker.TypeText(d.DeclaringType))
		case !f.IsExported():
			return nil, fmt.Errorf("%w: field %s of %s is tagged koanf:\",remain\", but decoding cannot set an unexported field; export it", ErrSchema, f.Name, gowalker.TypeText(d.DeclaringType))
		case t.Kind() != reflect.Map || t.Key().Kind() != reflect.String:
			return nil, fmt.Errorf("%w: field %s of %s is tagged koanf:\",remain\", but it is a %s; the keys it collects need a map keyed by strings", ErrSchema, f.Name, gowalker.TypeText(d.DeclaringType), gowalker.TypeText(t))
		}
		found = &d
	}
	return found, nil
}

// validateTags checks every field the view selects, through the structs it
// squashes, shadowed ones included, and the remain field n's keys reach:
// its declaration, and its sensitive tag, which the keys it collects take.
func (b *schemaBuilder) validateTags(n *schemaNode) {
	if n.remainErr != nil {
		b.fail(n.remainErr)
	}
	for _, d := range n.view.Root().Declarations() {
		var err error
		switch {
		case d.State != gowalker.Skipped:
			err = checkTags(d.DeclaringType, d.GoField)
		case koanfTag(d.GoField).Has("remain"):
			err = checkSensitiveTag(d.DeclaringType, d.GoField)
		}
		if err != nil {
			b.fail(err)
		}
	}
}

// checkSensitiveTag refuses a sensitive tag on field f of t other than
// "true" or "false" — sensitive:"yes" would otherwise leave the field
// unprotected — and a secret field opting out of redaction.
func checkSensitiveTag(t reflect.Type, f reflect.StructField) error {
	v, ok := f.Tag.Lookup("sensitive")
	switch {
	case !ok:
	case v != "true" && v != "false":
		return fmt.Errorf("%w: field %s of %s has sensitive:%q; want \"true\" or \"false\"", ErrSchema, f.Name, gowalker.TypeText(t), v)
	case v == "false" && taggedSensitive(f.Tag):
		return fmt.Errorf("%w: field %s of %s names a secret and is tagged sensitive:\"false\"", ErrSchema, f.Name, gowalker.TypeText(t))
	}
	return nil
}

// checkTags refuses a tag that cannot mean what it says: a sensitive tag
// checkSensitiveTag refuses, a sep tag on anything but a list of values, a
// format other than json or on a value JSON cannot shape, an env tag no
// variable can serve, a default on a section, and a default or example its
// field cannot hold.
func checkTags(t reflect.Type, f reflect.StructField) error {
	if err := checkSensitiveTag(t, f); err != nil {
		return err
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

// intoRemain steps into r, the koanf:",remain" field of the branch at c:
// the map whose entries are the keys no other field of the branch reads.
// Those keys sit directly beneath the branch, so the path stays the
// branch's; the pattern, and the Path of r's Field, name any key r
// collects (section[*]). The field's name covers the map, as it covers any
// map a field holds.
func (c cursor) intoRemain(r *remainField) cursor {
	next := c
	next.tagged = c.tagged || r.tagged
	next.covered = c.covered || r.named
	next.pattern = c.pattern.any()
	if c.index != nil || len(c.path) == 0 {
		next.index = append(slices.Clone(c.index), r.index...)
	}
	next.owner = Field{
		Path:      next.pattern.String(),
		Tag:       r.sf.Tag,
		Reload:    cmp.Or(r.reload, c.owner.Reload, ReloadRestart),
		Type:      r.sf.Type,
		Sensitive: next.tagged || next.covered,
		Index:     next.index,
	}
	return next
}

// intoCollected steps from the remain field at c into the key name it
// collects, whose value has type elem. The remain field stays its owner;
// a name like a credential's covers its value, as a map key's does.
func (c cursor) intoCollected(name string, elem reflect.Type) cursor {
	next := c
	next.path = c.path.member(name)
	next.covered = c.covered || isSensitiveName(name) && textCapable(elem)
	next.index = nil
	next.owner.Sensitive = next.tagged || next.covered
	return next
}

// withholdsCollected reports whether n is a branch whose remain field,
// beneath c, collects s and is sensitive: s, like every key the field
// holds, may itself be a secret, so diagnostics show no key beneath n.
func (n *schemaNode) withholdsCollected(c cursor, s segment) bool {
	return n.kind == nodeBranch && n.collects(s) && c.intoRemain(n.remain).sensitive()
}

// FieldFor returns the declared field a key of T names. A struct field's key
// returns its Field, Index included. A key inside a map or list —
// items[0].port, limits["/upload"] — returns a Field synthesized for every
// element or entry: Path is the pattern (items[*].port), Tag and Type the
// element's, Sensitive inherited from what holds it, and Index nil. A key
// beneath an interface returns the interface's field, which owns whatever
// the value holds. A key a koanf:",remain" field collects — one no other
// field of its struct reads, by any spelling — returns the remain field,
// which owns it the same way: Path is the pattern of every key it collects
// (plugins[*]), Tag, Type, Reload and Index the field's own, and Sensitive
// set when the field or what holds it is sensitive, or the key is named like
// a credential. A key beneath one walks into the remain map's element type,
// as beneath any map entry. The map keys and indexes in key need not exist
// in any config. Inspection does not decode default/example tags. A field T
// does not have, a selector of the wrong kind, an index past a fixed array's
// end, or a key that does not parse is an ErrKey error.
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
			var f *schemaField
			if s.kind == segField {
				f = n.byKey[s.name]
			}
			switch {
			case f != nil:
				c, n = c.intoField(f), f.node
			case n.collects(s):
				r := n.remain
				elem, err := r.node()
				if err != nil {
					return cursor{}, nil, err
				}
				c, n = c.intoRemain(r).intoCollected(s.name, r.sf.Type.Elem()), elem
			case s.kind != segField:
				return cursor{}, nil, fmt.Errorf("%w %s: %s is a struct; select one of its fields", ErrKey, p, c.path.orRoot())
			default:
				return cursor{}, nil, fmt.Errorf("%w %s: %s has no field %q", ErrKey, p, c.path.orRoot(), s.name)
			}
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
			if ownsBeneath(n.typ) {
				return c, n, nil // decoded whole; its field owns what lies beneath it
			}
			return cursor{}, nil, fmt.Errorf("%w %s: %s is a %s, which has no keys beneath it", ErrKey, p, p[:i].orRoot(), gowalker.TypeText(n.typ))
		}
	}
	return c, n, nil
}

// ownsBeneath reports a value decoded whole whose source can still hold
// keys beneath it: a type that decodes itself from whatever its source
// holds (mapstructure.Unmarshaler), or a map whose keys have no text (see
// keyCodec). A scalar, []byte and a value read from text, such as a
// netip.Prefix, have no keys beneath them.
func ownsBeneath(t reflect.Type) bool {
	return t.Kind() == reflect.Map || reflect.PointerTo(t).Implements(mapstructureUnmarshaler)
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
