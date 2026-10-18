package gowalker

import (
	"reflect"
	"strings"
	"unicode"
)

// FieldRule chooses a field's name and aliases, excludes it, or inlines its
// struct fields. An empty Name uses the Go name. Skip takes precedence. Inline
// accepts a struct or pointers to a struct; it cannot inline an atomic type.
// Atomic treats this field as a leaf without changing other uses of its type.
// It cannot be combined with Inline. Raw type relationships remain inspectable.
// Private fields are always excluded except explicitly inlined anonymous fields.
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
	fold         bool
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
}

// Name returns the canonical visible name.
func (f *Field) Name() string { return f.name }

// Aliases returns a copy of the alternative names.
func (f *Field) Aliases() []string { return append([]string(nil), f.aliases...) }

// Index returns the copied physical route from the containing view,
// including any inline ancestors.
func (f *Field) Index() []int { return append([]int(nil), f.index...) }

// GoField returns the original declaration with a copied index relative to
// DeclaringType. Use Index for the complete route through inline ancestors.
func (f *Field) GoField() reflect.StructField { return copyStructField(f.goField) }

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
func (f *Field) InlineParents() []reflect.StructField {
	result := make([]reflect.StructField, len(f.parents))
	for i, parent := range f.parents {
		result[i] = copyStructField(parent)
	}
	return result
}

func copyStructField(f reflect.StructField) reflect.StructField {
	f.Index = append([]int(nil), f.Index...)
	return f
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
	s := &Shape{typ: t, fold: b.engine.settings.fold}
	b.nodes[t] = s
	if b.engine.settings.atomic != nil {
		s.atomic = b.engine.settings.atomic(t)
	}
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
		rule := FieldRule{Name: field.field.Name}
		if b.engine.settings.resolver != nil {
			var err error
			rule, err = b.engine.settings.resolver(copyStructField(field.field))
			if err != nil {
				return failure("compile", ErrSchema, s.typ, emptyPath, -1, err)
			}
		}
		if rule.Name == "" {
			rule.Name = field.field.Name
		}
		rule.Aliases = append([]string(nil), rule.Aliases...)
		field.rule = rule
		if rule.Skip || !field.field.IsExported() && !(field.field.Anonymous && rule.Inline) {
			continue
		}
		if rule.Atomic {
			if rule.Inline {
				return failure("compile", ErrSchema, s.typ, emptyPath, -1, nil)
			}
			// Leaf status belongs to this field edge, not the shared type node.
			leaf := *field.shape
			leaf.atomic = true
			leaf.fields, leaf.declaredView, leaf.byName = nil, nil, nil
			field.shape = &leaf
		}
		field.selected = &Field{name: rule.Name, aliases: rule.Aliases, index: append([]int(nil), field.field.Index...), goField: field.field, shape: field.shape, declaring: s.typ, inline: rule.Inline}
		s.declaredView = append(s.declaredView, field.selected)
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
		if declared.selected == nil {
			continue
		}
		if rule.Inline {
			child, err := indirectShape(declared.shape)
			if err != nil || child.atomic || child.Kind() != reflect.Struct {
				return failure("compile", ErrSchema, sf.Type, emptyPath, -1, err)
			}
			lineage := append(append([]reflect.StructField(nil), parents...), sf)
			at := append(append([]int(nil), index...), sf.Index...)
			if err := b.expand(owner, child, at, lineage, active); err != nil {
				return err
			}
			continue
		}
		field := declared.selected
		if len(index) > 0 {
			at := append(append([]int(nil), index...), sf.Index...)
			field = &Field{name: rule.Name, aliases: rule.Aliases, index: at, goField: sf, parents: append([]reflect.StructField(nil), parents...), shape: declared.shape, declaring: current.typ}
		}
		for _, name := range append([]string{rule.Name}, rule.Aliases...) {
			if name == "" {
				return failure("compile", ErrSchema, current.typ, emptyPath, -1, nil)
			}
			reportedName := name
			if owner.fold {
				name = foldName(name)
			}
			if previous := owner.byName[name]; previous != nil && previous != field {
				return failure("compile", ErrSchema, owner.typ, emptyPath, -1, &NameConflict{Owner: owner.typ, Name: reportedName, First: previous, Second: field})
			}
			owner.byName[name] = field
		}
		owner.fields = append(owner.fields, field)
	}
	return nil
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
