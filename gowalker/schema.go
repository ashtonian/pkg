package gowalker

import (
	"errors"
	"reflect"
	"sync"

	"github.com/ashtonian/pkg/gowalker/path"
)

var emptyPath path.Path

// Schema combines an engine's view with one exact declared root type. Handles
// support concurrent use and must not be copied after first use.
type Schema struct {
	engine      *Engine
	root        *Shape
	fieldMu     sync.RWMutex
	fieldAccess map[*Field]*Accessor
}

// Type returns the exact declared root type, including any pointer layers.
func (s *Schema) Type() reflect.Type { return s.root.typ }

// Root returns the root descriptor. For a pointer root, follow Shape.Elem
// to reach the struct whose Fields or Lookup supplies field handles.
func (s *Schema) Root() *Shape { return s.root }

// AccessField returns a reusable accessor for a visible field of this schema's
// root struct, following non-atomic root pointers. Obtain the field from that
// root's Fields or Lookup. Inline descendants use their complete physical route.
// Fields from other schemas, nested owners, or unexpanded inline declarations
// return ErrPath. Repeated calls for the same field return the same accessor;
// this bounded cache contains only the root's visible fields, never map keys.
func (s *Schema) AccessField(field *Field) (*Accessor, error) {
	owner, err := indirectShape(s.root)
	if err != nil || owner.atomic || owner.Kind() != reflect.Struct || field == nil {
		return nil, failure("resolve", ErrPath, s.Type(), emptyPath, -1, nil)
	}
	selected, ok := owner.Lookup(field.name)
	if !ok || selected != field {
		return nil, failure("resolve", ErrPath, s.Type(), emptyPath, -1, nil)
	}
	s.fieldMu.RLock()
	cached := s.fieldAccess[field]
	s.fieldMu.RUnlock()
	if cached != nil {
		return cached, nil
	}
	s.fieldMu.Lock()
	defer s.fieldMu.Unlock()
	if cached := s.fieldAccess[field]; cached != nil {
		return cached, nil
	}
	step := path.Field(field.name)
	a := &Accessor{schema: s, path: path.Of(step), steps: []accessStep{{step: step, field: field}}, typ: field.shape.typ}
	if s.fieldAccess == nil {
		s.fieldAccess = make(map[*Field]*Accessor)
	}
	s.fieldAccess[field] = a
	return a, nil
}

type accessStep struct {
	step  path.Step
	field *Field
	key   reflect.Value
}

// Accessor is an immutable resolved path. It retains keys supplied in that path,
// but no root instance. An accessor can be reused concurrently with separate values.
type Accessor struct {
	schema *Schema
	path   path.Path
	steps  []accessStep
	typ    reflect.Type
}

// Path returns the immutable resolved path. Supplied keys keep their ordinary
// Go reference semantics for the lifetime of the accessor.
func (a *Accessor) Path() path.Path { return a.path }

// Type returns the declared terminal type, or nil when a dynamic interface must
// be inspected at execution time. Get reports the actual reached type.
func (a *Accessor) Type() reflect.Type { return a.typ }

// ResolveText parses fields, quoted string keys and indexes, then calls Resolve.
// Empty text selects the root. The accessor is not cached; retain it for reuse.
func (s *Schema) ResolveText(text string) (*Accessor, error) {
	p, err := path.Parse(text)
	if err != nil {
		if errors.Is(err, path.ErrLimit) {
			return nil, failure("resolve", ErrLimit, s.Type(), emptyPath, -1, err)
		}
		return nil, failure("resolve", ErrPath, s.Type(), emptyPath, -1, err)
	}
	return s.Resolve(p)
}

// Resolve validates all statically known steps. Fields below interfaces are
// checked against the dynamic type during reads and writes. Map entries need
// not exist to resolve. The accessor is not cached; retain it for reuse.
func (s *Schema) Resolve(p path.Path) (*Accessor, error) {
	if p.Len() > s.engine.settings.limits.MaxPathSteps {
		return nil, failure("resolve", ErrLimit, s.Type(), p, -1, nil)
	}
	a := &Accessor{schema: s, path: p, steps: make([]accessStep, 0, p.Len())}
	current := s.root
	for i, step := range p.Steps() {
		compiled := accessStep{step: step}
		if step.Kind() < path.FieldKind || step.Kind() > path.DerefKind {
			return nil, failure("resolve", ErrPath, nil, p, i, nil)
		}
		if step.Kind() == path.IndexKind && step.Index() < 0 {
			return nil, failure("resolve", ErrIndex, nil, p, i, nil)
		}
		if current != nil {
			var err error
			inputType := current.typ
			current, compiled, err = compileStep(current, step)
			if err != nil {
				return nil, failure("resolve", err, inputType, p, i, nil)
			}
		}
		a.steps = append(a.steps, compiled)
	}
	if current != nil {
		a.typ = current.typ
	}
	return a, nil
}

func compileStep(s *Shape, step path.Step) (*Shape, accessStep, error) {
	compiled := accessStep{step: step}
	if step.Kind() != path.DerefKind {
		var err error
		s, err = indirectShape(s)
		if err != nil {
			return nil, compiled, err
		}
	}
	if s.atomic {
		return nil, compiled, ErrPath
	}
	if s.Kind() == reflect.Interface {
		return nil, compiled, nil
	}
	switch step.Kind() {
	case path.FieldKind:
		if s.Kind() != reflect.Struct {
			return nil, compiled, ErrPath
		}
		field, ok := s.Lookup(step.Name())
		if !ok {
			return nil, compiled, ErrPath
		}
		compiled.field = field
		return field.shape, compiled, nil
	case path.KeyKind:
		if s.Kind() != reflect.Map {
			return nil, compiled, ErrPath
		}
		key, err := mapKey(step.Key(), s.typ.Key())
		if err != nil {
			return nil, compiled, err
		}
		compiled.key = key
		return s.elem, compiled, nil
	case path.IndexKind:
		if s.Kind() != reflect.Slice && s.Kind() != reflect.Array {
			return nil, compiled, ErrPath
		}
		if s.Kind() == reflect.Array && step.Index() >= s.typ.Len() {
			return nil, compiled, ErrIndex
		}
		return s.elem, compiled, nil
	case path.DerefKind:
		if s.Kind() != reflect.Pointer {
			return nil, compiled, ErrPath
		}
		return s.elem, compiled, nil
	}
	return nil, compiled, ErrPath
}

func mapKey(key any, target reflect.Type) (reflect.Value, error) {
	v := reflect.ValueOf(key)
	if !v.IsValid() {
		if target.Kind() != reflect.Interface {
			return reflect.Value{}, ErrKey
		}
		return reflect.Zero(target), nil
	}
	if !v.Type().AssignableTo(target) || !v.Comparable() || !v.Equal(v) {
		return reflect.Value{}, ErrKey
	}
	return v, nil
}
