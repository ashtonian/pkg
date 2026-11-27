package gowalker

import (
	"reflect"

	"github.com/ashtonian/pkg/gowalker/path"
)

// hop is one struct field a static route selects: the pointers to follow
// first, the field's index, and the path step it belongs to.
type hop struct {
	derefs int
	index  int
	step   int
}

// staticRoute compiles steps that select only struct fields, through pointers
// and no interface, into hops. Every type on such a route is fixed when the
// accessor resolves, so running it needs none of the per-call work a general
// path does: no shape tracking, no pointer-cycle detection (a finite chain of
// pointer types cannot revisit a pointer), no writeback copies (a settable
// root keeps every location on it addressable). A route the general path
// would refuse at a call returns nil, so that path reports the refusal.
func staticRoute(root *Shape, steps []accessStep, maxDepth int) []hop {
	count := 0
	for _, step := range steps {
		if step.step.Kind() != path.FieldKind || step.field == nil {
			return nil
		}
		count += len(step.field.index)
	}
	hops := make([]hop, 0, count)
	shape := root
	for i, step := range steps {
		for j, index := range step.field.index {
			// Past a field's first index, the route crosses structs the
			// owner's view inlines, which no leaf rule closes.
			inlined := j > 0
			derefs := 0
			for shape.Kind() == reflect.Pointer {
				if shape.atomic && !inlined || derefs >= maxDepth {
					return nil
				}
				derefs++
				shape = shape.elem
			}
			if shape.Kind() != reflect.Struct || shape.atomic && !inlined {
				return nil
			}
			hops = append(hops, hop{derefs: derefs, index: index, step: i})
			shape = shape.declared[index].shape
		}
	}
	return hops
}

// getStatic is GetValue over the accessor's static route.
func (a *Accessor) getStatic(root reflect.Value) (Value, error) {
	v := root
	for _, h := range a.route {
		for range h.derefs {
			if v.IsNil() {
				return a.missing(h.step), nil
			}
			v = v.Elem()
		}
		v = v.Field(h.index)
	}
	if !v.CanInterface() {
		return Value{}, failure("get", ErrAccess, v.Type(), a.path, len(a.steps)-1, nil)
	}
	return reachedValue(v, a.path), nil
}

type allocation struct{ owner, value reflect.Value }

// targetStatic is target over the accessor's static route, from a settable
// root. The pointers it allocates are attached by the slot's flush.
func (a *Accessor) targetStatic(op string, root reflect.Value, settings writeSettings) (writeSlot, error) {
	v := root
	var pending []allocation
	for _, h := range a.route {
		for range h.derefs {
			if v.IsNil() {
				if !settings.pointers {
					return writeSlot{}, failure(op, ErrNil, nil, a.path, h.step, nil)
				}
				if !v.CanSet() {
					return writeSlot{}, failure(op, ErrAccess, nil, a.path, h.step, nil)
				}
				p := reflect.New(v.Type().Elem())
				if p.Type() != v.Type() {
					p = p.Convert(v.Type())
				}
				pending = append(pending, allocation{owner: v, value: p})
				v = p
			}
			v = v.Elem()
		}
		v = v.Field(h.index)
	}
	slot := directSlot(v, nil)
	if len(pending) > 0 {
		// A variable the closure never sees reassigned is captured by value,
		// not moved to the heap.
		attach := pending
		slot.flush = func() {
			for _, p := range attach {
				p.owner.Set(p.value)
			}
		}
	}
	return slot, nil
}
