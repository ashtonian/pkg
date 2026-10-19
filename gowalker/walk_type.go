package gowalker

import "reflect"

// EdgeKind describes a schema relationship. A collection's ElementEdge names a
// possible element type, not a concrete map key/index or an accessor wildcard.
type EdgeKind uint8

const (
	FieldEdge EdgeKind = iota + 1
	DerefEdge
	ElementEdge
)

// TypeEdge is one step in a type route. Field is non-nil only for FieldEdge.
type TypeEdge struct {
	Kind  EdgeKind
	Field *Field
}

// TypeVisit describes a schema node. Route is a detached slice from the root;
// its length is Depth. Collection elements are possibilities, not live paths.
// References revisit a type on the active ancestor path; aliases on separate
// branches are visited independently. Map keys are metadata and are not walked.
type TypeVisit struct {
	Shape *Shape
	Route []TypeEdge
	Kind  VisitKind
	Depth int
}

// Inherited returns the value of tag key on the nearest field of this visit's
// route that declares it: the visited field itself, else its closest enclosing
// field, including the embedded owners an inlined field was promoted through.
// Use it for annotations that apply to a whole section, such as a
// sensitive:"true" or reload:"hot" tag that descendants inherit.
func (v TypeVisit) Inherited(key string) (string, bool) {
	for i := len(v.Route) - 1; i >= 0; i-- {
		field := v.Route[i].Field
		if field == nil {
			continue
		}
		if value, ok := field.goField.Tag.Lookup(key); ok {
			return value, true
		}
		for j := len(field.parents) - 1; j >= 0; j-- {
			if value, ok := field.parents[j].Tag.Lookup(key); ok {
				return value, true
			}
		}
	}
	return "", false
}

// TypeVisitor inspects a visit and chooses whether traversal continues.
// Stop with no error ends the walk successfully. A returned error is wrapped
// with ErrCallback and remains reachable through errors.Is/As.
type TypeVisitor func(TypeVisit) (Control, error)

// WalkType compiles T with the [Default] engine and walks its schema graph.
func WalkType[T any](fn TypeVisitor, options ...WalkOption) error {
	s, err := Compile[T]()
	if err != nil {
		return err
	}
	return s.WalkType(fn, options...)
}

// WalkType traverses visible fields, pointers and collection element types. An
// interface or atomic type is a leaf. Callback errors are wrapped with Unwrap.
func (s *Schema) WalkType(fn TypeVisitor, options ...WalkOption) error {
	settings, err := walkOptions(options)
	if err != nil {
		return failure("walktype", err, s.Type(), emptyPath, -1, nil)
	}
	if fn == nil {
		return failure("walktype", ErrType, s.Type(), emptyPath, -1, nil)
	}
	w := typeWalk{settings: settings, fn: fn, active: make(map[reflect.Type]bool)}
	err = w.visit(s.root, nil)
	if err == errWalkStopped {
		return nil
	}
	return err
}

type typeWalk struct {
	settings walkSettings
	fn       TypeVisitor
	active   map[reflect.Type]bool
	nodes    int
}

func (w *typeWalk) emit(shape *Shape, route []TypeEdge, kind VisitKind) (Control, error) {
	control, err := w.fn(TypeVisit{Shape: shape, Route: append([]TypeEdge(nil), route...), Kind: kind, Depth: len(route)})
	if err != nil {
		return control, typeWalkError(ErrCallback, shape, route, err)
	}
	if err := controlError(control); err != nil {
		if err == errWalkStopped {
			return control, err
		}
		return control, typeWalkError(err, shape, route, nil)
	}
	return control, nil
}

func (w *typeWalk) visit(shape *Shape, route []TypeEdge) error {
	w.nodes++
	if len(route) > w.settings.limits.MaxDepth || w.nodes > w.settings.limits.MaxNodes {
		return typeWalkError(ErrLimit, shape, route, nil)
	}
	if !shape.atomic && w.active[shape.typ] {
		_, err := w.emit(shape, route, Reference)
		return err
	}
	control, err := w.emit(shape, route, Enter)
	if err != nil {
		return err
	}
	w.active[shape.typ] = true
	defer delete(w.active, shape.typ)
	if control != SkipChildren && !shape.atomic {
		switch shape.Kind() {
		case reflect.Struct:
			for _, field := range shape.fields {
				if err := w.visit(field.shape, append(route, TypeEdge{Kind: FieldEdge, Field: field})); err != nil {
					return err
				}
			}
		case reflect.Pointer:
			if err := w.visit(shape.elem, append(route, TypeEdge{Kind: DerefEdge})); err != nil {
				return err
			}
		case reflect.Map, reflect.Slice, reflect.Array:
			if err := w.visit(shape.elem, append(route, TypeEdge{Kind: ElementEdge})); err != nil {
				return err
			}
		}
	}
	if w.settings.leave {
		_, err = w.emit(shape, route, Leave)
	}
	return err
}

func typeWalkError(kind error, shape *Shape, route []TypeEdge, cause error) error {
	return &Error{Op: "walktype", Kind: kind, Type: shape.typ, Step: -1, Cause: cause, TypeRoute: append([]TypeEdge(nil), route...)}
}
