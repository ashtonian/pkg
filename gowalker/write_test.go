package gowalker_test

import (
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/gowalker/path"
)

func TestSetPointersAndRootBinding(t *testing.T) {
	row := accessRoot{}
	if err := gowalker.Set(&row, "Child.N", 7); !errors.Is(err, gowalker.ErrNil) {
		t.Fatal(err)
	}
	if row.Child != nil {
		t.Fatal("implicit allocation")
	}
	if err := gowalker.Set(&row, "Child.N", 7, gowalker.WithPointerAllocation()); err != nil {
		t.Fatal(err)
	}
	alias := row.Child
	if err := gowalker.Set(&row, "Child.N", 8); err != nil || row.Child != alias || alias.N != 8 {
		t.Fatal(row, err)
	}
	replacement := &child{N: 9}
	if err := gowalker.Set(&row, "Child", replacement); err != nil || row.Child != replacement || alias.N != 8 {
		t.Fatal(row, err)
	}
	if err := gowalker.Set(&row, "Child", (*child)(nil)); err != nil || row.Child != nil {
		t.Fatal(row, err)
	}
	s := schemaFor[*child](t, engine(t))
	a := accessor(t, s, "N")
	var ptr *child
	if err := a.Set(&ptr, 3, gowalker.WithPointerAllocation()); err != nil || ptr.N != 3 {
		t.Fatal(ptr, err)
	}
	if err := a.Set(ptr, 4); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	for _, root := range []any{nil, row, (*accessRoot)(nil), new(int)} {
		if err := accessor(t, schemaFor[accessRoot](t, engine(t)), "Child.N").Set(root, 5); !errors.Is(err, gowalker.ErrType) {
			t.Fatal(err)
		}
	}
	type namedPointer *child
	var defined namedPointer
	if err := gowalker.Set(&defined, "N", 10, gowalker.WithPointerAllocation()); err != nil || defined.N != 10 {
		t.Fatal(defined, err)
	}
}

func TestSetMapAndInterfaceOwners(t *testing.T) {
	type row struct {
		N    int
		Pair [2]child
	}
	input := map[string]row{"one": {N: 1}}
	mapAlias := input
	if err := gowalker.Set(&input, "[\"one\"].N", 2); err != nil || input["one"].N != 2 || mapAlias["one"].N != 2 {
		t.Fatal(input, err)
	}
	if err := gowalker.Set(&input, "[\"new\"]", row{N: 3}); err != nil || input["new"].N != 3 {
		t.Fatal(input, err)
	}
	if err := gowalker.Set(&input, "[\"missing\"].N", 4); !errors.Is(err, gowalker.ErrMissing) {
		t.Fatal(err)
	}
	if err := gowalker.Set(&input, "[\"missing\"].N", 4, gowalker.WithMapAllocation()); err != nil || input["missing"].N != 4 {
		t.Fatal(input, err)
	}
	if err := gowalker.Set(&input, "[\"one\"].Pair[1].N", 5); err != nil || input["one"].Pair[1].N != 5 {
		t.Fatal(input, err)
	}
	boxed := any(row{N: 1})
	a := accessor(t, schemaFor[any](t, engine(t)), "N")
	if err := a.Set(&boxed, 6); err != nil || boxed.(row).N != 6 {
		t.Fatal(boxed, err)
	}
	nested := map[string]any{"box": row{N: 1}}
	if err := gowalker.Set(&nested, "[\"box\"].N", 7); err != nil || nested["box"].(row).N != 7 {
		t.Fatal(nested, err)
	}
	boxed = [2]row{}
	if err := accessor(t, schemaFor[any](t, engine(t)), "[1].N").Set(&boxed, 8); err != nil || boxed.([2]row)[1].N != 8 {
		t.Fatal(boxed, err)
	}
	nested["box"] = []row{{N: 1}}
	sliceAlias := nested["box"].([]row)
	if err := gowalker.Set(&nested, "[\"box\"][0].N", 9); err != nil || sliceAlias[0].N != 9 {
		t.Fatal(nested, err)
	}
}

func TestSetOptionalContainersAndFailedWrites(t *testing.T) {
	type leaf struct{ N uint8 }
	type root struct{ Values map[string]*leaf }
	var input *root
	options := []gowalker.WriteOption{gowalker.WithPointerAllocation(), gowalker.WithMapAllocation()}
	if err := gowalker.Set(&input, "Values[\"new\"].N", 256, append(options, gowalker.WithCheckedNumbers())...); !errors.Is(err, gowalker.ErrConversion) {
		t.Fatal(err)
	}
	if input != nil {
		t.Fatal("failed conversion attached allocations")
	}
	if err := gowalker.Set(&input, "Values[\"new\"].N", uint8(7), options...); err != nil || input.Values["new"].N != 7 {
		t.Fatal(input, err)
	}
	owner := input
	entry := input.Values["new"]
	if err := gowalker.Set(&input, "Values[\"new\"].N", "invalid", options...); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	if input != owner || input.Values["new"] != entry || entry.N != 7 {
		t.Fatal("failed write changed existing values")
	}
	if err := gowalker.Set(&input, "Values[\"absent\"].N", "invalid", options...); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	if len(input.Values) != 1 {
		t.Fatal("failed write inserted intermediate")
	}
	var maps map[string]map[string]int
	if err := gowalker.Set(&maps, "[\"one\"][\"two\"]", 9, gowalker.WithMapAllocation()); err != nil || maps["one"]["two"] != 9 {
		t.Fatal(maps, err)
	}
	var empty map[string]int
	if err := gowalker.Set(&empty, "[\"key\"]", 1); !errors.Is(err, gowalker.ErrNil) || empty != nil {
		t.Fatal(empty, err)
	}
	var dynamic any
	if err := accessor(t, schemaFor[any](t, engine(t)), "N").Set(&dynamic, 1, options...); !errors.Is(err, gowalker.ErrNil) || dynamic != nil {
		t.Fatal(dynamic, err)
	}
	if err := gowalker.Set(&input, "Values[\"nil\"]", (*leaf)(nil)); err != nil {
		t.Fatal(err)
	}
	if value, exists := input.Values["nil"]; !exists || value != nil {
		t.Fatal("typed nil deleted entry")
	}
}

func TestSetReferenceCyclesAndAliases(t *testing.T) {
	type node struct {
		N    int
		Self *node
		Map  map[string]node
	}
	n := node{N: 1}
	n.Self = &n
	if err := gowalker.Set(&n, "Self.N", 2); err != nil || n.N != 2 || n.Self != &n {
		t.Fatal(n, err)
	}
	n.Map = map[string]node{"copy": {N: 10, Self: &n}}
	if err := gowalker.Set(&n, "Map[\"copy\"].Self.N", 3); err != nil || n.N != 3 || n.Map["copy"].N != 10 {
		t.Fatal(n, err)
	}
	m := map[string]any{}
	m["self"] = m
	if err := gowalker.Set(&m, "[\"self\"][\"new\"]", 4); err != nil || m["new"] != 4 {
		t.Fatal(err)
	}
	var cycle any
	cycle = &cycle
	if err := accessor(t, schemaFor[any](t, engine(t)), "N").Set(&cycle, 1); !errors.Is(err, gowalker.ErrPath) {
		t.Fatal(err)
	}
}

func TestSetReflectAccessAndExplicitDeref(t *testing.T) {
	input := &child{N: 1}
	s := schemaFor[*child](t, engine(t))
	a, err := s.Resolve(path.Of(path.Deref()))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Set(&input, child{N: 2}); err != nil || input.N != 2 {
		t.Fatal(input, err)
	}
	var nilPointer *child
	if err := a.Set(&nilPointer, child{N: 3}, gowalker.WithPointerAllocation()); err != nil || nilPointer.N != 3 {
		t.Fatal(nilPointer, err)
	}
	plain := child{}
	n := accessor(t, schemaFor[child](t, engine(t)), "N")
	if err := n.SetValue(reflect.ValueOf(plain), reflect.ValueOf(4)); !errors.Is(err, gowalker.ErrAccess) {
		t.Fatal(err)
	}
	if err := n.SetValue(reflect.Value{}, reflect.ValueOf(4)); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	if err := n.SetValue(reflect.ValueOf(&plain).Elem(), reflect.Value{}); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	private := reflect.ValueOf(accessRoot{}).FieldByName("hidden")
	if err := n.SetValue(reflect.ValueOf(&plain).Elem(), private); !errors.Is(err, gowalker.ErrAccess) {
		t.Fatal(err)
	}
	if err := n.Set(&plain, nil); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	if err := n.Set(&plain, 5, nil); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	var boxed any = 7
	root := accessor(t, schemaFor[any](t, engine(t)), "")
	if err := root.SetValue(reflect.ValueOf(&boxed).Elem(), reflect.Zero(reflect.TypeFor[any]())); err != nil || boxed != nil {
		t.Fatal(boxed, err)
	}
	inline := struct {
		embedded `db:",inline"`
	}{}
	field := accessor(t, schemaFor[struct {
		embedded `db:",inline"`
	}](t, engine(t, gowalker.WithFieldResolver(tagged("db")))), "value")
	if err := field.Set(&inline, 8); err != nil || inline.Value != 8 {
		t.Fatal(inline, err)
	}
	list := []int{1}
	if err := gowalker.Set(&list, "[1]", 2); !errors.Is(err, gowalker.ErrIndex) || len(list) != 1 || list[0] != 1 {
		t.Fatal(list, err)
	}
	for _, root := range []any{nil, 1, (*int)(nil)} {
		if err := gowalker.Set(root, "", 1); !errors.Is(err, gowalker.ErrType) {
			t.Fatal(err)
		}
	}
}

type privateEmbeddedPointer struct{ Value int }

func TestSetPrivateEmbeddedPointer(t *testing.T) {
	type row struct {
		*privateEmbeddedPointer `db:",inline"`
	}
	s := schemaFor[row](t, engine(t, gowalker.WithFieldResolver(tagged("db"))))
	a := accessor(t, s, "Value")
	input := row{}
	if err := a.Set(&input, 2, gowalker.WithPointerAllocation()); !errors.Is(err, gowalker.ErrAccess) || input.privateEmbeddedPointer != nil {
		t.Fatal(input, err)
	}
	input.privateEmbeddedPointer = &privateEmbeddedPointer{Value: 1}
	if err := a.Set(&input, 2); err != nil || input.Value != 2 {
		t.Fatal(input, err)
	}
}

func TestCheckedNumbers(t *testing.T) {
	type small uint8
	for _, test := range []struct {
		input  any
		target reflect.Type
		want   any
		valid  bool
	}{
		{int(255), reflect.TypeFor[small](), small(255), true},
		{int(256), reflect.TypeFor[small](), nil, false},
		{int(-1), reflect.TypeFor[uint64](), nil, false},
		{uint64(math.MaxUint64), reflect.TypeFor[int64](), nil, false},
		{float64(7), reflect.TypeFor[int8](), int8(7), true},
		{float64(7.5), reflect.TypeFor[int8](), nil, false},
		{float64(1.5), reflect.TypeFor[float32](), float32(1.5), true},
		{float64(0.1), reflect.TypeFor[float32](), nil, false},
		{uint64(1 << 53), reflect.TypeFor[float64](), float64(1 << 53), true},
		{uint64(1<<53 + 1), reflect.TypeFor[float64](), nil, false},
		{math.NaN(), reflect.TypeFor[float32](), nil, false},
		{math.Inf(1), reflect.TypeFor[float32](), nil, false},
		{math.MaxFloat64, reflect.TypeFor[float32](), nil, false},
		{math.SmallestNonzeroFloat64, reflect.TypeFor[float32](), nil, false},
		{math.Copysign(0, -1), reflect.TypeFor[int](), nil, false},
		{math.Copysign(0, -1), reflect.TypeFor[float32](), float32(math.Copysign(0, -1)), true},
		{complex128(1.5 + 2.5i), reflect.TypeFor[complex64](), complex64(1.5 + 2.5i), true},
		{complex128(1.5 + 0.1i), reflect.TypeFor[complex64](), nil, false},
		{complex128(1 + 0i), reflect.TypeFor[float64](), nil, false},
		{"7", reflect.TypeFor[int](), nil, false},
	} {
		t.Run(reflect.TypeOf(test.input).String()+"/"+test.target.String(), func(t *testing.T) {
			destination := reflect.New(test.target)
			s, err := engine(t).CompileType(test.target)
			if err != nil {
				t.Fatal(err)
			}
			a := accessor(t, s, "")
			err = a.SetValue(destination.Elem(), reflect.ValueOf(test.input), gowalker.WithCheckedNumbers())
			if test.valid {
				if err != nil || !reflect.DeepEqual(destination.Elem().Interface(), test.want) {
					t.Fatal(destination.Elem(), err)
				}
			} else if !errors.Is(err, gowalker.ErrConversion) || !destination.Elem().IsZero() {
				t.Fatal(destination.Elem(), err)
			}
		})
	}
	var f float64
	if err := gowalker.Set(&f, "", math.NaN(), gowalker.WithCheckedNumbers()); err != nil || !math.IsNaN(f) {
		t.Fatal(f, err)
	}
	var smallFloat float32
	if err := gowalker.Set(&smallFloat, "", math.Copysign(0, -1), gowalker.WithCheckedNumbers()); err != nil || !math.Signbit(float64(smallFloat)) {
		t.Fatal(smallFloat, err)
	}
}

func TestSetConcurrentAccessorReuse(t *testing.T) {
	a := accessor(t, schemaFor[accessRoot](t, engine(t)), "Child.N")
	var group sync.WaitGroup
	for i := range 32 {
		group.Go(func() {
			row := accessRoot{}
			if err := a.Set(&row, i, gowalker.WithPointerAllocation()); err != nil || row.Child.N != i {
				t.Error(row, err)
			}
		})
	}
	group.Wait()
}

func FuzzCheckedWrite(f *testing.F) {
	for _, n := range []int64{0, -1, 255, 256, 1 << 53, 1<<53 + 1, math.MaxInt64} {
		f.Add(n)
	}
	s, err := mustEngine().Compile[struct{ N *uint8 }]()
	if err != nil {
		f.Fatal(err)
	}
	a, err := s.Resolve(path.Of(path.Field("N"), path.Deref()))
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, n int64) {
		row := struct{ N *uint8 }{}
		err := a.Set(&row, n, gowalker.WithPointerAllocation(), gowalker.WithCheckedNumbers())
		if n >= 0 && n <= math.MaxUint8 {
			if err != nil || row.N == nil || *row.N != uint8(n) {
				t.Fatal(row, err)
			}
		} else if !errors.Is(err, gowalker.ErrConversion) || row.N != nil {
			t.Fatal("failed write attached allocation", row, err)
		}
	})
}

func FuzzWritePaths(f *testing.F) {
	for _, text := range []string{"", "Child", "Child.N", "Rows[\"one\"].N", "Rows[\"new\"].N", "Items[0].N", "Dynamic.N", "False", "hidden", "unknown"} {
		f.Add(text, int64(42), true)
	}
	s, err := mustEngine().Compile[accessRoot]()
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, text string, n int64, allocate bool) {
		a, err := s.ResolveText(text)
		if err != nil {
			return
		}
		fresh := func() accessRoot {
			return accessRoot{Child: &child{N: 11}, Rows: map[string]child{"one": {N: 12}}, Items: []child{{N: 13}}, Dynamic: &child{N: 14}, False: true, Empty: "old"}
		}
		for _, populated := range []bool{false, true} {
			var input, expected accessRoot
			if populated {
				input, expected = fresh(), fresh()
			}
			childIdentity := input.Child
			mapIdentity := reflect.ValueOf(input.Rows).Pointer()
			sliceIdentity := reflect.ValueOf(input.Items).Pointer()
			options := []gowalker.WriteOption{gowalker.WithCheckedNumbers()}
			if allocate {
				options = append(options, gowalker.WithPointerAllocation(), gowalker.WithMapAllocation())
			}
			err := a.Set(&input, n, options...)
			if err != nil && (!reflect.DeepEqual(input, expected) || input.Child != childIdentity || reflect.ValueOf(input.Rows).Pointer() != mapIdentity || reflect.ValueOf(input.Items).Pointer() != sliceIdentity) {
				t.Fatal("failed write changed input", err)
			}
		}
	})
}
