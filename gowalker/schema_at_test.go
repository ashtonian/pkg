package gowalker_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
)

type AtDeep struct{ Deep int }

type atChild struct {
	Value int
	*AtDeep
}

type atRoot struct {
	Items []atChild
	Named atChild
}

// At gives a node of a compiled graph its own schema, whose accessors read a
// value already in hand, such as a collection element, without compiling its
// type again.
func TestSchemaAt_ReadsRelativeToANode(t *testing.T) {
	e := engine(t, gowalker.WithJSONFields("json", gowalker.NestedFields()))
	root := schemaFor[atRoot](t, e)
	items, _ := root.Root().Lookup("Items")
	child, err := root.At(items.Shape().Elem())
	if err != nil {
		t.Fatal(err)
	}
	if child.Type() != reflect.TypeFor[atChild]() || child.Root() != items.Shape().Elem() {
		t.Fatalf("At rooted %v", child.Type())
	}
	value, _ := child.Root().Lookup("Value")
	if _, err := root.AccessField(value); !errors.Is(err, gowalker.ErrPath) {
		t.Errorf("the root accessed a field of its element: %v", err)
	}
	access, err := child.AccessField(value)
	if err != nil {
		t.Fatal(err)
	}
	v := atRoot{Items: []atChild{{Value: 7}}}
	if got, err := access.Get(v.Items[0]); err != nil || got.Interface() != 7 {
		t.Errorf("Value reads %v, %v", got.Interface(), err)
	}

	// Every schema of the graph returns one schema per node, so the accessor
	// it caches is shared, and nothing else is compiled.
	again, err := child.At(items.Shape().Elem())
	if err != nil || again != child {
		t.Errorf("At returned another schema: %v", err)
	}
	if repeat, _ := again.AccessField(value); repeat != access {
		t.Error("At's schema did not reuse its accessor")
	}
	if self, err := child.At(root.Root()); err != nil || self != root {
		t.Errorf("At(root) = %v, %v", self, err)
	}
	if stats := e.CacheStats(); stats.Entries != 1 {
		t.Errorf("cache holds %d types", stats.Entries)
	}

	// Two fields of one type share its node.
	named, _ := root.Root().Lookup("Named")
	if at, _ := root.At(named.Shape()); at != child {
		t.Error("two fields of one type have two schemas")
	}

	// A promoted field through a nil embedded pointer is missing, and a write
	// that may allocate it does.
	deep, err := child.AccessName("Deep")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := deep.Get(atChild{}); err != nil || got.State != gowalker.Missing {
		t.Errorf("Deep through a nil pointer: %v, %v", got.State, err)
	}
	if err := deep.Set(&v.Items[0], 3, gowalker.WithPointerAllocation()); err != nil || v.Items[0].AtDeep == nil || v.Items[0].Deep != 3 {
		t.Errorf("Deep wrote %+v, %v", v.Items[0], err)
	}

	// Its walks start at the node too.
	var walked []string
	err = child.WalkType(func(visit gowalker.TypeVisit) (gowalker.Control, error) {
		walked = append(walked, typeRoute(visit))
		return gowalker.Continue, nil
	})
	if want := []string{"", "Value", "Deep"}; err != nil || !slices.Equal(walked, want) {
		t.Errorf("walked %q, want %q: %v", walked, want, err)
	}
}

// At accepts only nodes its graph gives a view: not a node of another
// compilation, or a map key's.
func TestSchemaAt_RefusesShapesOutsideTheGraph(t *testing.T) {
	e := engine(t)
	root := schemaFor[atRoot](t, e)
	alone := schemaFor[atChild](t, engine(t))
	if _, err := root.At(alone.Root()); !errors.Is(err, gowalker.ErrPath) {
		t.Errorf("At took another compilation's node: %v", err)
	}
	if _, err := root.At(nil); !errors.Is(err, gowalker.ErrPath) {
		t.Errorf("At(nil): %v", err)
	}
	items, _ := root.Root().Lookup("Items")
	child, err := root.At(items.Shape().Elem())
	if err != nil {
		t.Fatal(err)
	}
	foreign, _ := alone.Root().Lookup("Value")
	if _, err := child.AccessField(foreign); !errors.Is(err, gowalker.ErrPath) {
		t.Errorf("At's schema accessed another compilation's field: %v", err)
	}

	type keyed struct {
		ByPoint map[AtDeep]int
	}
	keyedSchema := schemaFor[keyed](t, e)
	byPoint, _ := keyedSchema.Root().Lookup("ByPoint")
	if _, err := keyedSchema.At(byPoint.Shape().Key()); !errors.Is(err, gowalker.ErrPath) {
		t.Errorf("At took a map key: %v", err)
	}
	if _, err := keyedSchema.At(byPoint.Shape().Elem()); err != nil {
		t.Errorf("At refused a map element: %v", err)
	}
}

type atNode struct {
	V    int
	Next *atNode
}

// A field-specific leaf keeps its own node, a leaf for At as everywhere; a
// recursive type's node is the root's; access rules for shadowed, unnamed and
// private fields are the same through At.
func TestSchemaAt_KeepsLeavesRecursionAndAccessRules(t *testing.T) {
	type pair struct {
		Open  atChild `db:"open"`
		Fixed atChild `db:"fixed,leaf"`
	}
	leafy := engine(t, gowalker.WithFieldResolver(func(sf reflect.StructField) (gowalker.FieldRule, error) {
		tag := sf.Tag.Get("db")
		return gowalker.FieldRule{Atomic: tag == "fixed,leaf"}, nil
	}))
	schema := schemaFor[pair](t, leafy)
	open, _ := schema.Root().Lookup("Open")
	fixed, _ := schema.Root().Lookup("Fixed")
	openAt, err := schema.At(open.Shape())
	if err != nil {
		t.Fatal(err)
	}
	fixedAt, err := schema.At(fixed.Shape())
	if err != nil {
		t.Fatal(err)
	}
	if openAt == fixedAt || !fixedAt.Root().Atomic() {
		t.Fatal("a leaf field shares its type's node")
	}
	if _, err := openAt.AccessName("Value"); err != nil {
		t.Errorf("Open.Value: %v", err)
	}
	if _, err := fixedAt.AccessName("Value"); !errors.Is(err, gowalker.ErrPath) {
		t.Errorf("a leaf's field was accessed: %v", err)
	}

	nodes := schemaFor[atNode](t, engine(t))
	next, _ := nodes.Root().Lookup("Next")
	pointer, err := nodes.At(next.Shape())
	if err != nil {
		t.Fatal(err)
	}
	if self, _ := nodes.At(next.Shape().Elem()); self != nodes {
		t.Error("the recursive node has a second schema")
	}
	v, err := pointer.AccessName("V")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v.Get(&atNode{V: 3}); err != nil || got.Interface() != 3 {
		t.Errorf("V through the pointer node: %v, %v", got.Interface(), err)
	}

	type section struct {
		atChild `json:",inline"`
		Value   int    `json:"Value"`
		Hidden  string `json:"-"`
		_       int
	}
	type document struct {
		Sections []section
	}
	hidden := engine(t, gowalker.WithJSONFields("json", gowalker.KeepHidden(), gowalker.NestedFields()))
	doc := schemaFor[document](t, hidden)
	sections, _ := doc.Root().Lookup("Sections")
	at, err := doc.At(sections.Shape().Elem())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range at.Root().Declarations() {
		_, err := at.AccessField(d.Field)
		switch d.State {
		case gowalker.Named, gowalker.Unnamed:
			if err != nil {
				t.Errorf("%s (%v): %v", d.GoField.Name, d.State, err)
			}
		default:
			if !errors.Is(err, gowalker.ErrPath) {
				t.Errorf("%s (%v) was accessed: %v", d.GoField.Name, d.State, err)
			}
		}
	}
}

// Concurrent first requests for a node return one schema.
func TestSchemaAt_Concurrent(t *testing.T) {
	root := schemaFor[atRoot](t, engine(t))
	items, _ := root.Root().Lookup("Items")
	got := make([]*gowalker.Schema, 16)
	var wg sync.WaitGroup
	for i := range got {
		wg.Go(func() {
			at, err := root.At(items.Shape().Elem())
			if err != nil {
				t.Error(err)
			}
			got[i] = at
		})
	}
	wg.Wait()
	for _, at := range got {
		if at != got[0] {
			t.Fatal("concurrent At calls returned different schemas")
		}
	}
}

type atTieHolder struct {
	Fixed TieOwner `w:"fixed,leaf"`
}

type atTieRoot struct {
	First  TieOwnerRow `w:"first"`
	Second atTieHolder `w:"second"`
}

// A field-specific leaf of a type whose own names conflict is a leaf, even
// when the compilation met the conflict before it made the leaf.
func TestSchemaAt_LeafOfAConflictedType(t *testing.T) {
	e := engine(t, gowalker.ResolveByDepth(), gowalker.WithFieldResolver(func(sf reflect.StructField) (gowalker.FieldRule, error) {
		name, option, _ := strings.Cut(sf.Tag.Get("w"), ",")
		return gowalker.FieldRule{Name: name, Inline: sf.Anonymous, Atomic: option == "leaf", Explicit: name != ""}, nil
	}))
	schema := schemaFor[atTieRoot](t, e)
	second, _ := schema.Root().Lookup("second")
	holder, err := schema.At(second.Shape())
	if err != nil {
		t.Fatal(err)
	}
	fixed, _ := holder.Root().Lookup("fixed")
	leaf, err := schema.At(fixed.Shape())
	if err != nil || !leaf.Root().Atomic() {
		t.Errorf("At(the leaf) = %v", err)
	}
	if _, err := e.Compile[TieOwner](); !errors.Is(err, gowalker.ErrSchema) {
		t.Errorf("TieOwner compiled alone: %v", err)
	}
}

type atTieLeaf struct{ X int }

type atTieOverLeaf struct {
	TieOwnerA
	TieOwnerB
	L atTieLeaf
}

type atTieHolderOfLeaf struct {
	Config atTieOverLeaf
}

// atTieShadowsLeaf's Config shadows the conflicted struct above atTieLeaf.
type atTieShadowsLeaf struct {
	atTieHolderOfLeaf
	Config string
}

// A node from which a path reaches a struct keeping its conflict in place of
// a view, such as a pointer to it or a struct with a field of its type,
// returns the error compiling its type alone returns; a node below the
// conflict has a view of its own.
func TestSchemaAt_PathsToAConflict(t *testing.T) {
	strict := engine(t, gowalker.WithJSONFields("json"))
	pointerRow := reflect.StructOf([]reflect.StructField{
		{Name: "TieOwner", Type: reflect.TypeFor[*TieOwner](), Anonymous: true},
		{Name: "ID", Type: reflect.TypeFor[string]()},
	})
	schema, err := strict.CompileType(pointerRow)
	if err != nil {
		t.Fatal(err)
	}
	owner := schema.Root().Declarations()[0].Field.Shape()
	_, alone := strict.CompileType(owner.Type())
	for range 2 {
		if _, err := schema.At(owner); !errors.Is(err, gowalker.ErrSchema) || alone == nil || err.Error() != alone.Error() {
			t.Errorf("At(*TieOwner) = %v, want %v", err, alone)
		}
	}

	nested := engine(t, gowalker.WithJSONFields("json", gowalker.NestedFields()))
	shadows := schemaFor[atTieShadowsLeaf](t, nested)
	holder := shadows.Root().Declarations()[0].Field.Shape()
	if _, alone := nested.CompileType(holder.Type()); alone == nil {
		t.Fatal("the holder compiled alone")
	} else if _, err := shadows.At(holder); err == nil || err.Error() != alone.Error() {
		t.Errorf("At(holder) = %v, want %v", err, alone)
	}

	// Below the conflict the leaf struct has a view, which a declared-view
	// walk reaches.
	var leaf *gowalker.Shape
	var routes []string
	err = shadows.WalkType(func(v gowalker.TypeVisit) (gowalker.Control, error) {
		routes = append(routes, typeRoute(v))
		if v.Shape.Type() == reflect.TypeFor[atTieLeaf]() {
			leaf = v.Shape
		}
		return gowalker.Continue, nil
	}, gowalker.WithDeclaredView())
	if err != nil || !slices.Contains(routes, "(atTieHolderOfLeaf).Config.L.X") || leaf == nil {
		t.Fatalf("declared walk %q: %v", routes, err)
	}
	at, err := shadows.At(leaf)
	if err != nil {
		t.Fatal(err)
	}
	x, err := at.AccessName("X")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := x.Get(atTieLeaf{X: 4}); err != nil || got.Interface() != 4 {
		t.Errorf("X reads %v, %v", got.Interface(), err)
	}
	if self, err := shadows.At(shadows.Root()); err != nil || self != shadows {
		t.Errorf("At(root) in a graph with a conflict = %v", err)
	}
}

// A leaf a field reaches is a node; a map key, which no view reads, is not,
// leaf or not.
func TestSchemaAt_LeavesAndMapKeys(t *testing.T) {
	e := engine(t, gowalker.WithAtomicType(func(t reflect.Type) bool { return t == reflect.TypeFor[AtDeep]() }))
	fielded := schemaFor[struct{ Point AtDeep }](t, e)
	point, _ := fielded.Root().Lookup("Point")
	if at, err := fielded.At(point.Shape()); err != nil || !at.Root().Atomic() {
		t.Errorf("At(a leaf field) = %v", err)
	}
	keyed := schemaFor[struct{ ByPoint map[AtDeep]int }](t, e)
	byPoint, _ := keyed.Root().Lookup("ByPoint")
	if _, err := keyed.At(byPoint.Shape().Key()); !errors.Is(err, gowalker.ErrPath) {
		t.Errorf("At took a leaf map key: %v", err)
	}
}
