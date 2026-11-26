package gowalker

import (
	"cmp"
	"errors"
	"fmt"
	"iter"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// FieldRule chooses a field's name and aliases, excludes it, inlines its
// struct fields, or keeps it without a name. An empty Name uses the Go name.
// Skip takes precedence. Inline accepts a struct or pointers to a struct; it
// cannot inline an atomic type, which only [WithJSONFields] does, as
// encoding/json promotes it. Atomic treats this field as a leaf without
// changing other uses of its type. It cannot be combined with Inline. Raw
// type relationships remain inspectable. Private fields are always excluded
// except explicitly inlined anonymous fields, and the unexported embedded
// structs [WithJSONFields] names as encoding/json does.
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

	// privateEmbed selects an unexported embedded struct the rule names.
	// Reflection cannot read or write its value whole; its exported fields
	// it can. Only WithJSONFields grants it, so a resolver still cannot
	// expose a private field.
	privateEmbed bool
	// caseStrict marks a field whose json tag sets case:strict, which
	// encoding/json matches only by its exact name: Shape.Match does too.
	// Only WithJSONFields sets it.
	caseStrict bool
	// inlineLeaf lets Inline expand a struct the leaf rules make a leaf, as
	// encoding/json promotes an embedded struct whatever its methods. Only
	// WithJSONFields sets it.
	inlineLeaf bool
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
	// keys are the field's tag keys in order, which every handle and
	// declaration of the field shares and none modifies.
	keys []string
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
	// byFold indexes names and aliases by their case fold for Match, where
	// byName is exact. foldLen is the length of the longest fold Match can
	// find, so a name whose fold is longer matches nothing.
	byFold       map[string]*Field
	foldLen      int
	unnamed      []*Field
	declarations []declaration
	fold         bool
	graph        *graph
	// viewed reports a shape the view pass reached, leaves included: one a
	// path, walk or declaration can reach, rather than one only map keys or a
	// leaf's contents reach.
	viewed bool
	// conflict is the name conflict of a struct reached only aside (see
	// builder.view), in place of its view.
	conflict *NameConflict
}

// conflictError is the error compiling s alone returns for its conflict, new
// for each caller, which may change it.
func (s *Shape) conflictError() error {
	conflict := *s.conflict
	return failure("compile", ErrSchema, s.typ, emptyPath, -1, &conflict)
}

// graph is the shapes one compilation builds. It keeps one schema per shape
// for [Schema.At], so repeated calls share accessors, and the outcome of each
// search for a conflict below a shape; the shapes bound both.
type graph struct {
	mu      sync.RWMutex
	schemas map[*Shape]*Schema
	// conflicted reports a struct that keeps a conflict in place of its view;
	// without one, no shape's paths reach a conflict.
	conflicted bool
	// blocked maps a shape to the conflicted struct its paths reach first.
	blocked map[*Shape]*Shape
	// clear holds shapes whose paths reach no conflicted struct.
	clear map[*Shape]bool
}

// conflictBelow returns the first struct keeping a conflict in place of its
// view that a path from start reaches, in the order compiling start's type
// alone meets it, or nil. The caller holds g.mu for writing.
func (g *graph) conflictBelow(start *Shape) *Shape {
	if !g.conflicted {
		return nil
	}
	if blocked := g.blocked[start]; blocked != nil {
		return blocked
	}
	visited := make(map[*Shape]bool)
	var search func(*Shape) *Shape
	search = func(s *Shape) *Shape {
		if s.atomic || visited[s] || g.clear[s] {
			return nil
		}
		visited[s] = true
		switch {
		case s.conflict != nil:
			return s
		case s.Kind() == reflect.Struct:
			for _, field := range s.fields {
				if found := search(field.shape); found != nil {
					return found
				}
			}
		case s.elem != nil:
			return search(s.elem)
		}
		return nil
	}
	found := search(start)
	if found != nil {
		if g.blocked == nil {
			g.blocked = make(map[*Shape]*Shape)
		}
		g.blocked[start] = found
		return found
	}
	// A search that finds nothing has read every path from each shape it
	// visited.
	if g.clear == nil {
		g.clear = make(map[*Shape]bool)
	}
	for s := range visited {
		g.clear[s] = true
	}
	return nil
}

// declaration is a Declaration before copying; field is nil for a skipped one.
type declaration struct {
	state     DeclarationState
	field     *Field
	goField   reflect.StructField
	declaring reflect.Type
	index     []int
	parents   []reflect.StructField
	keys      []string
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
// It is empty for a struct reached only as an inline owner, or through a field
// that lost its name, whose own names conflict: the view inlining it does not
// read them, and [Schema.At] reports the conflict.
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
	// InlineOwner is an inlined struct field; its fields follow it, but for
	// a struct inlined inside itself under [ResolveByDepth], which
	// contributes none.
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

	keys []string
}

// TagKeys yields the keys of the field's tag in order, as [TagKeys] reads
// them, skipped fields included; compilation refused any tag it cannot read.
// It reads metadata the schema keeps, so ranging over it allocates nothing;
// keeping the iterator, or passing it to a function that is not inlined,
// allocates its closure. A copied Declaration yields the same keys; a
// Declaration the caller builds yields none.
func (d Declaration) TagKeys() iter.Seq[string] { return slices.Values(d.keys) }

// Declarations lists every field the struct declares and, after each inlined
// struct field, that struct's declarations, in declaration order, with what
// the view made of each: the fields of Fields, those kept without a name,
// inline owners, shadowed and tied fields, and skipped and private fields. It
// is empty for a pointer, whose fields belong to Elem, for a leaf, and where
// [Shape.Fields] is for a conflict. The result is a copy.
func (s *Shape) Declarations() []Declaration {
	result := make([]Declaration, len(s.declarations))
	for i := range s.declarations {
		result[i] = s.declarations[i].public()
	}
	return result
}

// public is the caller's copy of d.
func (d *declaration) public() Declaration {
	public := Declaration{
		State:         d.state,
		Field:         d.field,
		GoField:       copyStructField(d.goField),
		DeclaringType: d.declaring,
		Index:         append([]int(nil), d.index...),
		InlineParents: copyStructFields(d.parents),
		keys:          d.keys,
	}
	return public
}

// Lookup returns the field selected by a canonical name or alias, or
// (nil, false) when none matches. It uses the same equivalence as collisions.
func (s *Shape) Lookup(name string) (*Field, bool) {
	if !s.fold {
		f, ok := s.byName[name]
		return f, ok
	}
	var buf [foldBuffer]byte
	key, ok := appendFold(buf[:0], name, s.foldLen)
	if !ok {
		return nil, false
	}
	f, ok := s.byName[string(key)]
	return f, ok
}

// NameMatch is how a name selects a field in [Shape.Match].
type NameMatch uint8

const (
	// NoMatch means no field takes the name.
	NoMatch NameMatch = iota
	// ExactMatch means the name is the field's name or one of its aliases.
	ExactMatch
	// FoldedMatch means the name equals the field's name or an alias only
	// under Unicode simple case folding.
	FoldedMatch
)

func (m NameMatch) String() string {
	switch m {
	case NoMatch:
		return "NoMatch"
	case ExactMatch:
		return "ExactMatch"
	case FoldedMatch:
		return "FoldedMatch"
	}
	return "NameMatch(" + strconv.Itoa(int(m)) + ")"
}

// Match returns the field encoding/json decodes a member named name into, and
// how the name matched it: the field with that exact name or alias, or else,
// of the fields with a name or alias equal to it under Unicode simple case
// folding as [strings.EqualFold] compares, the shallowest, then the first in
// Fields order. A field whose json tag sets case:strict ([WithJSONFields])
// matches only exactly. Under [WithCaseFoldNames] the field is Lookup's, and
// the match is exact when name spells the field's name or an alias. A struct
// whose view is empty, and a shape that is not a struct, match nothing. Match
// allocates nothing unless one of the struct's names or aliases folds to more
// than 128 bytes.
func (s *Shape) Match(name string) (*Field, NameMatch) { return match(s, name) }

// MatchBytes is [Shape.Match] for a name held as bytes, such as a member name
// a JSON decoder has just read. It keeps no reference to name.
func (s *Shape) MatchBytes(name []byte) (*Field, NameMatch) { return match(s, name) }

func match[N string | []byte](s *Shape, name N) (*Field, NameMatch) {
	if s.fold {
		return matchFolding(s, name)
	}
	if f := s.byName[string(name)]; f != nil {
		return f, ExactMatch
	}
	var buf [foldBuffer]byte
	if key, ok := appendFold(buf[:0], name, s.foldLen); ok {
		if f := s.byFold[string(key)]; f != nil {
			return f, FoldedMatch
		}
	}
	return nil, NoMatch
}

// matchFolding is match under WithCaseFoldNames, whose byName is keyed by
// fold: no two fields' names fold alike, so the fold finds the only candidate.
func matchFolding[N string | []byte](s *Shape, name N) (*Field, NameMatch) {
	var buf [foldBuffer]byte
	key, ok := appendFold(buf[:0], name, s.foldLen)
	if !ok {
		return nil, NoMatch
	}
	f := s.byName[string(key)]
	if f == nil {
		return nil, NoMatch
	}
	if string(name) == f.name {
		return f, ExactMatch
	}
	// Compared in place: converting a []byte name to pass it to slices.Contains
	// copies it, and allocates past 32 bytes.
	if slices.ContainsFunc(f.aliases, func(alias string) bool { return string(name) == alias }) {
		return f, ExactMatch
	}
	if f.caseStrict {
		return nil, NoMatch
	}
	return f, FoldedMatch
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
	// caseStrict fields match only exactly in Shape.Match (FieldRule).
	caseStrict bool
	keys       []string
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

// TagKeys yields the keys of the field's tag in order, as [TagKeys] reads
// them; compilation refused any tag it cannot read. It reads metadata the
// schema keeps, the same its [Declaration] yields, so ranging over it
// allocates nothing; keeping the iterator, or passing it to a function that is
// not inlined, allocates its closure.
func (f *Field) TagKeys() iter.Seq[string] { return slices.Values(f.keys) }

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

// GoSelector is the field's Go selector from the struct whose view lists it:
// the names of the InlineParents it is reached through, then its own, joined
// by dots, so two promoted ID fields read as Audit.ID and Base.ID rather
// than as two IDs. Error text labels a field with it.
func (f *Field) GoSelector() string {
	if len(f.parents) == 0 {
		return f.goField.Name
	}
	var b strings.Builder
	for _, parent := range f.parents {
		b.WriteString(parent.Name)
		b.WriteByte('.')
	}
	b.WriteString(f.goField.Name)
	return b.String()
}

// UnallocatableParent returns the first of the field's InlineParents that is
// an unexported pointer. Reflection can neither allocate such a pointer nor
// point it at a copy of what it points to, so a write cannot reach the field
// in a value where the pointer is nil, as in a freshly decoded or scanned one,
// nor in a copy that must not share the parent with its original, as a
// copy-on-write patch needs; WithPointerAllocation then fails with ErrAccess.
// An unexported struct held by value is no obstacle: its exported fields are
// settable, as encoding/json sets them. ok is false when every parent can be
// allocated. It is static, so a library can refuse such a field when it builds
// its metadata rather than on the first write.
func (f *Field) UnallocatableParent() (parent reflect.StructField, ok bool) {
	return unallocatableParent(f.parents)
}

// UnallocatableParent is [Field.UnallocatableParent] for the declaration's
// InlineParents, skipped declarations included.
func (d Declaration) UnallocatableParent() (parent reflect.StructField, ok bool) {
	return unallocatableParent(d.InlineParents)
}

func unallocatableParent(parents []reflect.StructField) (reflect.StructField, bool) {
	for _, p := range parents {
		if p.Type.Kind() == reflect.Pointer && !p.IsExported() {
			return copyStructField(p), true
		}
	}
	return reflect.StructField{}, false
}

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
	graph    *graph
	nodes    map[reflect.Type]*Shape
	prepared map[*Shape]bool
	reached  map[*Shape]reach
	fields   int
}

// reach is how a compilation's view pass reaches a shape.
type reach uint8

const (
	unreached reach = iota
	// aside: only as an inline owner, or as the type of a field that lost
	// its name. No path or walk of the view that reaches it lands on it.
	aside
	// onPath: from the root through visible fields, pointers and elements.
	onPath
)

func newBuilder(e *Engine) *builder {
	return &builder{engine: e, graph: &graph{schemas: make(map[*Shape]*Schema)}, nodes: make(map[reflect.Type]*Shape), reached: make(map[*Shape]reach)}
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
	s := &Shape{typ: t, fold: b.engine.settings.fold, atomic: b.engine.settings.isAtomic(t), graph: b.graph}
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

// view gives s, and each shape its view exposes, a view. A shape on a path
// from the root must have a valid one. A struct reached only aside keeps a
// conflict among its own names in place of a view, as encoding/json writes
// the struct that inlines it without reading them; compiling it alone,
// reaching it on a path, or a path from a node At is given, reports the
// conflict. Its declared fields' types are still read aside, so every check
// a view makes, such as of tags, applies below it. Reaching a shape aside
// and then on a path checks it, and what it exposes, again.
func (b *builder) view(s *Shape, r reach) error {
	if b.reached[s] >= r {
		return nil
	}
	first := b.reached[s] == unreached
	b.reached[s] = r
	s.viewed = true
	if s.atomic {
		return nil
	}
	if s.Kind() != reflect.Struct {
		if s.elem != nil {
			return b.view(s.elem, r)
		}
		return nil
	}
	if first {
		if err := b.resolve(s); err != nil {
			var conflict *NameConflict
			if r == onPath || !errors.As(err, &conflict) || conflict.Owner != s.typ {
				return err
			}
			s.conflict = conflict
			s.fields, s.byName, s.byFold, s.unnamed, s.declarations = nil, nil, nil, nil, nil
			b.graph.conflicted = true
			// The struct's own declarations are complete however far its
			// expansion got, and their types reach everything below it.
			for _, declared := range s.declared {
				if declared.selected != nil {
					if err := b.view(declared.shape, aside); err != nil {
						return err
					}
				}
			}
			return nil
		}
	}
	if s.conflict != nil {
		if r == onPath {
			return s.conflictError()
		}
		return nil
	}
	for _, field := range s.fields {
		if err := b.view(field.shape, r); err != nil {
			return err
		}
	}
	for _, d := range s.declarations {
		if d.field != nil && d.state != Named {
			if err := b.view(d.field.shape, aside); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolve expands the struct s into its view and settles its names.
func (b *builder) resolve(s *Shape) error {
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
	s.indexFolds()
	return nil
}

// indexFolds prepares Match. Where byName is exact, it indexes the names and
// aliases of the struct's fields by their case fold, each fold going to the
// field encoding/json prefers among those that share it: it matches breadth
// first, so the shallowest, then the first in Fields order. A case:strict
// field takes no fold.
func (s *Shape) indexFolds() {
	if s.fold {
		for key := range s.byName {
			s.foldLen = max(s.foldLen, len(key))
		}
		return
	}
	fields := s.fields
	if !slices.IsSortedFunc(fields, shallower) {
		fields = slices.Clone(fields)
		slices.SortStableFunc(fields, shallower)
	}
	s.byFold = make(map[string]*Field, len(s.byName))
	var buf [foldBuffer]byte
	index := func(name string, f *Field) {
		key, _ := appendFold(buf[:0], name, math.MaxInt)
		s.foldLen = max(s.foldLen, len(key))
		if _, taken := s.byFold[string(key)]; !taken {
			s.byFold[string(key)] = f
		}
	}
	for _, f := range fields {
		if f.caseStrict {
			continue
		}
		index(f.name, f)
		for _, alias := range f.aliases {
			index(alias, f)
		}
	}
}

// shallower orders fields by the inline steps their routes take.
func shallower(a, b *Field) int { return cmp.Compare(len(a.index), len(b.index)) }

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
		keys, err := TagKeys(field.field.Tag)
		if err != nil {
			return failure("compile", ErrSchema, s.typ, emptyPath, -1, fmt.Errorf("field %s: %w", field.field.Name, err))
		}
		field.keys = keys
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
		if rule.Skip || !field.field.IsExported() && !(field.field.Anonymous && (rule.Inline || rule.privateEmbed)) {
			continue
		}
		if rule.Atomic || rule.Unnamed {
			if rule.Inline {
				return failure("compile", ErrSchema, s.typ, emptyPath, -1, fmt.Errorf("field %s: an inlined field cannot be a leaf", field.field.Name))
			}
			// Leaf status belongs to this field edge, not the shared type node.
			leaf := *field.shape
			leaf.atomic = true
			leaf.fields, leaf.declaredView, leaf.byName, leaf.byFold, leaf.unnamed, leaf.declarations = nil, nil, nil, nil, nil, nil
			leaf.viewed, leaf.conflict, leaf.foldLen = false, nil, 0
			field.shape = &leaf
		}
		field.selected = &Field{name: rule.Name, aliases: rule.Aliases, index: append([]int(nil), field.field.Index...), goField: field.field, shape: field.shape, declaring: s.typ, inline: rule.Inline, explicit: rule.Explicit, caseStrict: rule.caseStrict, keys: keys}
		if !rule.Unnamed {
			s.declaredView = append(s.declaredView, field.selected)
		}
	}
	return nil
}

func (b *builder) expand(owner, current *Shape, index []int, parents []reflect.StructField, active map[*Shape]bool) error {
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
		listed := declaration{goField: sf, declaring: current.typ, index: at, parents: parents, keys: declared.keys}
		if declared.selected == nil {
			listed.state = Skipped
			owner.declarations = append(owner.declarations, listed)
			continue
		}
		if rule.Inline {
			child, err := indirect(declared.shape, rule.inlineLeaf)
			if err != nil {
				return failure("compile", ErrSchema, sf.Type, emptyPath, -1, fmt.Errorf("field %s: inlining %s: %w", sf.Name, TypeText(sf.Type), err))
			}
			if child.atomic && !rule.inlineLeaf || child.Kind() != reflect.Struct {
				return failure("compile", ErrSchema, current.typ, emptyPath, -1, fmt.Errorf("field %s: %s is not a struct with fields to inline", sf.Name, TypeText(sf.Type)))
			}
			listed.state, listed.field = InlineOwner, declared.selected
			owner.declarations = append(owner.declarations, listed)
			if active[child] {
				// Each field of the repeat is the same field nearer the root,
				// deeper: under depth resolution it would lose its name to that
				// one, so the repeat changes no outcome. encoding/json likewise
				// explores each embedded struct once.
				if b.engine.settings.resolvesByDepth() {
					continue
				}
				return failure("compile", ErrSchema, current.typ, emptyPath, -1, fmt.Errorf("field %s: %s is inlined inside itself, and its fields would claim their names again; ResolveByDepth leaves the repeat out", sf.Name, TypeText(child.typ)))
			}
			lineage := append(append([]reflect.StructField(nil), parents...), sf)
			if err := b.expand(owner, child, at, lineage, active); err != nil {
				return err
			}
			continue
		}
		field := declared.selected
		if len(index) > 0 {
			field = &Field{name: rule.Name, aliases: rule.Aliases, index: at, goField: sf, parents: append([]reflect.StructField(nil), parents...), shape: declared.shape, declaring: current.typ, explicit: rule.Explicit, caseStrict: rule.caseStrict, keys: declared.keys}
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
				return failure("compile", ErrSchema, current.typ, emptyPath, -1, fmt.Errorf("field %s: an empty alias", sf.Name))
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
	claims := make(map[string][]*Field, len(owner.fields))
	for _, f := range owner.fields {
		key := owner.lookupKey(f.name)
		claims[key] = append(claims[key], f)
	}
	lost := make(map[*Field]DeclarationState)
	// Each contested name is settled once, at its first claim, so names are
	// settled, and the first tie reported, in declaration order.
	for _, f := range owner.fields {
		rivals := claims[owner.lookupKey(f.name)]
		if len(rivals) < 2 || rivals[0] != f {
			continue
		}
		top := dominant(rivals)
		if len(top) > 1 && !dropTies {
			conflict := &NameConflict{Owner: owner.typ, Name: top[1].name, First: top[0], Second: top[1]}
			return nil, failure("compile", ErrSchema, owner.typ, emptyPath, -1, conflict)
		}
		for _, rival := range rivals {
			lost[rival] = Shadowed
		}
		if len(top) == 1 {
			delete(lost, top[0])
			continue
		}
		for _, tied := range top {
			lost[tied] = Tied
		}
	}
	owner.fields = slices.DeleteFunc(owner.fields, func(f *Field) bool {
		_, ok := lost[f]
		return ok
	})
	clear(owner.byName)
	for _, f := range owner.fields {
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

// dominant returns the claims to one name that rank first, in declaration
// order: the shallowest, then those whose name is explicit. More than one is
// a tie.
func dominant(claims []*Field) []*Field {
	var top []*Field
	for _, f := range claims {
		if len(top) > 0 {
			switch c := cmp.Or(cmp.Compare(len(f.index), len(top[0].index)), compareBool(!f.explicit, !top[0].explicit)); {
			case c > 0:
				continue
			case c < 0:
				top = top[:0]
			}
		}
		top = append(top, f)
	}
	return top
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

func indirectShape(s *Shape) (*Shape, error) { return indirect(s, false) }

// indirect follows s's pointers, stopping at a leaf unless throughLeaves.
func indirect(s *Shape, throughLeaves bool) (*Shape, error) {
	seen := make(map[*Shape]bool)
	for s.Kind() == reflect.Pointer && (throughLeaves || !s.atomic) {
		if seen[s] {
			return nil, ErrPath
		}
		seen[s] = true
		s = s.elem
	}
	return s, nil
}

// foldName is name's case fold: see appendFold.
func foldName(name string) string {
	folded, _ := appendFold(nil, name, math.MaxInt)
	return string(folded)
}

// foldBuffer is the stack space a lookup folds a name into.
const foldBuffer = 128

// appendFold appends name with each rune replaced by the least rune of its
// Unicode simple-fold orbit, so two names fold alike exactly when
// strings.EqualFold reports them equal: an orbit, not lowercasing, relates S
// and long s, or K and the Kelvin sign. It is encoding/json's fold. Invalid
// UTF-8 folds to U+FFFD, as encoding/json decodes it. It stops, returning
// false, once more than limit bytes are appended.
func appendFold[N string | []byte](dst []byte, name N, limit int) ([]byte, bool) {
	end := math.MaxInt
	if limit < math.MaxInt-len(dst) {
		end = len(dst) + limit
	}
	for i := 0; i < len(name); {
		if c := name[i]; c < utf8.RuneSelf {
			if 'a' <= c && c <= 'z' {
				c -= 'a' - 'A'
			}
			dst = append(dst, c)
			i++
		} else {
			// At most one rune's bytes, so converting a []byte name copies
			// nothing to the heap.
			r, size := utf8.DecodeRuneInString(string(name[i:min(i+utf8.UTFMax, len(name))]))
			least := r
			for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
				least = min(least, next)
			}
			dst = utf8.AppendRune(dst, least)
			i += size
		}
		if len(dst) > end {
			return dst, false
		}
	}
	return dst, true
}
