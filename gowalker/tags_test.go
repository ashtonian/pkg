package gowalker_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
)

func TestTaggedFieldsSelectionAndLiteralAccess(t *testing.T) {
	type embedded struct {
		Promoted int `db:"promoted"`
	}
	type payload struct {
		A int `db:"same"`
		B int `db:"same"`
	}
	type row struct {
		embedded
		ID      int     `db:"id.part,option" default:""`
		Data    payload `db:"payload"`
		Pointer *int    `db:"pointer"`
		Spaced  int     `db:" spaced "`
		Empty   int     `db:",option"`
		Skip    int     `db:"-,option"`
		Absent  int
		private int `db:"id.part"`
	}
	e := engine(t, gowalker.WithTaggedFields("db"))
	s := schemaFor[**row](t, e)
	owner := s.Root().Elem().Elem()
	if got := len(owner.Fields()); got != 4 {
		t.Fatalf("selected %d fields", got)
	}
	f, _ := owner.Lookup("id.part")
	if text, present := f.Tags().Lookup("default"); text != "" || !present {
		t.Fatal("lost explicit empty tag")
	}
	if _, present := f.Tags().Lookup("env"); present {
		t.Fatal("invented absent tag")
	}
	a, err := s.AccessName("id.part")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.AccessField(f)
	if err != nil || a != again {
		t.Fatal("name and field did not share accessor", err)
	}
	r := &row{ID: 42, private: 99}
	if got, err := a.GetAs[int](&r); err != nil || got != 42 {
		t.Fatal(got, err)
	}
	r = nil
	if got, err := a.Get(&r); err != nil || got.State != gowalker.Missing {
		t.Fatal(got, err)
	}
	for _, name := range []string{"id", "ID", "promoted", "Empty", "Skip", "Absent", "spaced", "payload.same"} {
		if _, err := s.AccessName(name); !errors.Is(err, gowalker.ErrPath) {
			t.Fatalf("%q: %v", name, err)
		}
	}
	if _, err := s.AccessName(" spaced "); err != nil {
		t.Fatal(err)
	}
	p, err := s.AccessName("pointer")
	if err != nil {
		t.Fatal(err)
	}
	r = &row{}
	if got, err := p.GetAs[*int](&r); err != nil || got != nil {
		t.Fatal(got, err)
	}
	if _, err := s.ResolveText("payload.same"); !errors.Is(err, gowalker.ErrPath) {
		t.Fatal(err)
	}
	if _, err := e.Compile[payload](); !errors.Is(err, gowalker.ErrSchema) {
		t.Fatal("duplicate root names accepted", err)
	}
}

func TestAccessNameAliasesAndInvalidRoots(t *testing.T) {
	e := engine(t, gowalker.WithFieldResolver(func(sf reflect.StructField) (gowalker.FieldRule, error) {
		return gowalker.FieldRule{Name: sf.Name, Aliases: []string{"alias"}}, nil
	}), gowalker.WithCaseFoldNames())
	s := schemaFor[struct{ ID int }](t, e)
	a, err := s.AccessName("ALIAS")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AccessName("id")
	if err != nil || a != b {
		t.Fatal("aliases did not reuse accessor", err)
	}
	if _, err := schemaFor[int](t, e).AccessName("N"); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	type pointer *pointer
	if _, err := schemaFor[pointer](t, e).AccessName("N"); !errors.Is(err, gowalker.ErrPath) {
		t.Fatal(err)
	}
	atomic := engine(t, gowalker.WithAtomicType(func(reflect.Type) bool { return true }))
	if _, err := schemaFor[struct{ N int }](t, atomic).AccessName("N"); !errors.Is(err, gowalker.ErrPath) {
		t.Fatal(err)
	}
}

func TestTaggedFieldsRejectInvalidTagKeys(t *testing.T) {
	for _, tag := range []string{"", "db:", "has space", "has\"quote", "db\x7f", "db\n"} {
		if _, err := gowalker.New(gowalker.WithTaggedFields(tag)); !errors.Is(err, gowalker.ErrSchema) {
			t.Fatalf("%q: %v", tag, err)
		}
	}
}
