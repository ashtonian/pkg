package gowalker

import (
	"reflect"
	"strconv"

	"github.com/ashtonian/pkg/gowalker/path"
)

// State distinguishes an ordinary value, a nil terminal and an unreachable value.
type State uint8

const (
	// Missing means a map entry is absent or a nil ancestor prevents access.
	Missing State = iota
	// Present includes zero values and non-nil interfaces holding typed nils.
	Present
	// Nil means the terminal itself is a nil pointer, map, slice, interface, function or channel.
	Nil
)

func (s State) String() string {
	switch s {
	case Missing:
		return "Missing"
	case Present:
		return "Present"
	case Nil:
		return "Nil"
	}
	return "State(" + strconv.Itoa(int(s)) + ")"
}

// Value is a read result. Type is the declared terminal type when known;
// DynamicType is the concrete type inside a non-nil interface, or Type otherwise.
// Reached is the consumed path prefix; it is the whole path for a reached nil.
// Maps, slices and pointers are borrowed references, not snapshots.
type Value struct {
	State       State
	Type        reflect.Type
	DynamicType reflect.Type
	Reached     path.Path
	value       reflect.Value
}

// Reflect returns the borrowed value, or an invalid value when Missing. It exposes
// ordinary reflect capabilities, potentially including mutation; it is not an
// authorization boundary. Get has checked CanInterface for reached terminals.
func (v Value) Reflect() reflect.Value { return v.value }

// Interface returns the reached value. Missing and a nil interface both return
// nil; use State to distinguish them. Typed nil pointers keep their types.
func (v Value) Interface() any {
	if !v.value.IsValid() {
		return nil
	}
	return v.value.Interface()
}

// Get reads a text path using exported Go names and the [Default] engine.
// Missing map entries and nil ancestors return State Missing with a nil error.
// Invalid fields, roots, keys or indexes return an error.
// The root's runtime type is retained, including pointers. Use Engine.Compile when
// a declared interface root or a reusable policy is needed.
func Get(root any, text string) (Value, error) {
	a, err := defaultAccessor(root, text)
	if err != nil {
		return Value{}, err
	}
	return a.Get(root)
}

func defaultAccessor(root any, text string) (*Accessor, error) {
	schema, err := CompileType(reflect.TypeOf(root))
	if err != nil {
		return nil, err
	}
	return schema.ResolveText(text)
}

// GetAs reads an assignable terminal value without decoding or numeric coercion.
// A Missing result returns ErrMissing; a compatible typed nil is valid.
func GetAs[T any](root any, text string) (T, error) {
	a, err := defaultAccessor(root, text)
	if err != nil {
		var zero T
		return zero, err
	}
	return a.GetAs[T](root)
}

// GetAs reads through a reusable accessor and returns an assignable T without
// decoding or coercion. The path and root still undergo normal runtime checks.
// A missing terminal returns ErrMissing; compatible typed nils remain valid.
// An interface terminal must be assignable to T as declared; append path.Deref
// to select its concrete contents before requesting that type.
func (a *Accessor) GetAs[T any](root any) (T, error) {
	var result T
	v, err := a.Get(root)
	if err != nil {
		return result, err
	}
	if v.State == Missing {
		return result, failure("get", ErrMissing, v.Type, a.path, v.Reached.Len(), nil)
	}
	target := reflect.TypeFor[T]()
	if !v.value.Type().AssignableTo(target) {
		return result, failure("get", ErrType, v.Type, v.Reached, v.Reached.Len()-1, nil)
	}
	if v.value.Type() == target {
		value := v.value.Interface()
		if value == nil {
			return result, nil // a reached nil interface has the zero value of T
		}
		return value.(T), nil
	}
	// Distinct named/unnamed assignable types still require Go assignment;
	// a type assertion alone would reject those valid assignments.
	var assigned T
	reflect.ValueOf(&assigned).Elem().Set(v.value)
	return assigned, nil
}

// Get accepts T or a non-nil *T for a schema of T. Exact T wins
// when T is itself a pointer. Interface roots also accept assignable values.
// Terminal pointers are preserved; path.Deref explicitly selects a pointee.
// Missing and Nil are successful results, distinguished by Value.State.
func (a *Accessor) Get(root any) (Value, error) {
	v, err := a.schema.readRoot(root, a.path, "get")
	if err != nil {
		return Value{}, err
	}
	return a.GetValue(v)
}

func (s *Schema) readRoot(root any, p path.Path, op string) (reflect.Value, error) {
	v := reflect.ValueOf(root)
	t := s.Type()
	switch {
	case !v.IsValid():
		if t.Kind() != reflect.Interface {
			return reflect.Value{}, failure(op, ErrType, t, p, -1, nil)
		}
		v = reflect.Zero(t)
	case v.Type() == t:
	case v.Type() == reflect.PointerTo(t):
		if v.IsNil() {
			return reflect.Value{}, failure(op, ErrType, t, p, -1, nil)
		}
		v = v.Elem()
	case t.Kind() == reflect.Interface && v.Type().AssignableTo(t):
		boxed := reflect.New(t).Elem()
		boxed.Set(v)
		v = boxed
	default:
		return reflect.Value{}, failure(op, ErrType, t, p, -1, nil)
	}
	return v, nil
}

// GetValue accepts the exact declared root value directly, without interpreting
// a pointer as an implicit reference to the root. An invalid or differently typed value is an error.
func (a *Accessor) GetValue(root reflect.Value) (Value, error) {
	if !root.IsValid() || root.Type() != a.schema.Type() {
		return Value{}, failure("get", ErrType, a.schema.Type(), a.path, -1, nil)
	}
	v, shape := root, a.schema.root
	for i, compiled := range a.steps {
		var missing bool
		var err error
		if compiled.step.Kind() != path.DerefKind {
			v, shape, missing, err = a.follow(v, shape)
			if err != nil {
				return Value{}, failure("get", err, nil, a.path, i, nil)
			}
			if missing {
				return a.missing(i), nil
			}
		}
		if shape.atomic {
			return Value{}, failure("get", ErrPath, shape.typ, a.path, i, nil)
		}
		if compiled.field == nil && !compiled.key.IsValid() && compiled.step.Kind() != path.DerefKind {
			_, compiled, err = compileStep(shape, compiled.step)
			if err != nil {
				return Value{}, failure("get", err, shape.typ, a.path, i, nil)
			}
		}
		switch compiled.step.Kind() {
		case path.FieldKind:
			for _, index := range compiled.field.index {
				v, shape, missing, err = a.follow(v, shape)
				if err != nil {
					return Value{}, failure("get", err, nil, a.path, i, nil)
				}
				if missing {
					return a.missing(i), nil
				}
				v = v.Field(index)
				shape = shape.declared[index].shape
			}
		case path.KeyKind:
			v = v.MapIndex(compiled.key)
			shape = shape.elem
			if !v.IsValid() {
				result := a.missing(i)
				if i == len(a.steps)-1 {
					result.Type = shape.typ
				}
				return result, nil
			}
		case path.IndexKind:
			if compiled.step.Index() >= v.Len() {
				return Value{}, failure("get", ErrIndex, shape.typ, a.path, i, nil)
			}
			v = v.Index(compiled.step.Index())
			shape = shape.elem
		case path.DerefKind:
			if v.Kind() != reflect.Pointer && v.Kind() != reflect.Interface {
				return Value{}, failure("get", ErrPath, shape.typ, a.path, i, nil)
			}
			if v.IsNil() {
				return a.missing(i), nil
			}
			v = v.Elem()
			shape, err = a.elementShape(shape, v)
			if err != nil {
				return Value{}, failure("get", err, v.Type(), a.path, i, nil)
			}
		}
	}
	if !v.CanInterface() {
		return Value{}, failure("get", ErrAccess, v.Type(), a.path, len(a.steps)-1, nil)
	}
	return reachedValue(v, a.path), nil
}

func reachedValue(v reflect.Value, p path.Path) Value {
	result := Value{State: Present, Type: v.Type(), DynamicType: v.Type(), Reached: p, value: v}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		if v.IsNil() {
			result.State = Nil
		}
	}
	if v.Kind() == reflect.Interface {
		result.DynamicType = nil
		if !v.IsNil() {
			result.DynamicType = v.Elem().Type()
		}
	}
	return result
}

func (a *Accessor) missing(consumed int) Value {
	// Of owns its copy; assemble short prefixes on the stack.
	var short [8]path.Step
	steps := short[:]
	if consumed > len(short) {
		steps = make([]path.Step, consumed)
	}
	steps = steps[:consumed]
	for i := range steps {
		steps[i] = a.steps[i].step
	}
	return Value{State: Missing, Type: a.typ, Reached: path.Of(steps...)}
}

type pointerID struct {
	typ reflect.Type
	ptr uintptr
}

func (a *Accessor) follow(v reflect.Value, shape *Shape) (reflect.Value, *Shape, bool, error) {
	var initial [8]pointerID
	seen := initial[:0]
	for depth := 0; v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface; depth++ {
		if shape.atomic {
			return reflect.Value{}, nil, false, ErrPath
		}
		if depth >= a.schema.engine.settings.limits.MaxDepth {
			return reflect.Value{}, nil, false, ErrLimit
		}
		if v.IsNil() {
			return reflect.Value{}, nil, true, nil
		}
		if v.Kind() == reflect.Pointer {
			id := pointerID{v.Type(), v.Pointer()}
			for _, previous := range seen {
				if previous == id {
					return reflect.Value{}, nil, false, ErrPath
				}
			}
			seen = append(seen, id)
		}
		v = v.Elem()
		var err error
		shape, err = a.elementShape(shape, v)
		if err != nil {
			return reflect.Value{}, nil, false, err
		}
	}
	return v, shape, false, nil
}

func (a *Accessor) elementShape(shape *Shape, v reflect.Value) (*Shape, error) {
	if shape.Kind() != reflect.Interface {
		return shape.elem, nil
	}
	schema, err := a.schema.engine.CompileType(v.Type())
	if err != nil {
		return nil, err
	}
	return schema.root, nil
}
