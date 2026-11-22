package gowalker

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// FieldRule chooses a field's name and aliases, excludes it, inlines its
// struct fields, or keeps it without a name. An empty Name uses the Go name.
// Skip takes precedence. Inline accepts a struct or pointers to a struct; it
// cannot inline an atomic type. Atomic treats this field as a leaf without
// changing other uses of its type. It cannot be combined with Inline. Raw
// type relationships remain inspectable. Private fields are always excluded
// except explicitly inlined anonymous fields.
type FieldRule struct {
	// Name is canonical; empty uses the Go field name.
	Name string
	// Aliases select the same field and participate in collision checking.
	Aliases []string
	// Skip excludes the field and overrides the other rules.
	Skip bool
	// Inline exposes a struct's visible fields in the containing struct.
	Inline bool
	// Atomic makes only this field a leaf. It provides no synchronization.
	Atomic bool
	// Explicit marks Name as declared for the field, such as by its tag,
	// rather than derived from its Go name. Under [ResolveByDepth] it wins
	// over a derived name claimed at the same depth.
	Explicit bool
	// Unnamed keeps the field without a name, as a leaf: it is absent from
	// Fields, Lookup, paths and walks and claims no name, and it is listed by
	// [Shape.Declarations] with its route and tags, for a library that reads
	// it under another tag. [Schema.AccessField] reads and writes it. It
	// cannot be combined with Name, Aliases or Inline.
	Unnamed bool
}

// FieldResolver runs once per field in each reachable, non-atomic struct during
// a compilation. Concurrent cold compiles can invoke it more than once.
// A returned error is wrapped as ErrSchema. Use StructTag.Lookup to distinguish absent and empty tags.
type FieldResolver func(reflect.StructField) (FieldRule, error)

type declaredField struct {
	field    reflect.StructField
	shape    *Shape
	rule     FieldRule
	selected *Field
}

// Shape is an immutable descriptor of an exact declared type. Pointers retain
// their own descriptor. Recursive relationships refer back to existing Shapes.
// Atomic marks a traversal leaf; its declared relationships remain inspectable.
type Shape struct {
	typ          reflect.Type
	atomic       bool
	elem         *Shape
	key          *Shape
	declared     []declaredField
	fields       []*Field
	declaredView []*Field
	byName       map[string]*Field
	unnamed      []*Field
	declarations []declaration
	fold         bool
}

// declaration is a Declaration before copying; field is nil for a skipped one.
type declaration struct {
	state     DeclarationState
	field     *Field
	goField   reflect.StructField
	declaring reflect.Type
	index     []int
	parents   []reflect.StructField
}

// Type returns the exact Go type represented by this descriptor.
func (s *Shape) Type() reflect.Type { return s.typ }

// Kind returns the declared kind without dereferencing pointers.
func (s *Shape) Kind() reflect.Kind { return s.typ.Kind() }

// Atomic reports a leaf: paths and walks do not descend into it.
// Whole-value reads/writes and raw type relationships remain available.
func (s *Shape) Atomic() bool { return s.atomic }

// Elem returns the pointer or collection element shape, or nil for other kinds.
func (s *Shape) Elem() *Shape { return s.elem }

// Key returns a map's declared key shape, or nil for other kinds.
func (s *Shape) Key() *Shape { return s.key }

// Fields returns the visible fields in declaration/inline order, with no backing
// slice exposed. A pointer's fields belong to Elem, not the pointer descriptor.
func (s *Shape) Fields() []*Field { return append([]*Field(nil), s.fields...) }

// DeclaredView returns selected fields before inline expansion. Inline owners,
// including empty ones, remain available for consumer validation and annotations.
// It uses the same resolver decisions as Fields; skipped fields are absent.
func (s *Shape) DeclaredView() []*Field { return append([]*Field(nil), s.declaredView...) }

// DeclaredFields includes private and skipped fields, using raw Go tags/names.
// It describes structure, not permission to read or write a private value.
func (s *Shape) DeclaredFields() []reflect.StructField {
	fields := make([]reflect.StructField, len(s.declared))
	for i, f := range s.declared {
		fields[i] = copyStructField(f.field)
	}
	return fields
}

// DeclarationState is what a struct's view made of one declared field.
type DeclarationState uint8

const (
	// Named fields are in Fields and Lookup under their names.
	Named DeclarationState = iota + 1
	// Unnamed fields are kept without a name ([FieldRule.Unnamed]).
	Unnamed
	// InlineOwner is an inlined struct field; its fields follow it.
	InlineOwner
	// Shadowed fields lost their names to a shallower field, or at equal
	// depth to one whose name is explicit ([ResolveByDepth]).
	Shadowed
	// Tied fields were dropped with the fields they tied with ([DropTies]).
	Tied
	// Skipped fields are excluded by the rule, or private.
	Skipped
)

func (s DeclarationState) String() string {
	switch s {
	case Named:
		return "Named"
	case Unnamed:
		return "Unnamed"
	case InlineOwner:
		return "InlineOwner"
	case Shadowed:
		return "Shadowed"
	case Tied:
		return "Tied"
	case Skipped:
		return "Skipped"
	}
	return "DeclarationState(" + strconv.Itoa(int(s)) + ")"
}

// Declaration is one field a struct declares, as the view of the struct that
// owns the listing sees it.
type Declaration struct {
	// State is what the view made of the field.
	State DeclarationState
	// Field is the field's handle, or nil for a Skipped field. A Shadowed or
	// Tied field's handle reaches no accessor.
	Field *Field
	// GoField is the declaration; its Index is relative to DeclaringType.
	GoField reflect.StructField
	// DeclaringType is the struct that declares the field.
	DeclaringType reflect.Type
	// Index is the route from the listing's struct to the field.
	Index []int
	// InlineParents are the inlined fields the field is reached through,
	// outermost first.
	InlineParents []reflect.StructField
}

// Declarations lists every field the struct declares and, after each inlined
// struct field, that struct's declarations, in declaration order, with what
// the view made of each: the fields of Fields, those kept without a name,
// inline owners, shadowed and tied fields, and skipped and private fields. It
// is empty for a pointer, whose fields belong to Elem, and for a leaf. The
// result is a copy.
func (s *Shape) Declarations() []Declaration {
	result := make([]Declaration, len(s.declarations))
	for i, d := range s.declarations {
		result[i] = Declaration{
			State:         d.state,
			Field:         d.field,
			GoField:       copyStructField(d.goField),
			DeclaringType: d.declaring,
			Index:         append([]int(nil), d.index...),
			InlineParents: copyStructFields(d.parents),
		}
	}
	return result
}

// Lookup returns the field selected by a canonical name or alias, or
// (nil, false) when none matches. It uses the same equivalence as collisions.
func (s *Shape) Lookup(name string) (*Field, bool) {
	if s.fold {
		name = foldName(name)
	}
	f, ok := s.byName[name]
	return f, ok
}

// Field is a stable handle within a Shape. Its logical name can differ from its
// physical route when an ancestor is inlined. It captures no instance or CanSet.
type Field struct {
	name      string
	aliases   []string
	index     []int
	goField   reflect.StructField
	parents   []reflect.StructField
	shape     *Shape
	declaring reflect.Type
	inline    bool
	explicit  bool
}

// Name returns the canonical visible name, or "" for a field kept without a
// name ([FieldRule.Unnamed]).
func (f *Field) Name() string { return f.name }

// Type returns the field's exact declared Go type, including pointer layers.
func (f *Field) Type() reflect.Type { return f.shape.typ }

// Aliases returns a copy of the alternative names.
func (f *Field) Aliases() []string { return append([]string(nil), f.aliases...) }

// Index returns the copied physical route from the containing view,
// including any inline ancestors.
func (f *Field) Index() []int { return append([]int(nil), f.index...) }

// GoField returns the original declaration with a copied index relative to
// DeclaringType. Use Index for the complete route through inline ancestors.
func (f *Field) GoField() reflect.StructField { return copyStructField(f.goField) }

// Tags returns the original struct tags without copying field indexes. Use
// Lookup to distinguish an absent tag from a tag with an explicitly empty value.
// Gowalker does not decode or apply tag values.
func (f *Field) Tags() reflect.StructTag { return f.goField.Tag }

// Tag parses the field's key tag in the "name,options" form. ok is false when
// the field has no such tag.
func (f *Field) Tag(key string) (Tag, bool) { return LookupTag(f.goField.Tag, key) }

// Shape returns the declared field type with its field-specific leaf rule.
func (f *Field) Shape() *Shape { return f.shape }

// DeclaringType is the struct that originally declared the Go field, even when
// Fields exposes the handle through an inlined owner.
func (f *Field) DeclaringType() reflect.Type { return f.declaring }

// Inline reports a selected declaration expanded into its owner's Fields. Such
// declarations appear in DeclaredView, not in the flattened Fields result.
func (f *Field) Inline() bool { return f.inline }

// InlineParents preserves the inline owners, outermost first, for consumer
// metadata inheritance. Each field's own Index is relative to its declaring type.
func (f *Field) InlineParents() []reflect.StructField { return copyStructFields(f.parents) }

func copyStructField(f reflect.StructField) reflect.StructField {
	f.Index = append([]int(nil), f.Index...)
	return f
}

func copyStructFields(fields []reflect.StructField) []reflect.StructField {
	result := make([]reflect.StructField, len(fields))
	for i, f := range fields {
		result[i] = copyStructField(f)
	}
	return result
}

type builder struct {
	engine   *Engine
	nodes    map[reflect.Type]*Shape
	prepared map[*Shape]bool
	viewed   map[*Shape]bool
	fields   int
}

func (b *builder) countField(t reflect.Type) error {
	b.fields++
	if b.fields > b.engine.settings.limits.MaxFields {
		return failure("compile", ErrLimit, t, emptyPath, -1, nil)
	}
	return nil
}

func (b *builder) build(t reflect.Type, depth int) (*Shape, error) {
	if s := b.nodes[t]; s != nil {
		return s, nil
	}
	limits := b.engine.settings.limits
	if depth > limits.MaxDepth || len(b.nodes) >= limits.MaxTypes {
		return nil, failure("compile", ErrLimit, t, emptyPath, -1, nil)
	}
	s := &Shape{typ: t, fold: b.engine.settings.fold, atomic: b.engine.settings.isAtomic(t)}
	b.nodes[t] = s
	var err error
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		s.elem, err = b.build(t.Elem(), depth+1)
		if err != nil {
			return nil, err
		}
		if t.Kind() == reflect.Map {
			s.key, err = b.build(t.Key(), depth+1)
			if err != nil {
				return nil, err
			}
		}
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if err := b.countField(t); err != nil {
				return nil, err
			}
			f := t.Field(i)
			child, err := b.build(f.Type, depth+1)
			if err != nil {
				return nil, err
			}
			s.declared = append(s.declared, declaredField{field: f, shape: child})
		}
	}
	return s, nil
}

func (b *builder) view(s *Shape) error {
	if s.atomic || b.viewed[s] {
		return nil
	}
	if b.viewed == nil {
		b.viewed = make(map[*Shape]bool)
	}
	b.viewed[s] = true
	if s.Kind() != reflect.Struct {
		if s.elem != nil {
			return b.view(s.elem)
		}
		return nil
	}
	s.byName = make(map[string]*Field)
	if err := b.expand(s, s, nil, nil, make(map[*Shape]bool)); err != nil {
		return err
	}
	if b.engine.settings.resolvesByDepth() {
		lost, err := resolveByDepth(s, b.engine.settings.dropTies)
		if err != nil {
			return err
		}
		for i := range s.declarations {
			if state, ok := lost[s.declarations[i].field]; ok && s.declarations[i].state == Named {
				s.declarations[i].state = state
			}
		}
	}
	for _, field := range s.declaredView {
		if err := b.view(field.shape); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) prepare(s *Shape) error {
	if b.prepared[s] {
		return nil
	}
	if b.prepared == nil {
		b.prepared = make(map[*Shape]bool)
	}
	b.prepared[s] = true
	for i := range s.declared {
		field := &s.declared[i]
		if _, err := TagKeys(field.field.Tag); err != nil {
			return failure("compile", ErrSchema, s.typ, emptyPath, -1, fmt.Errorf("field %s: %w", field.field.Name, err))
		}
		rule, err := b.engine.settings.rule(field.field)
		if err != nil {
			return failure("compile", ErrSchema, s.typ, emptyPath, -1, err)
		}
		if rule.Unnamed && !rule.Skip && (rule.Name != "" || len(rule.Aliases) > 0 || rule.Inline) {
			return failure("compile", ErrSchema, s.typ, emptyPath, -1, fmt.Errorf("field %s: an unnamed field takes no name, alias or inlining", field.field.Name))
		}
		if rule.Name == "" && !rule.Unnamed {
			rule.Name = field.field.Name
		}
		rule.Aliases = append([]string(nil), rule.Aliases...)
		field.rule = rule
		if rule.Skip || !field.field.IsExported() && !(field.field.Anonymous && rule.Inline) {
			continue
		}
		if rule.Atomic || rule.Unnamed {
			if rule.Inline {
				return failure("compile", ErrSchema, s.typ, emptyPath, -1, nil)
			}
			// Leaf status belongs to this field edge, not the shared type node.
			leaf := *field.shape
			leaf.atomic = true
			leaf.fields, leaf.declaredView, leaf.byName, leaf.unnamed, leaf.declarations = nil, nil, nil, nil, nil
			field.shape = &leaf
		}
		field.selected = &Field{name: rule.Name, aliases: rule.Aliases, index: append([]int(nil), field.field.Index...), goField: field.field, shape: field.shape, declaring: s.typ, inline: rule.Inline, explicit: rule.Explicit}
		if !rule.Unnamed {
			s.declaredView = append(s.declaredView, field.selected)
		}
	}
	return nil
}

func (b *builder) expand(owner, current *Shape, index []int, parents []reflect.StructField, active map[*Shape]bool) error {
	if active[current] {
		return failure("compile", ErrSchema, current.typ, emptyPath, -1, nil)
	}
	if len(active) > b.engine.settings.limits.MaxDepth {
		return failure("compile", ErrLimit, current.typ, emptyPath, -1, nil)
	}
	active[current] = true
	defer delete(active, current)
	if err := b.prepare(current); err != nil {
		return err
	}
	for _, declared := range current.declared {
		if err := b.countField(current.typ); err != nil {
			return err
		}
		sf, rule := declared.field, declared.rule
		at := append(append([]int(nil), index...), sf.Index...)
		listed := declaration{goField: sf, declaring: current.typ, index: at, parents: parents}
		if declared.selected == nil {
			listed.state = Skipped
			owner.declarations = append(owner.declarations, listed)
			continue
		}
		if rule.Inline {
			child, err := indirectShape(declared.shape)
			if err != nil || child.atomic || child.Kind() != reflect.Struct {
				return failure("compile", ErrSchema, sf.Type, emptyPath, -1, err)
			}
			listed.state, listed.field = InlineOwner, declared.selected
			owner.declarations = append(owner.declarations, listed)
			lineage := append(append([]reflect.StructField(nil), parents...), sf)
			if err := b.expand(owner, child, at, lineage, active); err != nil {
				return err
			}
			continue
		}
		field := declared.selected
		if len(index) > 0 {
			field = &Field{name: rule.Name, aliases: rule.Aliases, index: at, goField: sf, parents: append([]reflect.StructField(nil), parents...), shape: declared.shape, declaring: current.typ, explicit: rule.Explicit}
		}
		listed.field = field
		if rule.Unnamed {
			listed.state = Unnamed
			owner.declarations = append(owner.declarations, listed)
			owner.unnamed = append(owner.unnamed, field)
			continue
		}
		listed.state = Named
		owner.declarations = append(owner.declarations, listed)
		for i, name := range append([]string{rule.Name}, rule.Aliases...) {
			if name == "" {
				return failure("compile", ErrSchema, current.typ, emptyPath, -1, nil)
			}
			reportedName := name
			name = owner.lookupKey(name)
			if previous := owner.byName[name]; previous != nil && previous != field && !b.engine.settings.resolvesByDepth() {
				firstName := previous.name
				if owner.fold {
					firstName = foldName(firstName)
				}
				conflict := &NameConflict{Owner: owner.typ, Name: reportedName, First: previous, Second: field, firstAlias: firstName != name, secondAlias: i > 0}
				return failure("compile", ErrSchema, owner.typ, emptyPath, -1, conflict)
			}
			owner.byName[name] = field
		}
		owner.fields = append(owner.fields, field)
	}
	return nil
}

// resolveByDepth keeps, for each canonical name more than one of owner's
// fields claims, the field encoding/json would serialize, and fails on a
// tie unless dropTies, which drops every field claiming the name as
// encoding/json does; then it indexes the kept fields' names and aliases,
// which must be unique. It returns the state of each field that lost its
// name: Shadowed, or Tied for a field at the tie's own rank.
func resolveByDepth(owner *Shape, dropTies bool) (map[*Field]DeclarationState, error) {
	lost := make(map[*Field]DeclarationState)
	claims := make(map[string][]*Field, len(owner.fields))
	for _, f := range owner.fields {
		key := owner.lookupKey(f.name)
		claims[key] = append(claims[key], f)
	}
	kept := owner.fields[:0]
	for _, f := range owner.fields {
		rivals := claims[owner.lookupKey(f.name)]
		if len(rivals) > 1 {
			ranked := slices.Clone(rivals)
			slices.SortStableFunc(ranked, func(x, y *Field) int {
				return cmp.Or(cmp.Compare(len(x.index), len(y.index)), compareBool(!x.explicit, !y.explicit))
			})
			if len(ranked[0].index) == len(ranked[1].index) && ranked[0].explicit == ranked[1].explicit {
				if dropTies {
					lost[f] = Shadowed
					if len(f.index) == len(ranked[0].index) && f.explicit == ranked[0].explicit {
						lost[f] = Tied
					}
					continue
				}
				first, second := ranked[0], ranked[1]
				if slices.Index(rivals, second) < slices.Index(rivals, first) {
					first, second = second, first
				}
				conflict := &NameConflict{Owner: owner.typ, Name: second.name, First: first, Second: second}
				return nil, failure("compile", ErrSchema, owner.typ, emptyPath, -1, conflict)
			}
			if ranked[0] != f {
				lost[f] = Shadowed
				continue
			}
		}
		kept = append(kept, f)
	}
	owner.fields = kept
	clear(owner.byName)
	for _, f := range kept {
		for i, name := range append([]string{f.name}, f.aliases...) {
			key := owner.lookupKey(name)
			if previous := owner.byName[key]; previous != nil && previous != f {
				conflict := &NameConflict{Owner: owner.typ, Name: name, First: previous, Second: f, firstAlias: owner.lookupKey(previous.name) != key, secondAlias: i > 0}
				return nil, failure("compile", ErrSchema, owner.typ, emptyPath, -1, conflict)
			}
			owner.byName[key] = f
		}
	}
	return lost, nil
}

func compareBool(x, y bool) int {
	switch {
	case x == y:
		return 0
	case !x:
		return -1
	}
	return 1
}

// owns reports whether field is one of the struct's own fields: named, or
// kept without a name.
func (s *Shape) owns(field *Field) bool {
	if field.name == "" {
		return slices.Contains(s.unnamed, field)
	}
	selected, ok := s.Lookup(field.name)
	return ok && selected == field
}

// lookupKey is the form of name the shape's lookup and collision checks
// compare: the name itself, or its case fold.
func (s *Shape) lookupKey(name string) string {
	if s.fold {
		return foldName(name)
	}
	return name
}

func indirectShape(s *Shape) (*Shape, error) {
	seen := make(map[*Shape]bool)
	for s.Kind() == reflect.Pointer && !s.atomic {
		if seen[s] {
			return nil, ErrPath
		}
		seen[s] = true
		s = s.elem
	}
	return s, nil
}

// EqualFold follows Unicode simple-fold orbits, which are not equivalent to
// lowercasing (for example, S and long s). Use one representative per orbit.
func foldName(name string) string {
	var result strings.Builder
	for _, r := range name {
		min := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < min {
				min = next
			}
		}
		result.WriteRune(min)
	}
	return result.String()
}
