package gowalker

import (
	"reflect"

	"github.com/ashtonian/pkg/gowalker/path"
)

type writeSettings struct {
	pointers bool
	maps     bool
	checked  bool
}

// WriteOption configures one write. Allocation and numeric conversion are opt-in.
// The settings pass by value, so applying options does not allocate.
type WriteOption func(writeSettings) writeSettings

// WithPointerAllocation permits creating nil intermediate pointers. A terminal
// pointer is replaced as a pointer; use path.Deref to select its pointee.
func WithPointerAllocation() WriteOption {
	return func(s writeSettings) writeSettings { s.pointers = true; return s }
}

// WithMapAllocation permits nil map allocation and creation of missing map
// intermediates. New terminal entries in existing maps need no option. A nil
// interface's dynamic type cannot be inferred, and slices never grow implicitly.
func WithMapAllocation() WriteOption {
	return func(s writeSettings) writeSettings { s.maps = true; return s }
}

// WithCheckedNumbers permits Go numeric conversions only when they preserve the
// exact finite value and signed zeros. Exact assignment remains allowed for
// non-finite values. Strings and decoder hooks are outside this option.
func WithCheckedNumbers() WriteOption {
	return func(s writeSettings) writeSettings { s.checked = true; return s }
}

// Set infers the declared root T from a non-nil *T destination and writes a
// text path using the default field view. A nil input has no declared type and
// is rejected; use a typed nil or Accessor.SetValue to provide a nil interface.
// It returns nil on success. A core error leaves storage unchanged.
func Set(root any, text string, input any, options ...WriteOption) error {
	v := reflect.ValueOf(root)
	if !v.IsValid() || v.Kind() != reflect.Pointer || v.IsNil() {
		return failure("set", ErrType, nil, emptyPath, -1, nil)
	}
	schema, err := CompileType(v.Type().Elem())
	if err != nil {
		return err
	}
	a, err := schema.ResolveText(text)
	if err != nil {
		return err
	}
	return a.Set(root, input, options...)
}

// Set writes through a non-nil *T destination for a schema of T. A schema of
// *Row therefore requires **Row. All fallible preparation finishes before the
// first mutation. Callers exclude concurrent mutation and callback side effects.
func (a *Accessor) Set(root any, input any, options ...WriteOption) error {
	v := reflect.ValueOf(root)
	if !v.IsValid() || v.Type() != reflect.PointerTo(a.schema.Type()) || v.IsNil() {
		return failure("set", ErrType, a.schema.Type(), a.path, -1, nil)
	}
	return a.SetValue(v.Elem(), reflect.ValueOf(input), options...)
}

// SetValue takes the exact declared, settable root directly. The input must be a
// valid, accessible value. A core-generated error leaves the graph unchanged,
// including any optional pointer/map allocations. Traversed references retain
// identity; assigning a terminal reference can replace it. Unaddressable
// struct/array owners are copied for writeback.
func (a *Accessor) SetValue(root, input reflect.Value, options ...WriteOption) error {
	slot, settings, err := a.target("set", root, options)
	if err != nil {
		return err
	}
	if !slot.canPut() {
		return failure("set", ErrAccess, slot.value.Type(), a.path, len(a.steps)-1, nil)
	}
	prepared, err := assignment(input, slot.value.Type(), settings.checked)
	if err != nil {
		return failure("set", err, slot.value.Type(), a.path, len(a.steps)-1, nil)
	}
	slot.put(prepared)
	if slot.flush != nil {
		slot.flush()
	}
	return nil
}

// Locate returns the settable location the accessor reaches in the exact
// declared, settable root, for a caller that writes it itself: a decoder's
// destination, or a row scanner's. It follows the path as SetValue does,
// allocating a nil pointer only with WithPointerAllocation and a nil map only
// with WithMapAllocation, and attaches what it allocated before returning. On
// an error the graph is unchanged. WithCheckedNumbers has no effect.
//
// A location a write through the result would not reach returns ErrAccess:
// a map entry, an interface's dynamic value, or a struct or array that
// SetValue would copy and write back, such as a struct held in a map or an
// interface with no pointer below it.
func (a *Accessor) Locate(root reflect.Value, options ...WriteOption) (reflect.Value, error) {
	slot, _, err := a.target("locate", root, options)
	if err != nil {
		return reflect.Value{}, err
	}
	if slot.detached || slot.key.IsValid() || !slot.value.CanSet() {
		return reflect.Value{}, failure("locate", ErrAccess, slot.value.Type(), a.path, len(a.steps)-1, nil)
	}
	if slot.flush != nil {
		slot.flush()
	}
	return slot.value, nil
}

// target follows the steps from root to the terminal slot without changing
// the graph: allocations wait in the slot's flush.
func (a *Accessor) target(op string, root reflect.Value, options []WriteOption) (writeSlot, writeSettings, error) {
	if !root.IsValid() || root.Type() != a.schema.Type() {
		return writeSlot{}, writeSettings{}, failure(op, ErrType, a.schema.Type(), a.path, -1, nil)
	}
	if !root.CanSet() {
		return writeSlot{}, writeSettings{}, failure(op, ErrAccess, root.Type(), a.path, -1, nil)
	}
	settings, err := writeOptions(options)
	if err != nil {
		return writeSlot{}, writeSettings{}, failure(op, err, root.Type(), a.path, -1, nil)
	}
	if a.route != nil {
		slot, err := a.targetStatic(op, root, settings)
		return slot, settings, err
	}
	slot := directSlot(root, nil)
	shape := a.schema.root
	for i, compiled := range a.steps {
		var err error
		if compiled.step.Kind() != path.DerefKind {
			slot, shape, err = a.followWrite(slot, shape, settings)
			if err != nil {
				return writeSlot{}, settings, failure(op, err, nil, a.path, i, nil)
			}
		}
		if shape.atomic {
			return writeSlot{}, settings, failure(op, ErrPath, shape.typ, a.path, i, nil)
		}
		if compiled.field == nil && !compiled.key.IsValid() && compiled.step.Kind() != path.DerefKind {
			_, compiled, err = compileStep(shape, compiled.step)
			if err != nil {
				return writeSlot{}, settings, failure(op, err, shape.typ, a.path, i, nil)
			}
		}
		switch compiled.step.Kind() {
		case path.FieldKind:
			for _, index := range compiled.field.index {
				slot, shape, err = a.followWrite(slot, shape, settings)
				if err != nil {
					return writeSlot{}, settings, failure(op, err, nil, a.path, i, nil)
				}
				slot, err = slot.ownedValue()
				if err != nil {
					return writeSlot{}, settings, failure(op, err, shape.typ, a.path, i, nil)
				}
				slot = slot.within(slot.value.Field(index))
				shape = shape.declared[index].shape
			}
		case path.KeyKind:
			slot, err = slot.mapEntry(compiled.key, settings.maps, i == len(a.steps)-1)
			if err != nil {
				return writeSlot{}, settings, failure(op, err, shape.typ, a.path, i, nil)
			}
			shape = shape.elem
		case path.IndexKind:
			if compiled.step.Index() >= slot.value.Len() {
				return writeSlot{}, settings, failure(op, ErrIndex, shape.typ, a.path, i, nil)
			}
			if slot.value.Kind() == reflect.Array {
				slot, err = slot.ownedValue()
				if err != nil {
					return writeSlot{}, settings, failure(op, err, shape.typ, a.path, i, nil)
				}
				slot = slot.within(slot.value.Index(compiled.step.Index()))
			} else {
				slot = directSlot(slot.value.Index(compiled.step.Index()), nil)
			}
			shape = shape.elem
		case path.DerefKind:
			slot, shape, err = a.derefWrite(slot, shape, settings)
			if err != nil {
				return writeSlot{}, settings, failure(op, err, nil, a.path, i, nil)
			}
		}
	}
	return slot, settings, nil
}

func writeOptions(options []WriteOption) (writeSettings, error) {
	if len(options) == 0 {
		return writeSettings{}, nil
	}
	s := writeSettings{}
	for _, option := range options {
		if option == nil {
			return s, ErrType
		}
		s = option(s)
	}
	return s, nil
}

// owner/key identify a direct assignment or map entry; flush attaches detached
// owners after the terminal change. Direct locations need no setter closure.
// Following an existing pointer/map/slice drops ancestor writeback: the referenced
// storage is already attached. This also avoids overwriting cyclic ancestors
// with stale copies after a descendant reference has been changed. detached
// marks storage inside such a copy: a write to it after flush is lost.
type writeSlot struct {
	value    reflect.Value
	owner    reflect.Value
	key      reflect.Value
	flush    func()
	detached bool
}

func (s writeSlot) canPut() bool { return s.owner.IsValid() }

func (s writeSlot) put(v reflect.Value) {
	if s.key.IsValid() {
		s.owner.SetMapIndex(s.key, v)
	} else {
		s.owner.Set(v)
	}
}

func directSlot(v reflect.Value, flush func()) writeSlot {
	s := writeSlot{value: v, flush: flush}
	if v.CanSet() {
		s.owner = v
	}
	return s
}

// within is the slot of v, a field or array element of s's storage.
func (s writeSlot) within(v reflect.Value) writeSlot {
	child := directSlot(v, s.flush)
	child.detached = s.detached
	return child
}

func (s writeSlot) ownedValue() (writeSlot, error) {
	if s.value.CanAddr() {
		return s, nil
	}
	if !s.canPut() || !s.value.CanInterface() {
		return writeSlot{}, ErrAccess
	}
	copy := reflect.New(s.value.Type()).Elem()
	copy.Set(s.value)
	owned := directSlot(copy, func() {
		s.put(copy)
		if s.flush != nil {
			s.flush()
		}
	})
	owned.detached = true
	return owned, nil
}

func (s writeSlot) mapEntry(key reflect.Value, allocate, terminal bool) (writeSlot, error) {
	m := s.value
	if !m.CanInterface() {
		return writeSlot{}, ErrAccess
	}
	var flush func()
	if m.IsNil() {
		if !allocate {
			return writeSlot{}, ErrNil
		}
		if !s.canPut() {
			return writeSlot{}, ErrAccess
		}
		m = reflect.MakeMap(m.Type())
		flush = func() {
			s.put(m)
			if s.flush != nil {
				s.flush()
			}
		}
	}
	v := m.MapIndex(key)
	if !v.IsValid() {
		if !terminal && !allocate {
			return writeSlot{}, ErrMissing
		}
		v = reflect.Zero(m.Type().Elem())
	}
	return writeSlot{value: v, owner: m, key: key, flush: flush}, nil
}

func (a *Accessor) followWrite(slot writeSlot, shape *Shape, settings writeSettings) (writeSlot, *Shape, error) {
	var initial [8]pointerID
	seen := initial[:0]
	for depth := 0; slot.value.Kind() == reflect.Pointer || slot.value.Kind() == reflect.Interface; depth++ {
		if depth >= a.schema.engine.settings.limits.MaxDepth {
			return writeSlot{}, nil, ErrLimit
		}
		if slot.value.Kind() == reflect.Pointer && !slot.value.IsNil() {
			id := pointerID{slot.value.Type(), slot.value.Pointer()}
			for _, previous := range seen {
				if previous == id {
					return writeSlot{}, nil, ErrPath
				}
			}
			seen = append(seen, id)
		}
		var err error
		slot, shape, err = a.derefWrite(slot, shape, settings)
		if err != nil {
			return writeSlot{}, nil, err
		}
	}
	return slot, shape, nil
}

func (a *Accessor) derefWrite(slot writeSlot, shape *Shape, settings writeSettings) (writeSlot, *Shape, error) {
	if shape.atomic {
		return writeSlot{}, nil, ErrPath
	}
	v := slot.value
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			return directSlot(v.Elem(), nil), shape.elem, nil
		}
		if !settings.pointers {
			return writeSlot{}, nil, ErrNil
		}
		if !slot.canPut() {
			return writeSlot{}, nil, ErrAccess
		}
		p := reflect.New(v.Type().Elem())
		if p.Type() != v.Type() {
			p = p.Convert(v.Type())
		}
		return directSlot(p.Elem(), func() {
			slot.put(p)
			if slot.flush != nil {
				slot.flush()
			}
		}), shape.elem, nil
	case reflect.Interface:
		if v.IsNil() {
			return writeSlot{}, nil, ErrNil
		}
		child, err := a.elementShape(shape, v.Elem())
		if err != nil {
			return writeSlot{}, nil, err
		}
		return writeSlot{value: v.Elem(), owner: slot.owner, key: slot.key, flush: slot.flush, detached: slot.detached}, child, nil
	default:
		return writeSlot{}, nil, ErrPath
	}
}
