package gowalker_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/gowalker/path"
)

type locateInner struct {
	X int `json:"x"`
}

type locateRow struct {
	*locateInner
	Inner *locateInner            `json:"inner"`
	Byval map[string]locateInner  `json:"byval"`
	Byref map[string]*locateInner `json:"byref"`
	Any   any                     `json:"any"`
	Y     int                     `json:"y"`
}

type LocateInnerExported struct {
	X int `json:"x"`
}

func locator(t *testing.T, typ reflect.Type, steps ...path.Step) *gowalker.Accessor {
	t.Helper()
	schema, err := engine(t, gowalker.WithJSONFields("json", gowalker.NestedFields())).CompileType(typ)
	if err != nil {
		t.Fatal(err)
	}
	a, err := schema.Resolve(path.Of(steps...))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Locate returns the field's own storage, allocating a nil embedded pointer
// only when asked, and a write through it reaches the root.
func TestLocate(t *testing.T) {
	type Outer struct {
		*LocateInnerExported
		Y int `json:"y"`
	}
	x := locator(t, reflect.TypeFor[Outer](), path.Field("x"))
	var row Outer
	root := reflect.ValueOf(&row).Elem()
	if _, err := x.Locate(root); !errors.Is(err, gowalker.ErrNil) || row.LocateInnerExported != nil {
		t.Fatalf("without allocation: %v, %+v", err, row)
	}
	location, err := x.Locate(root, gowalker.WithPointerAllocation())
	if err != nil {
		t.Fatal(err)
	}
	if row.LocateInnerExported == nil || location.Addr().Interface() != &row.X {
		t.Fatalf("location %v is not row.X", location)
	}
	location.SetInt(7)
	if row.X != 7 {
		t.Fatalf("row.X = %d", row.X)
	}
	y := locator(t, reflect.TypeFor[Outer](), path.Field("y"))
	if location, err := y.Locate(root); err != nil || location.Addr().Interface() != &row.Y {
		t.Fatalf("y: %v, %v", location, err)
	}
	if _, err := y.Locate(reflect.ValueOf(row)); !errors.Is(err, gowalker.ErrAccess) {
		t.Errorf("an unsettable root: %v", err)
	}
	if _, err := y.Locate(reflect.ValueOf(&row)); !errors.Is(err, gowalker.ErrType) {
		t.Errorf("a pointer root: %v", err)
	}
}

// A location a write would not reach is refused and the graph is unchanged,
// allocations included.
func TestLocate_Refusals(t *testing.T) {
	typ := reflect.TypeFor[locateRow]()
	allocate := []gowalker.WriteOption{gowalker.WithPointerAllocation(), gowalker.WithMapAllocation()}
	for _, tc := range []struct {
		name  string
		steps []path.Step
		row   locateRow
		want  error
	}{
		{"unexported embedded pointer", []path.Step{path.Field("x")}, locateRow{}, gowalker.ErrAccess},
		{"struct held in a map", []path.Step{path.Field("byval"), path.Key("a"), path.Field("x")}, locateRow{}, gowalker.ErrAccess},
		{"map entry", []path.Step{path.Field("byref"), path.Key("a")}, locateRow{}, gowalker.ErrAccess},
		{"interface value", []path.Step{path.Field("any"), path.Deref()}, locateRow{Any: 1}, gowalker.ErrAccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := tc.row
			before := row
			if _, err := locator(t, typ, tc.steps...).Locate(reflect.ValueOf(&row).Elem(), allocate...); !errors.Is(err, tc.want) {
				t.Fatalf("%v, want %v", err, tc.want)
			}
			if !reflect.DeepEqual(row, before) {
				t.Errorf("graph changed: %+v", row)
			}
		})
	}
	// SetValue still writes the struct held in a map back.
	row := locateRow{}
	byval := locator(t, typ, path.Field("byval"), path.Key("a"), path.Field("x"))
	if err := byval.SetValue(reflect.ValueOf(&row).Elem(), reflect.ValueOf(3), allocate...); err != nil || row.Byval["a"].X != 3 {
		t.Fatalf("SetValue: %v, %+v", err, row)
	}
}

// Missing map entries and nil pointers below a map are allocated, attached
// and reached by a write through the result.
func TestLocate_AllocatesThroughMaps(t *testing.T) {
	var row locateRow
	x := locator(t, reflect.TypeFor[locateRow](), path.Field("byref"), path.Key("a"), path.Field("x"))
	if _, err := x.Locate(reflect.ValueOf(&row).Elem(), gowalker.WithPointerAllocation()); !errors.Is(err, gowalker.ErrNil) || row.Byref != nil {
		t.Fatalf("without map allocation: %v, %+v", err, row)
	}
	location, err := x.Locate(reflect.ValueOf(&row).Elem(), gowalker.WithPointerAllocation(), gowalker.WithMapAllocation())
	if err != nil {
		t.Fatal(err)
	}
	location.SetInt(5)
	if row.Byref["a"] == nil || row.Byref["a"].X != 5 {
		t.Fatalf("row.Byref = %+v", row.Byref)
	}
	// A pointer below an interface reaches shared storage.
	inner := &locateInner{}
	row.Any = inner
	viaAny := locator(t, reflect.TypeFor[locateRow](), path.Field("any"), path.Deref(), path.Deref(), path.Field("x"))
	if location, err := viaAny.Locate(reflect.ValueOf(&row).Elem()); err != nil || location.Addr().Interface() != &inner.X {
		t.Fatalf("through an interface: %v, %v", location, err)
	}
}
