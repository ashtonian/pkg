package gowalker

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type routeInner struct {
	X int
	P *int
}

type routeHidden struct{ H int }

type routeMid struct {
	*routeInner
	Inner routeInner
	Ptr   *routeInner
	PP    **routeInner
}

type routeRoot struct {
	routeMid
	*routeHidden
	M *routeMid
	N int
}

func routeRoots() []func() routeRoot {
	n := 3
	return []func() routeRoot{
		func() routeRoot { return routeRoot{} },
		func() routeRoot {
			inner := &routeInner{X: 1, P: &n}
			return routeRoot{
				routeInner:  inner,
				Ptr:         inner,
				PP:          &inner,
				Inner:       routeInner{X: 2},
				routeHidden: &routeHidden{H: 4},
				M:           &routeMid{Ptr: &routeInner{X: 5}},
				N:           6,
			}
		},
		func() routeRoot { return routeRoot{M: &routeMid{PP: new(*routeInner)}} },
	}
}

// general is a copy of a that takes the general path.
func general(a *Accessor) *Accessor {
	b := *a
	b.route = nil
	return &b
}

func sameError(t *testing.T, context string, static, dynamic error) {
	t.Helper()
	if (static == nil) != (dynamic == nil) {
		t.Fatalf("%s: static %v, general %v", context, static, dynamic)
	}
	if static == nil {
		return
	}
	s, sok := errors.AsType[*Error](static)
	d, dok := errors.AsType[*Error](dynamic)
	if !sok || !dok || s.Op != d.Op || s.Kind != d.Kind || s.Step != d.Step || s.Type != d.Type ||
		!reflect.DeepEqual(s.Path, d.Path) || static.Error() != dynamic.Error() {
		t.Fatalf("%s: static %#v, general %#v", context, static, dynamic)
	}
}

// A static route behaves as the general path does: the same values, states,
// errors and changes to the graph.
func TestStaticRoute_MatchesGeneralPath(t *testing.T) {
	e, err := New(WithJSONFields("json", NestedFields()))
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"X", "P", "Inner", "Inner.X", "Ptr", "Ptr.X", "PP.X", "PP.P", "H", "M", "M.X", "M.Ptr.X", "M.PP.X", "M.Inner.P", "N"}
	writes := [][]WriteOption{nil, {WithPointerAllocation()}, {WithPointerAllocation(), WithMapAllocation()}}
	for _, rootType := range []reflect.Type{reflect.TypeFor[routeRoot](), reflect.TypeFor[*routeRoot]()} {
		schema, err := e.CompileType(rootType)
		if err != nil {
			t.Fatal(err)
		}
		// Each root, as the schema's root type, built afresh.
		build := func(make func() routeRoot) reflect.Value {
			r := make()
			v := reflect.New(rootType).Elem()
			if rootType.Kind() == reflect.Pointer {
				v.Set(reflect.ValueOf(&r))
			} else {
				v.Set(reflect.ValueOf(r))
			}
			return v
		}
		for _, text := range paths {
			static, err := schema.ResolveText(text)
			if err != nil {
				t.Fatalf("%s: %v", text, err)
			}
			if static.route == nil {
				t.Fatalf("%v %s: no static route", rootType, text)
			}
			dynamic := general(static)
			for r, make := range routeRoots() {
				context := fmt.Sprintf("%v %s root %d", rootType, text, r)
				root := build(make)
				sv, serr := static.GetValue(root)
				dv, derr := dynamic.GetValue(root)
				sameError(t, context+" get", serr, derr)
				if sv.State != dv.State || sv.Type != dv.Type || !reflect.DeepEqual(sv.Reached, dv.Reached) {
					t.Fatalf("%s get: static %+v, general %+v", context, sv, dv)
				}
				if sv.value.IsValid() && (sv.value.CanAddr() != dv.value.CanAddr() || sv.value.CanAddr() && sv.value.UnsafeAddr() != dv.value.UnsafeAddr()) {
					t.Fatalf("%s get: different locations", context)
				}
				for w, options := range writes {
					context := fmt.Sprintf("%s options %d", context, w)
					a, b := build(make), build(make)
					input := reflect.New(static.typ).Elem()
					if input.Kind() == reflect.Int {
						input.SetInt(9)
					}
					sameError(t, context+" set", static.SetValue(a, input, options...), dynamic.SetValue(b, input, options...))
					if !reflect.DeepEqual(a.Interface(), b.Interface()) {
						t.Fatalf("%s set: graphs differ", context)
					}
					a, b = build(make), build(make)
					sl, serr := static.Locate(a, options...)
					dl, derr := dynamic.Locate(b, options...)
					sameError(t, context+" locate", serr, derr)
					if serr == nil {
						sl.Set(input)
						dl.Set(input)
					}
					if !reflect.DeepEqual(a.Interface(), b.Interface()) {
						t.Fatalf("%s locate: graphs differ", context)
					}
				}
			}
		}
	}
	// A route through an interface, a map or an index has no static route.
	type dynamicRoot struct {
		Any  any
		Map  map[string]routeInner
		List []routeInner
	}
	schema, err := e.Compile[dynamicRoot]()
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{`Any.X`, `Map["a"]`, `List[0].X`} {
		a, err := schema.ResolveText(text)
		if err != nil {
			t.Fatal(err)
		}
		if a.route != nil {
			t.Errorf("%s: a static route", text)
		}
	}
}
