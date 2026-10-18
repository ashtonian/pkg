package gowalker

import (
	"reflect"
	"sort"

	"github.com/ashtonian/pkg/gowalker/path"
)

// ValueVisit describes an actual value. Pointer/interface children have an
// explicit Deref path step; fields use their canonical names. Paths can be
// resolved again after the walk. Callbacks may inspect or prune but must not
// structurally mutate the graph; collect paths, then write after walking.
type ValueVisit struct {
	Shape *Shape
	// Field is set for a struct-field visit, and nil for the root or other edges.
	Field *Field
	Value Value
	Path  path.Path
	Kind  VisitKind
	Depth int
}

// ValueVisitor inspects a visit and chooses whether traversal continues.
// Stop with no error ends the walk successfully. A returned error is wrapped
// with ErrCallback and remains reachable through errors.Is/As.
type ValueVisitor func(ValueVisit) (Control, error)

// WalkValue walks a runtime root using a short-lived default engine. A plain nil
// root has no type; use an interface schema when nil is an expected root.
func WalkValue(root any, fn ValueVisitor, options ...WalkOption) error {
	e, _ := New()
	s, err := e.CompileType(reflect.TypeOf(root))
	if err != nil {
		return err
	}
	return s.WalkValue(root, fn, options...)
}

// WalkValue uses the same root binding as Accessor.Get. Nil values are visited as
// terminals without synthesized children. Fields hidden behind nil inline owners
// are omitted. Shared references are visited at each non-cyclic path.
func (s *Schema) WalkValue(root any, fn ValueVisitor, options ...WalkOption) error {
	v, err := s.readRoot(root, emptyPath, "walkvalue")
	if err != nil {
		return err
	}
	return s.WalkValueReflect(v, fn, options...)
}

// WalkValueReflect takes the exact declared root with no implicit pointer
// unwrapping at the root.
// Returned values are borrowed. Functions/channels/unsafe pointers are leaves.
func (s *Schema) WalkValueReflect(root reflect.Value, fn ValueVisitor, options ...WalkOption) error {
	if !root.IsValid() || root.Type() != s.Type() {
		return failure("walkvalue", ErrType, s.Type(), emptyPath, -1, nil)
	}
	settings, err := walkOptions(options)
	if err != nil {
		return failure("walkvalue", err, s.Type(), emptyPath, -1, nil)
	}
	if fn == nil {
		return failure("walkvalue", ErrType, s.Type(), emptyPath, -1, nil)
	}
	w := valueWalk{schema: s, settings: settings, fn: fn, active: make(map[valueID]bool)}
	err = w.visit(root, s.root, nil, emptyPath, 0)
	if err == errWalkStopped {
		return nil
	}
	return err
}

type valueID struct {
	typ              reflect.Type
	pointer          uintptr
	length, capacity int
}

func referenceID(v reflect.Value) (valueID, bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Map:
		if !v.IsNil() {
			return valueID{typ: v.Type(), pointer: v.Pointer()}, true
		}
	case reflect.Slice:
		if v.Len() > 0 {
			return valueID{typ: v.Type(), pointer: v.Pointer(), length: v.Len(), capacity: v.Cap()}, true
		}
	}
	return valueID{}, false
}

type valueWalk struct {
	schema   *Schema
	settings walkSettings
	fn       ValueVisitor
	active   map[valueID]bool
	nodes    int
}

func (w *valueWalk) emit(v reflect.Value, shape *Shape, field *Field, p path.Path, depth int, kind VisitKind) (Control, error) {
	control, err := w.fn(ValueVisit{Shape: shape, Field: field, Value: reachedValue(v, p), Path: p, Kind: kind, Depth: depth})
	if err != nil {
		return control, failure("walkvalue", ErrCallback, shape.typ, p, p.Len()-1, err)
	}
	if err := controlError(control); err != nil {
		if err == errWalkStopped {
			return control, err
		}
		return control, failure("walkvalue", err, shape.typ, p, p.Len()-1, nil)
	}
	return control, nil
}

func (w *valueWalk) visit(v reflect.Value, shape *Shape, field *Field, p path.Path, depth int) error {
	w.nodes++
	if depth > w.settings.limits.MaxDepth || w.nodes > w.settings.limits.MaxNodes || p.Len() > w.schema.engine.settings.limits.MaxPathSteps {
		return failure("walkvalue", ErrLimit, shape.typ, p, p.Len()-1, nil)
	}
	if !v.CanInterface() {
		return failure("walkvalue", ErrAccess, v.Type(), p, p.Len()-1, nil)
	}
	id, reference := referenceID(v)
	if reference && !shape.atomic && w.active[id] {
		_, err := w.emit(v, shape, field, p, depth, Cycle)
		return err
	}
	control, err := w.emit(v, shape, field, p, depth, Enter)
	if err != nil {
		return err
	}
	if reference {
		w.active[id] = true
		defer delete(w.active, id)
	}
	if control != SkipChildren && !shape.atomic {
		if err := w.children(v, shape, p, depth); err != nil {
			return err
		}
	}
	if w.settings.leave {
		_, err = w.emit(v, shape, field, p, depth, Leave)
	}
	return err
}

func (w *valueWalk) children(v reflect.Value, shape *Shape, p path.Path, depth int) error {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		a := Accessor{schema: w.schema}
		child, err := a.elementShape(shape, v.Elem())
		if err != nil {
			return failure("walkvalue", err, v.Elem().Type(), p, p.Len()-1, nil)
		}
		return w.visit(v.Elem(), child, nil, p.Append(path.Deref()), depth+1)
	case reflect.Struct:
		for _, field := range shape.fields {
			value, missing, err := w.fieldValue(v, shape, field)
			if err != nil {
				return failure("walkvalue", err, field.shape.typ, p.Append(path.Field(field.name)), p.Len(), nil)
			}
			if missing {
				continue
			}
			if err := w.visit(value, field.shape, field, p.Append(path.Field(field.name)), depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := w.visit(v.Index(i), shape.elem, nil, p.Append(path.Index(i)), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		return w.mapValues(v, shape, p, depth)
	}
	return nil
}

func (w *valueWalk) fieldValue(v reflect.Value, shape *Shape, field *Field) (reflect.Value, bool, error) {
	a := Accessor{schema: w.schema}
	for _, index := range field.index {
		var missing bool
		var err error
		v, shape, missing, err = a.follow(v, shape)
		if err != nil || missing {
			return reflect.Value{}, missing, err
		}
		v = v.Field(index)
		shape = shape.declared[index].shape
	}
	return v, false, nil
}

func (w *valueWalk) mapValues(v reflect.Value, shape *Shape, p path.Path, depth int) error {
	if v.Len() == 0 {
		return nil
	}
	stringKeys := v.Type().Key().Kind() == reflect.String
	if !stringKeys && w.settings.less == nil && !w.settings.unordered {
		return failure("walkvalue", ErrKey, shape.typ, p, p.Len()-1, nil)
	}
	visitEntry := func(key, value reflect.Value) error {
		if !key.Comparable() || !key.Equal(key) {
			return failure("walkvalue", ErrKey, shape.typ, p, p.Len()-1, nil)
		}
		return w.visit(value, shape.elem, nil, p.Append(path.Key(key.Interface())), depth+1)
	}
	if !stringKeys && w.settings.unordered {
		iterator := v.MapRange()
		for iterator.Next() {
			if err := visitEntry(iterator.Key(), iterator.Value()); err != nil {
				return err
			}
		}
		return nil
	}
	// Bound materialized keys before allocating/sorting them. A caller can prune
	// the map node to inspect a large map without traversing its entries.
	if v.Len() > w.settings.limits.MaxNodes-w.nodes {
		return failure("walkvalue", ErrLimit, shape.typ, p, p.Len()-1, nil)
	}
	keys := v.MapKeys()
	if stringKeys {
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	} else {
		sort.SliceStable(keys, func(i, j int) bool { return w.settings.less(keys[i], keys[j]) })
	}
	for _, key := range keys {
		if err := visitEntry(key, v.MapIndex(key)); err != nil {
			return err
		}
	}
	return nil
}
