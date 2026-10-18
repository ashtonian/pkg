package gowalker_test

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
)

func TestFieldAtomicKeepsColumnContentsOpaque(t *testing.T) {
	type payload struct {
		A int `db:"duplicate"`
		B int `db:"duplicate"`
	}
	type row struct {
		ID      int     `db:"id"`
		Payload payload `db:"payload"`
	}
	e := engine(t, gowalker.WithFieldResolver(func(f reflect.StructField) (gowalker.FieldRule, error) {
		name, _, _ := strings.Cut(f.Tag.Get("db"), ",")
		return gowalker.FieldRule{Name: name, Skip: name == "" || name == "-", Atomic: true}, nil
	}))
	s := schemaFor[row](t, e)
	f, _ := s.Root().Lookup("id")
	a, err := s.AccessField(f)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.GetAs[int](&row{ID: 7})
	if err != nil || got != 7 {
		t.Fatal(got, err)
	}
	p, _ := s.Root().Lookup("payload")
	if !p.Shape().Atomic() || len(p.Shape().DeclaredFields()) != 2 {
		t.Fatal("leaf lost raw structure")
	}
	if _, err := s.ResolveText("payload.duplicate"); !errors.Is(err, gowalker.ErrPath) {
		t.Fatal(err)
	}
	if _, err := e.Compile[payload](); !errors.Is(err, gowalker.ErrSchema) {
		t.Fatalf("root collision not rejected: %v", err)
	}
}

func TestFieldAtomicDoesNotChangeOtherUsesOfType(t *testing.T) {
	type child struct{ N int }
	type root struct {
		Leaf    child `leaf:"true"`
		Open    child
		Pointer *child `leaf:"true"`
		Dynamic any    `leaf:"true"`
	}
	e := engine(t, gowalker.WithFieldResolver(func(f reflect.StructField) (gowalker.FieldRule, error) {
		return gowalker.FieldRule{Atomic: f.Tag.Get("leaf") == "true"}, nil
	}))
	s := schemaFor[root](t, e)
	value := root{Leaf: child{1}, Open: child{2}, Pointer: &child{3}, Dynamic: child{4}}
	for _, name := range []string{"Leaf", "Pointer", "Dynamic"} {
		if _, err := s.ResolveText(name + ".N"); !errors.Is(err, gowalker.ErrPath) {
			t.Fatal(name, err)
		}
	}
	if err := accessor(t, s, "Leaf").Set(&value, child{5}); err != nil || value.Leaf.N != 5 {
		t.Fatal(value, err)
	}
	if err := accessor(t, s, "Open.N").Set(&value, 6); err != nil || value.Open.N != 6 {
		t.Fatal(value, err)
	}
	leaves, descendants := 0, 0
	if err := s.WalkValue(&value, func(v gowalker.ValueVisit) (gowalker.Control, error) {
		if v.Shape.Atomic() {
			leaves++
		}
		if v.Shape.Type() == reflect.TypeFor[int]() {
			descendants++
		}
		return gowalker.Continue, nil
	}); err != nil {
		t.Fatal(err)
	}
	if leaves != 3 || descendants != 1 {
		t.Fatal(leaves, descendants)
	}
	if _, err := e.Compile[child](); err != nil {
		t.Fatal(err)
	}
}

func TestAtomicFieldIsLeafEvenWhenItReferencesAncestor(t *testing.T) {
	type node struct{ Next *node }
	e := engine(t, gowalker.WithFieldResolver(func(reflect.StructField) (gowalker.FieldRule, error) {
		return gowalker.FieldRule{Atomic: true}, nil
	}))
	s := schemaFor[*node](t, e)
	n := &node{}
	n.Next = n
	types, values := 0, 0
	if err := s.WalkType(func(v gowalker.TypeVisit) (gowalker.Control, error) {
		if v.Kind != gowalker.Enter {
			t.Fatalf("leaf emitted %v", v.Kind)
		}
		types++
		return gowalker.Continue, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WalkValue(n, func(v gowalker.ValueVisit) (gowalker.Control, error) {
		if v.Kind != gowalker.Enter {
			t.Fatalf("leaf emitted %v", v.Kind)
		}
		values++
		return gowalker.Continue, nil
	}); err != nil {
		t.Fatal(err)
	}
	if types != 3 || values != 3 {
		t.Fatal(types, values)
	}
}

func TestAtomicInlineRuleRejected(t *testing.T) {
	type child struct{ N int }
	type root struct{ child }
	for _, skip := range []bool{false, true} {
		e := engine(t, gowalker.WithFieldResolver(func(reflect.StructField) (gowalker.FieldRule, error) {
			return gowalker.FieldRule{Atomic: true, Inline: true, Skip: skip}, nil
		}))
		_, err := e.Compile[root]()
		if skip && err != nil || !skip && !errors.Is(err, gowalker.ErrSchema) {
			t.Fatal(skip, err)
		}
	}
}

func TestAccessFieldHandlesAliasesInlineRootsAndForeignFields(t *testing.T) {
	type inner struct {
		Port int `db:"PORT"`
	}
	type root struct {
		*inner `db:",inline"`
		Nested inner
	}
	e := engine(t, gowalker.WithFieldResolver(tagged("db")), gowalker.WithCaseFoldNames())
	s := schemaFor[*root](t, e)
	owner := s.Root().Elem()
	f, ok := owner.Lookup("port")
	if !ok {
		t.Fatal("missing port")
	}
	a, err := s.AccessField(f)
	if err != nil {
		t.Fatal(err)
	}
	value := &root{inner: &inner{Port: 80}}
	got, err := a.GetAs[int](value)
	if err != nil || got != 80 {
		t.Fatal(got, err)
	}
	if err := a.Set(&value, 81); err != nil || value.Port != 81 {
		t.Fatal(value, err)
	}
	var group sync.WaitGroup
	for range 24 {
		group.Go(func() {
			next, err := s.AccessField(f)
			if err != nil || next != a {
				t.Error(next, err)
			}
		})
	}
	group.Wait()
	nested, _ := owner.Lookup("Nested")
	foreign := schemaFor[root](t, e).Root().Fields()[0]
	for _, invalid := range []*gowalker.Field{nil, owner.DeclaredView()[0], nested.Shape().Fields()[0], foreign} {
		if _, err := s.AccessField(invalid); !errors.Is(err, gowalker.ErrPath) {
			t.Fatal(err)
		}
	}
}
