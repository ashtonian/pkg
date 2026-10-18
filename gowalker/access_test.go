package gowalker_test

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/gowalker/path"
)

func accessor(t testing.TB, s *gowalker.Schema, text string) *gowalker.Accessor {
	t.Helper()
	a, err := s.ResolveText(text)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

type child struct{ N int }
type accessRoot struct {
	Child   *child
	Rows    map[string]child
	Items   []child
	Dynamic any
	False   bool
	Empty   string
	//lint:ignore U1000 reflection fixture for inaccessible fields
	hidden *int
}

func TestReadStatesAndPointers(t *testing.T) {
	row := accessRoot{Rows: map[string]child{"one": {N: 0}}, Items: []child{{N: 4}}}
	s := schemaFor[accessRoot](t, engine(t))
	for _, test := range []struct {
		text    string
		state   gowalker.State
		value   any
		reached string
	}{
		{"Child", gowalker.Nil, (*child)(nil), "Child"},
		{"Child.N", gowalker.Missing, nil, "Child"},
		{"Rows[\"absent\"].N", gowalker.Missing, nil, "Rows"},
		{"Rows[\"one\"].N", gowalker.Present, 0, "Rows[\"one\"].N"},
		{"Items[0].N", gowalker.Present, 4, "Items[0].N"},
		{"Dynamic", gowalker.Nil, nil, "Dynamic"},
		{"False", gowalker.Present, false, "False"},
		{"Empty", gowalker.Present, "", "Empty"},
	} {
		t.Run(test.text, func(t *testing.T) {
			v, err := accessor(t, s, test.text).Get(&row)
			if err != nil || v.State != test.state || !reflect.DeepEqual(v.Interface(), test.value) {
				t.Fatalf("got %#v (%#v), %v", v, v.Interface(), err)
			}
			reached, _ := v.Reached.MarshalText()
			if string(reached) != test.reached {
				t.Fatalf("reached %q", reached)
			}
		})
	}
	if row.Child != nil {
		t.Fatal("read allocated nil ancestor")
	}
	row.Child = &child{N: 9}
	v, err := accessor(t, s, "Child").Get(row)
	if err != nil || v.Interface() != row.Child || v.Type != reflect.TypeFor[*child]() {
		t.Fatalf("pointer terminal changed: %v, %v", v, err)
	}
	deref, err := s.Resolve(path.Of(path.Field("Child"), path.Deref()))
	if err != nil {
		t.Fatal(err)
	}
	v, err = deref.Get(row)
	if err != nil || v.Interface() != *row.Child || v.Type != reflect.TypeFor[child]() {
		t.Fatalf("pointee: %v, %v", v, err)
	}
}

func TestRootBinding(t *testing.T) {
	e := engine(t)
	row := child{N: 5}
	ptr := &row
	for _, root := range []any{row, &row} {
		v, err := accessor(t, schemaFor[child](t, e), "N").Get(root)
		if err != nil || v.Interface() != 5 {
			t.Fatalf("value binding: %v, %v", v, err)
		}
	}
	p := schemaFor[*child](t, e)
	for _, root := range []any{ptr, &ptr} {
		v, err := accessor(t, p, "").Get(root)
		if err != nil || v.Interface() != ptr {
			t.Fatalf("pointer binding: %v, %v", v, err)
		}
	}
	var nilPointer *child
	v, err := accessor(t, p, "").Get(nilPointer)
	if err != nil || v.State != gowalker.Nil {
		t.Fatal(v, err)
	}
	if _, err := accessor(t, schemaFor[child](t, e), "N").Get(nilPointer); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	for _, root := range []any{nil, 1, new(int)} {
		if _, err := accessor(t, p, "N").Get(root); !errors.Is(err, gowalker.ErrType) {
			t.Fatal(err)
		}
	}
	a := accessor(t, schemaFor[child](t, e), "N")
	if _, err := a.GetValue(reflect.ValueOf(&row)); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	if _, err := a.GetValue(reflect.Value{}); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	if v, err := a.GetValue(reflect.ValueOf(row)); err != nil || v.Interface() != 5 {
		t.Fatal(v, err)
	}

	iface := schemaFor[any](t, e)
	boxed := any(row)
	for _, root := range []any{row, &boxed} {
		v, err := accessor(t, iface, "").Get(root)
		if err != nil || v.Type != reflect.TypeFor[any]() || v.DynamicType != reflect.TypeFor[child]() {
			t.Fatal(v, err)
		}
		v, err = accessor(t, iface, "N").Get(root)
		if err != nil || v.Interface() != 5 {
			t.Fatal(v, err)
		}
	}
	if v, err := accessor(t, iface, "").Get(nil); err != nil || v.State != gowalker.Nil || v.DynamicType != nil {
		t.Fatal(v, err)
	}
}

func TestDynamicInterfacesAndCycles(t *testing.T) {
	s := schemaFor[accessRoot](t, engine(t))
	a := accessor(t, s, "Dynamic.N")
	if a.Type() != nil {
		t.Fatal("dynamic type invented")
	}
	for _, dynamic := range []any{child{N: 6}, &child{N: 6}} {
		v, err := a.Get(accessRoot{Dynamic: dynamic})
		if err != nil || v.Interface() != 6 {
			t.Fatal(v, err)
		}
	}
	if _, err := a.Get(accessRoot{Dynamic: 6}); !errors.Is(err, gowalker.ErrPath) {
		t.Fatal(err)
	}
	if v, err := a.Get(accessRoot{}); err != nil || v.State != gowalker.Missing {
		t.Fatal(v, err)
	}
	var cycle any
	cycle = &cycle
	if _, err := a.Get(accessRoot{Dynamic: cycle}); !errors.Is(err, gowalker.ErrPath) {
		t.Fatalf("dynamic cycle: %v", err)
	}
	deref, err := s.Resolve(path.Of(path.Field("Dynamic"), path.Deref()))
	if err != nil {
		t.Fatal(err)
	}
	if v, err := deref.Get(accessRoot{Dynamic: child{N: 6}}); err != nil || v.Interface() != (child{N: 6}) {
		t.Fatal(v, err)
	}
}

func TestTypedKeysAndBounds(t *testing.T) {
	type named string
	s := schemaFor[map[named]int](t, engine(t))
	a, err := s.Resolve(path.Of(path.Key(named("key"))))
	if err != nil {
		t.Fatal(err)
	}
	if v, err := a.Get(map[named]int{"key": 7}); err != nil || v.Interface() != 7 {
		t.Fatal(v, err)
	}
	if _, err := s.ResolveText("[\"key\"]"); !errors.Is(err, gowalker.ErrKey) {
		t.Fatal(err)
	}
	for _, key := range []any{math.NaN(), struct{ F float64 }{math.NaN()}, []int{1}, map[string]int{"key": 1}} {
		_, err := schemaFor[map[any]int](t, engine(t)).Resolve(path.Of(path.Key(key)))
		if !errors.Is(err, gowalker.ErrKey) {
			t.Fatalf("key %T: %v", key, err)
		}
	}
	keys := schemaFor[map[any]int](t, engine(t))
	for _, key := range []any{nil, (*int)(nil), 3} {
		a, err := keys.Resolve(path.Of(path.Key(key)))
		if err != nil {
			t.Fatal(err)
		}
		v, err := a.Get(map[any]int{key: 9})
		if err != nil || v.Interface() != 9 {
			t.Fatal(v, err)
		}
	}
	array := schemaFor[[2]int](t, engine(t))
	if _, err := array.ResolveText("[2]"); !errors.Is(err, gowalker.ErrIndex) {
		t.Fatal(err)
	}
	slice := schemaFor[[]int](t, engine(t))
	if _, err := accessor(t, slice, "[1]").Get([]int{0}); !errors.Is(err, gowalker.ErrIndex) {
		t.Fatal(err)
	}
	if _, err := slice.Resolve(path.Of(path.Index(-1))); !errors.Is(err, gowalker.ErrIndex) {
		t.Fatal(err)
	}
	if _, err := slice.Resolve(path.Of(path.Step{})); !errors.Is(err, gowalker.ErrPath) {
		t.Fatal(err)
	}
}

func TestPrivateFieldsAndSafeDiagnostics(t *testing.T) {
	s := schemaFor[accessRoot](t, engine(t))
	if _, err := s.ResolveText("hidden"); !errors.Is(err, gowalker.ErrPath) {
		t.Fatal(err)
	}
	private := reflect.ValueOf(accessRoot{}).FieldByName("hidden")
	a := accessor(t, schemaFor[*int](t, engine(t)), "")
	if _, err := a.GetValue(private); !errors.Is(err, gowalker.ErrAccess) {
		t.Fatal(err)
	}
	secret := "secret-api-token"
	_, err := schemaFor[map[string]child](t, engine(t)).Resolve(path.Of(path.Key(secret), path.Field("Password")))
	if !errors.Is(err, gowalker.ErrPath) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "Password") {
		t.Fatal(err)
	}
	var operation *gowalker.Error
	if !errors.As(err, &operation) || operation.Step != 1 || operation.Path.Steps()[0].Key() != secret {
		t.Fatal("structured error lost location")
	}
}

func TestGetAsPreservesTypes(t *testing.T) {
	row := accessRoot{Child: &child{N: 7}}
	if n, err := gowalker.GetAs[int](row, "Child.N"); err != nil || n != 7 {
		t.Fatal(n, err)
	}
	if _, err := gowalker.GetAs[int64](row, "Child.N"); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	if _, err := gowalker.GetAs[int](accessRoot{}, "Child.N"); !errors.Is(err, gowalker.ErrMissing) {
		t.Fatal(err)
	}
	_, missing := gowalker.GetAs[int](accessRoot{}, "Child.N")
	var detail *gowalker.Error
	if !errors.As(missing, &detail) || detail.Path.Len() != 2 || detail.Step != 1 {
		t.Fatal(missing)
	}
	if p, err := gowalker.GetAs[*child](accessRoot{}, "Child"); err != nil || p != nil {
		t.Fatal(p, err)
	}
	if _, err := gowalker.Get(nil, ""); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
}

func TestAccessorGetAs(t *testing.T) {
	type count int
	type row struct {
		Count    count
		Optional *int
		Dynamic  any
	}
	schema, err := engine(t).Compile[row]()
	if err != nil {
		t.Fatal(err)
	}
	a := accessor(t, schema, "Count")
	var readCount func(any) (count, error) = a.GetAs
	if got, err := readCount(row{Count: 7}); err != nil || got != 7 {
		t.Fatal(got, err)
	}
	if _, err := a.GetAs[int](row{}); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	if got, err := a.GetAs[any](row{Count: 7}); err != nil || got != count(7) {
		t.Fatal(got, err)
	}
	if got, err := accessor(t, schema, "Optional").GetAs[*int](row{}); err != nil || got != nil {
		t.Fatal(got, err)
	}
	if got, err := accessor(t, schema, "Dynamic").GetAs[any](row{}); err != nil || got != nil {
		t.Fatal(got, err)
	}
	if _, err := accessor(t, schema, "Dynamic").GetAs[int](row{Dynamic: 7}); !errors.Is(err, gowalker.ErrType) {
		t.Fatal("terminal interface was implicitly unboxed", err)
	}
	missing := accessor(t, schemaFor[accessRoot](t, engine(t)), "Child.N")
	if _, err := missing.GetAs[int](accessRoot{}); !errors.Is(err, gowalker.ErrMissing) {
		t.Fatal(err)
	}
	if _, err := a.GetAs[count](struct{}{}); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
}

func TestReadLimitsAndConcurrency(t *testing.T) {
	limits := gowalker.DefaultLimits()
	limits.MaxPathSteps = 1
	s := schemaFor[accessRoot](t, engine(t, gowalker.WithLimits(limits)))
	if _, err := s.ResolveText("Child.N"); !errors.Is(err, gowalker.ErrLimit) {
		t.Fatal(err)
	}
	if _, err := s.ResolveText(strings.Repeat("x", path.MaxTextBytes+1)); !errors.Is(err, gowalker.ErrLimit) || !errors.Is(err, path.ErrLimit) {
		t.Fatal(err)
	}
	a := accessor(t, schemaFor[accessRoot](t, engine(t)), "Dynamic.N")
	var group sync.WaitGroup
	for i := range 32 {
		group.Go(func() {
			v, err := a.Get(accessRoot{Dynamic: &child{N: i}})
			if err != nil || v.Interface() != i {
				t.Errorf("got %v, %v", v, err)
			}
		})
	}
	group.Wait()
}

func TestDynamicFailureCausesAndMissingType(t *testing.T) {
	cause := errors.New("consumer schema failure")
	e := engine(t, gowalker.WithFieldResolver(func(reflect.StructField) (gowalker.FieldRule, error) { return gowalker.FieldRule{}, cause }))
	a := accessor(t, schemaFor[any](t, e), "N")
	if _, err := a.Get(child{}); !errors.Is(err, gowalker.ErrSchema) || !errors.Is(err, cause) {
		t.Fatalf("dynamic schema cause lost: %v", err)
	}
	s := schemaFor[any](t, engine(t))
	v, err := accessor(t, s, "[\"absent\"]").Get(map[string]int{})
	if err != nil || v.State != gowalker.Missing || v.Type != reflect.TypeFor[int]() {
		t.Fatal(v, err)
	}
	limits := gowalker.DefaultLimits()
	limits.MaxDepth = 2
	a = accessor(t, schemaFor[any](t, engine(t, gowalker.WithLimits(limits))), "N")
	x := any(child{N: 1})
	y := any(&x)
	z := any(&y)
	if _, err := a.Get(z); !errors.Is(err, gowalker.ErrLimit) {
		t.Fatalf("indirection limit: %v", err)
	}
	e = engine(t, gowalker.WithAtomicType(func(typ reflect.Type) bool { return typ == reflect.TypeFor[child]() }))
	if _, err := accessor(t, schemaFor[any](t, e), "N").Get(child{}); !errors.Is(err, gowalker.ErrPath) {
		t.Fatal(err)
	}
}

func TestInlineNilAndMultiplePointers(t *testing.T) {
	type row struct {
		*EmbeddedPointer `db:",inline"`
	}
	s := schemaFor[row](t, engine(t, gowalker.WithFieldResolver(tagged("db"))))
	a := accessor(t, s, "Value")
	input := row{}
	if v, err := a.Get(&input); err != nil || v.State != gowalker.Missing || input.EmbeddedPointer != nil {
		t.Fatal(v, err)
	}
	input.EmbeddedPointer = &EmbeddedPointer{Value: 3}
	if v, err := a.Get(input); err != nil || v.Interface() != 3 {
		t.Fatal(v, err)
	}
	p := &input
	pp := &p
	if n, err := gowalker.GetAs[int](&pp, "EmbeddedPointer.Value"); err != nil || n != 3 {
		t.Fatal(n, err)
	}
}

func FuzzRead(f *testing.F) {
	for _, text := range []string{"", "Child", "Child.N", "Rows[\"one\"].N", "Items[0]", "Dynamic.N", "hidden", "Missing"} {
		f.Add(text)
	}
	s, err := mustEngine().Compile[accessRoot]()
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, text string) {
		a, err := s.ResolveText(text)
		if err != nil {
			return
		}
		for _, row := range []accessRoot{{}, {Rows: map[string]child{"one": {N: 3}}, Items: []child{{N: 2}}, Dynamic: &child{N: 1}}} {
			_, _ = a.Get(row)
		}
	})
}

func mustEngine() *gowalker.Engine {
	e, err := gowalker.New()
	if err != nil {
		panic(err)
	}
	return e
}

func TestGetAsAssignableDistinctTypes(t *testing.T) {
	type named struct{ N int }
	row := struct{ Value struct{ N int } }{}
	row.Value.N = 7
	a := accessor(t, schemaFor[struct{ Value struct{ N int } }](t, engine(t)), "Value")
	got, err := a.GetAs[named](&row)
	if err != nil || got.N != 7 {
		t.Fatal(got, err)
	}
}

func TestMissingLongPrefix(t *testing.T) {
	root := map[string]any{}
	current := root
	var steps []path.Step
	for range 10 {
		next := map[string]any{}
		current["child"] = next
		current = next
		steps = append(steps, path.Key("child"))
	}
	steps = append(steps, path.Key("missing"))
	s := schemaFor[map[string]any](t, engine(t))
	a, err := s.Resolve(path.Of(steps...))
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Get(&root)
	if err != nil || got.State != gowalker.Missing || got.Reached.Len() != 10 {
		t.Fatal(got, err)
	}
}
