package gowalker_test

import (
	"errors"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/gowalker/path"
)

func textPath(v gowalker.ValueVisit) string {
	var out strings.Builder
	for _, step := range v.Path.Steps() {
		switch step.Kind() {
		case path.FieldKind:
			if out.Len() > 0 {
				out.WriteByte('.')
			}
			out.WriteString(step.Name())
		case path.KeyKind:
			out.WriteByte('[')
			out.WriteString(strconv.Quote(reflect.ValueOf(step.Key()).String()))
			out.WriteByte(']')
		case path.IndexKind:
			out.WriteString("[" + strconv.Itoa(step.Index()) + "]")
		case path.DerefKind:
			out.WriteByte('*')
		}
	}
	return out.String()
}

func typeRoute(v gowalker.TypeVisit) string {
	var out strings.Builder
	for _, edge := range v.Route {
		switch edge.Kind {
		case gowalker.FieldEdge:
			if out.Len() > 0 {
				out.WriteByte('.')
			}
			out.WriteString(edge.Field.Name())
		case gowalker.DerefEdge:
			out.WriteByte('*')
		case gowalker.ElementEdge:
			out.WriteString("[]")
		case gowalker.InlineEdge:
			if out.Len() > 0 {
				out.WriteByte('.')
			}
			out.WriteString("(" + edge.Field.GoField().Name + ")")
		}
	}
	return out.String()
}

func TestWalkTypeReferencesAndAliases(t *testing.T) {
	type node struct {
		N    int
		Next *node
	}
	type root struct {
		A, B       node
		Collection map[string]node
	}
	var enters, refs []string
	err := gowalker.WalkType[root](func(v gowalker.TypeVisit) (gowalker.Control, error) {
		if v.Depth != len(v.Route) {
			t.Fatal(v)
		}
		if v.Kind == gowalker.Reference {
			refs = append(refs, typeRoute(v))
		} else {
			enters = append(enters, typeRoute(v))
		}
		return gowalker.Continue, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(refs, []string{"A.Next*", "B.Next*", "Collection[].Next*"}) {
		t.Fatal(refs)
	}
	for _, route := range []string{"A.N", "B.N", "Collection[].N"} {
		if !slices.Contains(enters, route) {
			t.Fatal(enters)
		}
	}
	var kinds []gowalker.VisitKind
	err = gowalker.WalkType[recursivePointer](func(v gowalker.TypeVisit) (gowalker.Control, error) {
		kinds = append(kinds, v.Kind)
		return gowalker.Continue, nil
	})
	if err != nil || !reflect.DeepEqual(kinds, []gowalker.VisitKind{gowalker.Enter, gowalker.Reference}) {
		t.Fatal(kinds, err)
	}
}

func TestWalkValueCyclesAndSharedPaths(t *testing.T) {
	type node struct {
		N    int
		Next *node
	}
	shared := &node{N: 7}
	shared.Next = shared
	input := struct{ A, B *node }{A: shared, B: shared}
	var cycles, leaves []string
	err := gowalker.WalkValue(input, func(v gowalker.ValueVisit) (gowalker.Control, error) {
		if v.Kind == gowalker.Cycle {
			cycles = append(cycles, textPath(v))
		}
		if v.Shape.Type() == reflect.TypeFor[int]() {
			leaves = append(leaves, textPath(v))
		}
		return gowalker.Continue, nil
	})
	if err != nil || !reflect.DeepEqual(cycles, []string{"A*.Next", "B*.Next"}) || !reflect.DeepEqual(leaves, []string{"A*.N", "B*.N"}) {
		t.Fatal(cycles, leaves, err)
	}
	m := map[string]any{}
	m["self"] = m
	cycles = nil
	if err := gowalker.WalkValue(m, func(v gowalker.ValueVisit) (gowalker.Control, error) {
		if v.Kind == gowalker.Cycle {
			cycles = append(cycles, textPath(v))
		}
		return gowalker.Continue, nil
	}); err != nil || len(cycles) != 1 {
		t.Fatal(cycles, err)
	}
	slice := make([]any, 1)
	slice[0] = slice
	cycles = nil
	if err := gowalker.WalkValue(slice, func(v gowalker.ValueVisit) (gowalker.Control, error) {
		if v.Kind == gowalker.Cycle {
			cycles = append(cycles, textPath(v))
		}
		return gowalker.Continue, nil
	}); err != nil || len(cycles) != 1 {
		t.Fatal(cycles, err)
	}
}

func TestWalkNilAtomicAndInline(t *testing.T) {
	input := accessRoot{}
	var paths []string
	if err := gowalker.WalkValue(input, func(v gowalker.ValueVisit) (gowalker.Control, error) {
		paths = append(paths, textPath(v))
		return gowalker.Continue, nil
	}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(paths, "Child*.N") || !slices.Contains(paths, "Child") || input.Child != nil {
		t.Fatal(paths)
	}
	type row struct {
		*EmbeddedPointer `db:",inline"`
		N                int
	}
	s := schemaFor[row](t, engine(t, gowalker.WithFieldResolver(tagged("db"))))
	var count int
	if err := s.WalkValue(row{}, func(v gowalker.ValueVisit) (gowalker.Control, error) {
		count++
		if v.Value.State == gowalker.Missing {
			t.Fatal("synthesized inline value")
		}
		return gowalker.Continue, nil
	}); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	e := engine(t, gowalker.WithAtomicType(func(typ reflect.Type) bool { return typ == reflect.TypeFor[child]() }))
	atomic := schemaFor[child](t, e)
	count = 0
	if err := atomic.WalkValue(child{N: 1}, func(gowalker.ValueVisit) (gowalker.Control, error) { count++; return gowalker.Continue, nil }); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	count = 0
	if err := atomic.WalkType(func(gowalker.TypeVisit) (gowalker.Control, error) { count++; return gowalker.Continue, nil }); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestWalkControlsAndLeaveEvents(t *testing.T) {
	input := struct {
		A child
		B int
	}{}
	var events []string
	err := gowalker.WalkValue(input, func(v gowalker.ValueVisit) (gowalker.Control, error) {
		events = append(events, strconv.Itoa(int(v.Kind))+":"+textPath(v))
		if textPath(v) == "A" {
			return gowalker.SkipChildren, nil
		}
		return gowalker.Continue, nil
	}, gowalker.WithLeaveEvents())
	if err != nil || !reflect.DeepEqual(events, []string{"0:", "0:A", "1:A", "0:B", "1:B", "1:"}) {
		t.Fatal(events, err)
	}
	var types []gowalker.VisitKind
	if err := gowalker.WalkType[child](func(v gowalker.TypeVisit) (gowalker.Control, error) {
		types = append(types, v.Kind)
		return gowalker.SkipChildren, nil
	}, gowalker.WithLeaveEvents()); err != nil || !reflect.DeepEqual(types, []gowalker.VisitKind{gowalker.Enter, gowalker.Leave}) {
		t.Fatal(types, err)
	}
	count := 0
	if err := gowalker.WalkType[child](func(gowalker.TypeVisit) (gowalker.Control, error) { count++; return gowalker.Stop, nil }, gowalker.WithLeaveEvents()); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	count = 0
	if err := gowalker.WalkValue(input, func(gowalker.ValueVisit) (gowalker.Control, error) { count++; return gowalker.Stop, nil }, gowalker.WithLeaveEvents()); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	cause := errors.New("consumer callback failed")
	if err := gowalker.WalkValue(input, func(gowalker.ValueVisit) (gowalker.Control, error) { return gowalker.Stop, cause }); !errors.Is(err, cause) || !errors.Is(err, gowalker.ErrCallback) {
		t.Fatal(err)
	}
	if err := gowalker.WalkType[child](func(gowalker.TypeVisit) (gowalker.Control, error) { return gowalker.Continue, cause }); !errors.Is(err, cause) || !errors.Is(err, gowalker.ErrCallback) {
		t.Fatal(err)
	}
	if err := gowalker.WalkType[child](func(gowalker.TypeVisit) (gowalker.Control, error) { return gowalker.Control(255), nil }); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
}

func TestWalkMapsOrderingAndUnsupportedKeys(t *testing.T) {
	input := map[string]int{"z": 1, "a": 2, "m": 3}
	var keys []string
	if err := gowalker.WalkValue(input, func(v gowalker.ValueVisit) (gowalker.Control, error) {
		if v.Depth == 1 {
			keys = append(keys, v.Path.Steps()[0].Key().(string))
		}
		return gowalker.Continue, nil
	}); err != nil || !reflect.DeepEqual(keys, []string{"a", "m", "z"}) {
		t.Fatal(keys, err)
	}
	numbers := map[int]int{3: 3, 1: 1, 2: 2}
	visitor := func(gowalker.ValueVisit) (gowalker.Control, error) { return gowalker.Continue, nil }
	if err := gowalker.WalkValue(numbers, visitor); !errors.Is(err, gowalker.ErrKey) {
		t.Fatal(err)
	}
	var indexes []int
	if err := gowalker.WalkValue(numbers, func(v gowalker.ValueVisit) (gowalker.Control, error) {
		if v.Depth == 1 {
			indexes = append(indexes, v.Path.Steps()[0].Key().(int))
		}
		return gowalker.Continue, nil
	}, gowalker.WithMapKeyOrder(func(a, b reflect.Value) bool { return a.Int() < b.Int() })); err != nil || !reflect.DeepEqual(indexes, []int{1, 2, 3}) {
		t.Fatal(indexes, err)
	}
	if err := gowalker.WalkValue(numbers, visitor, gowalker.WithUnorderedMaps()); err != nil {
		t.Fatal(err)
	}
	nanMap := map[float64]int{math.NaN(): 1}
	if err := gowalker.WalkValue(nanMap, visitor, gowalker.WithUnorderedMaps()); !errors.Is(err, gowalker.ErrKey) {
		t.Fatal(err)
	}
	if err := gowalker.WalkValue(numbers, visitor, gowalker.WithMapKeyOrder(nil)); !errors.Is(err, gowalker.ErrKey) {
		t.Fatal(err)
	}
	if err := gowalker.WalkValue(numbers, visitor, gowalker.WithUnorderedMaps(), gowalker.WithMapKeyOrder(func(a, b reflect.Value) bool { return false })); !errors.Is(err, gowalker.ErrKey) {
		t.Fatal(err)
	}
	type key string
	s := schemaFor[map[key]int](t, engine(t))
	if err := s.WalkValue(map[key]int{"named": 1}, func(v gowalker.ValueVisit) (gowalker.Control, error) {
		if v.Depth == 1 {
			if _, ok := v.Path.Steps()[0].Key().(key); !ok {
				t.Fatal("key type erased")
			}
			a, err := s.Resolve(v.Path)
			if err != nil {
				return gowalker.Stop, err
			}
			if value, err := a.Get(map[key]int{"named": 1}); err != nil || value.Interface() != 1 {
				t.Fatal(value, err)
			}
		}
		return gowalker.Continue, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWalkLimitsAndRootChecks(t *testing.T) {
	visitor := func(gowalker.ValueVisit) (gowalker.Control, error) { return gowalker.Continue, nil }
	limit := gowalker.WithWalkLimits(gowalker.WalkLimits{MaxDepth: 0, MaxNodes: 10})
	if err := gowalker.WalkValue(child{}, visitor, limit); !errors.Is(err, gowalker.ErrLimit) {
		t.Fatal(err)
	}
	var detail *gowalker.Error
	err := gowalker.WalkType[child](func(gowalker.TypeVisit) (gowalker.Control, error) { return gowalker.Continue, nil }, limit)
	if !errors.As(err, &detail) || len(detail.TypeRoute) != 1 || detail.TypeRoute[0].Field.Name() != "N" {
		t.Fatal(err)
	}
	limit = gowalker.WithWalkLimits(gowalker.WalkLimits{MaxDepth: 10, MaxNodes: 1})
	if err := gowalker.WalkValue(map[string]int{"key": 1}, visitor, limit); !errors.Is(err, gowalker.ErrLimit) {
		t.Fatal(err)
	}
	if err := gowalker.WalkValue(child{}, visitor, gowalker.WithWalkLimits(gowalker.WalkLimits{})); !errors.Is(err, gowalker.ErrLimit) {
		t.Fatal(err)
	}
	if err := gowalker.WalkValue(child{}, visitor, nil); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	if err := gowalker.WalkValue(child{}, nil); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	if err := gowalker.WalkValue(nil, visitor); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	s := schemaFor[child](t, engine(t))
	if err := s.WalkValue(1, visitor); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	if err := s.WalkValueReflect(reflect.Value{}, visitor); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	if err := s.WalkValueReflect(reflect.ValueOf(child{}), visitor); err != nil {
		t.Fatal(err)
	}
	if err := schemaFor[any](t, engine(t)).WalkValue(nil, visitor); err != nil {
		t.Fatal(err)
	}
	limits := gowalker.DefaultLimits()
	limits.MaxPathSteps = 1
	s = schemaFor[child](t, engine(t, gowalker.WithLimits(limits)))
	if err := s.WalkValue(&child{}, visitor, gowalker.WithWalkLimits(gowalker.WalkLimits{MaxDepth: 8, MaxNodes: 10})); err != nil {
		t.Fatal(err)
	}
	pointerSchema := schemaFor[*child](t, engine(t, gowalker.WithLimits(limits)))
	if err := pointerSchema.WalkValue(&child{}, visitor, gowalker.WithWalkLimits(gowalker.WalkLimits{MaxDepth: 8, MaxNodes: 10})); !errors.Is(err, gowalker.ErrLimit) {
		t.Fatal(err)
	}
	if err := gowalker.WalkValue(map[int]int{1: 2}, func(gowalker.ValueVisit) (gowalker.Control, error) { return gowalker.SkipChildren, nil }, gowalker.WithWalkLimits(gowalker.WalkLimits{MaxDepth: 0, MaxNodes: 1})); err != nil {
		t.Fatal(err)
	}
	type chain struct{ Next *chain }
	root := &chain{}
	current := root
	for range 130 {
		current.Next = &chain{}
		current = current.Next
	}
	if err := gowalker.WalkValue(root, visitor); !errors.Is(err, gowalker.ErrLimit) {
		t.Fatal("default walk was not bounded", err)
	}
}

func TestWalkPathsResolveAndTypeRoutesAreDetached(t *testing.T) {
	input := accessRoot{Child: &child{N: 2}, Rows: map[string]child{"one": {N: 3}}, Items: []child{{N: 4}}, Dynamic: child{N: 5}}
	s := schemaFor[accessRoot](t, engine(t))
	var count int
	if err := s.WalkValue(&input, func(v gowalker.ValueVisit) (gowalker.Control, error) {
		a, err := s.Resolve(v.Path)
		if err != nil {
			return gowalker.Stop, err
		}
		read, err := a.Get(&input)
		if err != nil || read.Type != v.Value.Type || !reflect.DeepEqual(read.Interface(), v.Value.Interface()) {
			t.Fatal(read, v, err)
		}
		count++
		return gowalker.Continue, nil
	}); err != nil || count == 0 {
		t.Fatal(count, err)
	}
	if err := s.WalkType(func(v gowalker.TypeVisit) (gowalker.Control, error) {
		if len(v.Route) > 0 {
			v.Route[0] = gowalker.TypeEdge{}
		}
		return gowalker.Continue, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WalkType(func(v gowalker.TypeVisit) (gowalker.Control, error) {
		for _, edge := range v.Route {
			if edge.Kind == 0 {
				t.Fatal("route backing slice exposed")
			}
		}
		return gowalker.Continue, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWalkConcurrentSchemaReuse(t *testing.T) {
	s := schemaFor[accessRoot](t, engine(t))
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			if err := s.WalkValue(accessRoot{Dynamic: &child{N: 1}}, func(gowalker.ValueVisit) (gowalker.Control, error) { return gowalker.Continue, nil }); err != nil {
				t.Error(err)
			}
			if err := s.WalkType(func(gowalker.TypeVisit) (gowalker.Control, error) { return gowalker.Continue, nil }); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
}

type dwEmpty struct{ _ int }

type dwShared struct{ Port int }

type dwInner struct {
	Shared dwShared `cfg:"shared"`
}

type dwNode struct {
	Next    *dwNode `cfg:"next"`
	dwEmpty `cfg:",inline"`
}

type dwConfig struct {
	dwEmpty  `cfg:",inline" reload:"empty"`
	*dwInner `cfg:",inline" reload:"inner"`
	A        dwShared   `cfg:"a"`
	B        []dwShared `cfg:"b"`
	Node     dwNode     `cfg:"node"`
}

// A walk of declared views visits each struct's selected declarations, inline
// owners included, empty ones too, along every route; the default walk visits
// visible fields as before.
func TestWalkType_DeclaredView(t *testing.T) {
	schema := schemaFor[dwConfig](t, engine(t, gowalker.WithFieldResolver(tagged("cfg"))))
	walk := func(options ...gowalker.WalkOption) ([]string, map[string]gowalker.TypeVisit, error) {
		var routes []string
		visits := map[string]gowalker.TypeVisit{}
		err := schema.WalkType(func(v gowalker.TypeVisit) (gowalker.Control, error) {
			route := typeRoute(v)
			if v.Kind == gowalker.Reference {
				route += "@ref"
			}
			routes = append(routes, route)
			visits[route] = v
			return gowalker.Continue, nil
		}, options...)
		return routes, visits, err
	}
	visible, _, err := walk()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"", "shared", "shared.Port", "a", "a.Port", "b", "b[]", "b[].Port", "node", "node.next", "node.next*@ref"}; !slices.Equal(visible, want) {
		t.Errorf("visible walk %q, want %q", visible, want)
	}
	declared, visits, err := walk(gowalker.WithDeclaredView())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"", "(dwEmpty)", "(dwInner)", "(dwInner)*", "(dwInner)*.shared", "(dwInner)*.shared.Port",
		"a", "a.Port", "b", "b[]", "b[].Port", "node", "node.next", "node.next*@ref", "node.(dwEmpty)",
	}
	if !slices.Equal(declared, want) {
		t.Errorf("declared walk %q, want %q", declared, want)
	}
	for route, want := range map[string]string{"(dwEmpty)": "empty", "(dwInner)*.shared.Port": "inner"} {
		if got, ok := visits[route].Inherited("reload"); !ok || got != want {
			t.Errorf("%s inherits reload %q, want %q", route, got, want)
		}
	}
	if f := visits["(dwInner)*.shared"].Route[2].Field; f.DeclaringType() != reflect.TypeFor[dwInner]() || !slices.Equal(f.Index(), []int{0}) {
		t.Errorf("a declared field's handle is relative to %v at %v", f.DeclaringType(), f.Index())
	}

	// A leaf stays a leaf.
	leaves := schemaFor[dwConfig](t, engine(t, gowalker.WithFieldResolver(tagged("cfg")),
		gowalker.WithAtomicType(func(t reflect.Type) bool { return t == reflect.TypeFor[dwShared]() })))
	var leafRoutes []string
	err = leaves.WalkType(func(v gowalker.TypeVisit) (gowalker.Control, error) {
		leafRoutes = append(leafRoutes, typeRoute(v))
		return gowalker.Continue, nil
	}, gowalker.WithDeclaredView())
	if err != nil || !slices.Contains(leafRoutes, "a") || slices.Contains(leafRoutes, "a.Port") || slices.Contains(leafRoutes, "(dwInner)*.shared.Port") {
		t.Errorf("declared walk with leaves %q: %v", leafRoutes, err)
	}

	// Controls and limits hold.
	var pruned []string
	err = schema.WalkType(func(v gowalker.TypeVisit) (gowalker.Control, error) {
		pruned = append(pruned, typeRoute(v))
		if typeRoute(v) == "(dwInner)" {
			return gowalker.SkipChildren, nil
		}
		return gowalker.Continue, nil
	}, gowalker.WithDeclaredView())
	if err != nil || slices.Contains(pruned, "(dwInner)*") {
		t.Errorf("SkipChildren at an inline owner: %q, %v", pruned, err)
	}
	err = schema.WalkType(func(gowalker.TypeVisit) (gowalker.Control, error) { return gowalker.Continue, nil },
		gowalker.WithDeclaredView(), gowalker.WithWalkLimits(gowalker.WalkLimits{MaxDepth: 1, MaxNodes: 100}))
	if !errors.Is(err, gowalker.ErrLimit) {
		t.Errorf("depth limit: %v", err)
	}
	err = schema.WalkValue(dwConfig{dwEmpty: dwEmpty{}, Node: dwNode{dwEmpty: dwEmpty{}}}, func(gowalker.ValueVisit) (gowalker.Control, error) { return gowalker.Continue, nil }, gowalker.WithDeclaredView())
	if !errors.Is(err, gowalker.ErrType) {
		t.Errorf("a value walk took WithDeclaredView: %v", err)
	}
}

type cycleNode struct {
	V    int
	Next *cycleNode
}

// A cycle closes on the type that re-enters first: through a pointer field
// the pointer type, in both walks.
func TestWalkType_CycleThroughAPointer(t *testing.T) {
	schema := schemaFor[struct{ N *cycleNode }](t, engine(t))
	for name, options := range map[string][]gowalker.WalkOption{"visible": nil, "declared": {gowalker.WithDeclaredView()}} {
		t.Run(name, func(t *testing.T) {
			var references []string
			err := schema.WalkType(func(v gowalker.TypeVisit) (gowalker.Control, error) {
				if v.Kind == gowalker.Reference {
					references = append(references, typeRoute(v)+" "+v.Shape.Type().String())
				}
				return gowalker.Continue, nil
			}, options...)
			if want := []string{"N*.Next *gowalker_test.cycleNode"}; err != nil || !slices.Equal(references, want) {
				t.Errorf("references %q, want %q: %v", references, want, err)
			}
		})
	}
}
