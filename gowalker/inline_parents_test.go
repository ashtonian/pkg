package gowalker_test

import (
	"testing"

	"github.com/ashtonian/pkg/gowalker"
)

type selectorInner struct {
	ID int
}

type selectorMid struct {
	selectorInner
}

type selectorSection struct {
	Port int `col:"port"`
}

type selectorRow struct {
	selectorMid
	Name    string
	Section selectorSection `col:"section,inline"`
}

// lookup is the root field of s named name, failing the test without one.
func lookup(t *testing.T, s *gowalker.Schema, name string) *gowalker.Field {
	t.Helper()
	f, ok := s.Root().Lookup(name)
	if !ok {
		t.Fatalf("%s has no field %q", s.Type(), name)
	}
	return f
}

func TestFieldGoSelectorNamesTheInlineParents(t *testing.T) {
	json := schemaFor[selectorRow](t, engine(t, gowalker.WithJSONFields("json")))
	assert.Equal(t, "selectorMid.selectorInner.ID", lookup(t, json, "ID").GoSelector())
	assert.Equal(t, "Name", lookup(t, json, "Name").GoSelector())

	// A resolver may inline a named field: its name is a selector step too.
	inlined := schemaFor[selectorRow](t, engine(t, gowalker.WithFieldResolver(tagged("col"))))
	assert.Equal(t, "Section.Port", lookup(t, inlined, "port").GoSelector())
}

type allocHidden struct {
	Hidden int
}

type allocValue struct {
	ByValue int
}

type AllocExported struct {
	Exported int
}

type AllocOuter struct {
	*allocDeep
}

type allocDeep struct {
	Deep int
}

type allocRow struct {
	*allocHidden
	allocValue
	*AllocExported
	*AllocOuter
}

func TestUnallocatableParentIsTheFirstUnexportedPointer(t *testing.T) {
	s := schemaFor[allocRow](t, engine(t, gowalker.WithJSONFields("json")))
	for _, tc := range []struct {
		field  string
		parent string
	}{
		{"Hidden", "allocHidden"},
		{"ByValue", ""},
		{"Exported", ""},
		// AllocOuter can be allocated; the pointer inside it cannot.
		{"Deep", "allocDeep"},
	} {
		parent, ok := lookup(t, s, tc.field).UnallocatableParent()
		assert.Equal(t, tc.parent != "", ok, tc.field)
		assert.Equal(t, tc.parent, parent.Name, tc.field)
	}

	declared := map[string]string{}
	for _, d := range s.Root().Declarations() {
		parent, ok := d.UnallocatableParent()
		if ok {
			declared[d.GoField.Name] = parent.Name
		}
	}
	assert.Equal(t, map[string]string{"Hidden": "allocHidden", "Deep": "allocDeep"}, declared)
}

// The rule is what a write meets: a nil unexported pointer cannot be
// allocated while an exported one can, and a field below a non-nil
// unexported pointer is settable.
func TestUnallocatableParentMatchesWrites(t *testing.T) {
	s := schemaFor[allocRow](t, engine(t, gowalker.WithJSONFields("json")))
	set := func(row *allocRow, name string) error {
		t.Helper()
		a, err := s.AccessName(name)
		if err != nil {
			t.Fatal(err)
		}
		return a.Set(row, 1, gowalker.WithPointerAllocation())
	}

	var fresh allocRow
	assert.ErrorIs(t, set(&fresh, "Hidden"), gowalker.ErrAccess)
	assert.ErrorIs(t, set(&fresh, "Deep"), gowalker.ErrAccess)
	assert.NoError(t, set(&fresh, "Exported"))
	assert.Equal(t, 1, fresh.Exported)

	present := allocRow{allocHidden: &allocHidden{}}
	assert.NoError(t, set(&present, "Hidden"))
	assert.Equal(t, 1, present.Hidden)
}
