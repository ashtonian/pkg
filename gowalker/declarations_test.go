package gowalker_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
)

type declBase struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	secret string
	Notes  string `json:"-"`
}

type declRow struct {
	declBase
	Name     string `json:"name"`
	Password string `json:"-" db:"password_hash"`
	Skipped  int    `json:"-"`
	internal int
}

type listed struct {
	state  gowalker.DeclarationState
	goName string
	index  []int
}

func listing(t *testing.T, shape *gowalker.Shape) []listed {
	t.Helper()
	var got []listed
	for _, d := range shape.Declarations() {
		if (d.Field == nil) != (d.State == gowalker.Skipped) {
			t.Errorf("%s: state %v with field %v", d.GoField.Name, d.State, d.Field)
		}
		got = append(got, listed{d.State, d.GoField.Name, d.Index})
	}
	return got
}

// Declarations lists the owner's fields and, after each inlined struct, its
// fields, with what the view made of each and the route from the owner.
func TestDeclarations(t *testing.T) {
	schema := schemaFor[declRow](t, engine(t, gowalker.WithJSONFields("json", gowalker.KeepHidden())))
	got := listing(t, schema.Root())
	want := []listed{
		{gowalker.InlineOwner, "declBase", []int{0}},
		{gowalker.Named, "ID", []int{0, 0}},
		{gowalker.Shadowed, "Name", []int{0, 1}},
		{gowalker.Skipped, "secret", []int{0, 2}},
		{gowalker.Unnamed, "Notes", []int{0, 3}},
		{gowalker.Named, "Name", []int{1}},
		{gowalker.Unnamed, "Password", []int{2}},
		{gowalker.Unnamed, "Skipped", []int{3}},
		{gowalker.Skipped, "internal", []int{4}},
	}
	if !slices.EqualFunc(got, want, func(a, b listed) bool {
		return a.state == b.state && a.goName == b.goName && slices.Equal(a.index, b.index)
	}) {
		t.Fatalf("declarations\n got %v\nwant %v", got, want)
	}
	for _, d := range schema.Root().Declarations() {
		if d.GoField.Name == "Notes" && (d.DeclaringType != reflect.TypeFor[declBase]() || len(d.InlineParents) != 1 || d.InlineParents[0].Name != "declBase") {
			t.Errorf("Notes: declared by %v through %v", d.DeclaringType, d.InlineParents)
		}
	}
	// Without KeepHidden the hidden fields are skipped.
	plain := schemaFor[declRow](t, engine(t, gowalker.WithJSONFields("json")))
	for _, d := range plain.Root().Declarations() {
		if d.State == gowalker.Unnamed {
			t.Errorf("%s kept without KeepHidden", d.GoField.Name)
		}
	}
}

// A field kept without a name claims no name and is reached by no path or
// walk; AccessField reads and writes it.
func TestUnnamedFields(t *testing.T) {
	schema := schemaFor[declRow](t, engine(t, gowalker.WithJSONFields("json", gowalker.KeepHidden())))
	var names []string
	for _, f := range schema.Root().Fields() {
		names = append(names, f.Name())
	}
	if want := []string{"id", "name"}; !slices.Equal(names, want) {
		t.Fatalf("Fields %q, want %q", names, want)
	}
	var password *gowalker.Field
	for _, d := range schema.Root().Declarations() {
		if d.GoField.Name == "Password" {
			password = d.Field
		}
	}
	if password.Name() != "" {
		t.Fatalf("an unnamed field is named %q", password.Name())
	}
	for _, name := range []string{"", "Password", "password_hash"} {
		if _, ok := schema.Root().Lookup(name); ok {
			t.Errorf("Lookup(%q) found an unnamed field", name)
		}
	}
	a, err := schema.AccessField(password)
	if err != nil {
		t.Fatal(err)
	}
	row := declRow{Password: "hash"}
	if got, err := a.GetAs[string](&row); err != nil || got != "hash" {
		t.Fatalf("read %q, %v", got, err)
	}
	if err := a.Set(&row, "next"); err != nil || row.Password != "next" {
		t.Fatalf("wrote %q, %v", row.Password, err)
	}
	if again, err := schema.AccessField(password); err != nil || again != a {
		t.Errorf("accessor not reused: %v", err)
	}
	var visited []string
	err = schema.WalkValue(row, func(v gowalker.ValueVisit) (gowalker.Control, error) {
		if v.Field != nil {
			visited = append(visited, v.Field.GoField().Name)
		}
		return gowalker.Continue, nil
	})
	if err != nil || slices.Contains(visited, "Password") {
		t.Errorf("walk visited %q, %v", visited, err)
	}
	// An unnamed field of an inlined struct is reached by its whole route.
	var notes *gowalker.Field
	for _, d := range schema.Root().Declarations() {
		if d.GoField.Name == "Notes" {
			notes = d.Field
		}
	}
	na, err := schema.AccessField(notes)
	if err != nil {
		t.Fatal(err)
	}
	if err := na.Set(&row, "kept"); err != nil || row.Notes != "kept" {
		t.Fatalf("wrote %q, %v", row.Notes, err)
	}
	// A nested section lists its own declarations; an unnamed field is a
	// leaf even where sections nest.
	type holder struct {
		Row  declRow `json:"row"`
		Blob declRow `json:"-"`
	}
	nested := schemaFor[holder](t, engine(t, gowalker.WithJSONFields("json", gowalker.KeepHidden(), gowalker.NestedFields())))
	ds := nested.Root().Declarations()
	if len(ds) != 2 || ds[1].State != gowalker.Unnamed || len(ds[1].Field.Shape().Declarations()) != 0 {
		t.Fatalf("holder declarations %v", ds)
	}
	if got := len(ds[0].Field.Shape().Declarations()); got != 9 {
		t.Errorf("row lists %d declarations, want 9", got)
	}
	// A shadowed field's handle reaches no accessor.
	for _, d := range schema.Root().Declarations() {
		if d.State == gowalker.Shadowed {
			if _, err := schema.AccessField(d.Field); !errors.Is(err, gowalker.ErrPath) {
				t.Errorf("shadowed %s: %v", d.GoField.Name, err)
			}
		}
	}
}

// DropTies marks the fields at the tie's rank Tied and deeper rivals Shadowed.
func TestDeclarations_Tied(t *testing.T) {
	schema, err := engine(t, gowalker.WithJSONFields("json"), gowalker.DropTies()).CompileType(deepTieType)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]gowalker.DeclarationState{}
	for _, d := range schema.Root().Declarations() {
		if d.GoField.Name == "X" {
			states[d.DeclaringType.Name()] = d.State
		}
	}
	want := map[string]gowalker.DeclarationState{"TieA": gowalker.Tied, "TieB": gowalker.Tied, "DeepX": gowalker.Shadowed}
	if !reflect.DeepEqual(states, want) {
		t.Errorf("states %v, want %v", states, want)
	}
}

// An unnamed rule takes no name, alias or inlining.
func TestFieldRule_UnnamedRefusesNames(t *testing.T) {
	for name, rule := range map[string]gowalker.FieldRule{
		"name":   {Unnamed: true, Name: "x"},
		"alias":  {Unnamed: true, Aliases: []string{"x"}},
		"inline": {Unnamed: true, Inline: true},
	} {
		e := engine(t, gowalker.WithFieldResolver(func(reflect.StructField) (gowalker.FieldRule, error) { return rule, nil }))
		if _, err := e.Compile[struct{ declBase }](); !errors.Is(err, gowalker.ErrSchema) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Skip still takes precedence.
	e := engine(t, gowalker.WithFieldResolver(func(reflect.StructField) (gowalker.FieldRule, error) {
		return gowalker.FieldRule{Unnamed: true, Name: "x", Skip: true}, nil
	}))
	if _, err := e.Compile[declBase](); err != nil {
		t.Error(err)
	}
}
