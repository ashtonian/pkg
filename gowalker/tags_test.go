package gowalker_test

import (
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
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

// TagKeys lists the keys reflect.StructTag reads, and refuses a tag it
// stops reading early or a key it reads only once.
func TestTagKeys(t *testing.T) {
	for _, tc := range []struct {
		tag    reflect.StructTag
		keys   []string
		offset int
		key    string
	}{
		{tag: ``, keys: nil, offset: -1},
		{tag: `json:"a"`, keys: []string{"json"}, offset: -1},
		{tag: `json:"a" db:"b"  parse:"filter,sort" `, keys: []string{"json", "db", "parse"}, offset: -1},
		{tag: `json:"a"db:"b"`, keys: []string{"json", "db"}, offset: -1},
		{tag: `x:"a\"b"`, keys: []string{"x"}, offset: -1},
		{tag: `json:"a" parse: "filter"`, offset: 9},
		{tag: `json:"a" parse`, offset: 9},
		{tag: `json:"a`, offset: 5},
		{tag: `json:a`, offset: 0},
		{tag: "json:\"a\"\tdb:\"b\"", offset: 8},
		{tag: `json:"\q"`, offset: 5},
		{tag: `json:"a" db:"b" json:"c"`, offset: 16, key: "json"},
	} {
		keys, err := gowalker.TagKeys(tc.tag)
		if tc.offset < 0 {
			if err != nil || !slices.Equal(keys, tc.keys) {
				t.Errorf("%q: keys %q, err %v; want %q", tc.tag, keys, err, tc.keys)
			}
			for _, key := range keys {
				if _, ok := tc.tag.Lookup(key); !ok {
					t.Errorf("%q: reflect cannot read %q", tc.tag, key)
				}
			}
			continue
		}
		tagErr, ok := errors.AsType[*gowalker.TagError](err)
		if !ok || tagErr.Offset != tc.offset || tagErr.Key != tc.key || tagErr.Tag != tc.tag {
			t.Errorf("%q: %#v, want offset %d key %q", tc.tag, err, tc.offset, tc.key)
		}
	}
}

type tagKeysInner struct {
	Deep string `env:"DEEP" default:""`
}

// Every declaration, and every field handle, yields its tag's keys in order,
// including keys with empty values: named, unnamed, inline owner, promoted,
// shadowed and skipped fields alike.
func TestTagKeys_OnDeclarationsAndFields(t *testing.T) {
	type row struct {
		ID           int    `json:"id" db:"id,pk" parse:"filter"`
		Hidden       string `json:"-" db:"hidden"`
		Plain        int
		tagKeysInner `json:",inline" note:""`
		Deep         string `json:"Deep" env:""`
		_            int    `db:"private" check:""`
	}
	schema := schemaFor[row](t, engine(t, gowalker.WithJSONFields("json", gowalker.KeepHidden())))
	want := map[string][]string{
		"ID": {"json", "db", "parse"}, "Hidden": {"json", "db"}, "Plain": nil, "tagKeysInner": {"json", "note"},
		"row.Deep": {"json", "env"}, "tagKeysInner.Deep": {"env", "default"}, "_": {"db", "check"},
	}
	states := map[string]gowalker.DeclarationState{}
	for _, d := range schema.Root().Declarations() {
		name := d.GoField.Name
		if name == "Deep" {
			name = d.DeclaringType.Name() + ".Deep"
		}
		states[name] = d.State
		if got := slices.Collect(d.TagKeys()); !slices.Equal(got, want[name]) {
			t.Errorf("%s declaration: keys %q, want %q", name, got, want[name])
		}
		if d.Field == nil {
			continue
		}
		if got := slices.Collect(d.Field.TagKeys()); !slices.Equal(got, want[name]) {
			t.Errorf("%s field: keys %q, want %q", name, got, want[name])
		}
	}
	wantStates := map[string]gowalker.DeclarationState{
		"ID": gowalker.Named, "Hidden": gowalker.Unnamed, "Plain": gowalker.Named, "tagKeysInner": gowalker.InlineOwner,
		"row.Deep": gowalker.Named, "tagKeysInner.Deep": gowalker.Shadowed, "_": gowalker.Skipped,
	}
	if !maps.Equal(states, wantStates) {
		t.Errorf("states %v, want %v", states, wantStates)
	}
	for _, f := range schema.Root().DeclaredView() {
		if got := slices.Collect(f.TagKeys()); !slices.Equal(got, want[f.GoField().Name]) && f.GoField().Name != "Deep" {
			t.Errorf("%s declared view: keys %q", f.GoField().Name, got)
		}
	}
	// A copied declaration yields the same keys; one the caller builds, none.
	first := schema.Root().Declarations()[0]
	copied := first
	if got := slices.Collect(copied.TagKeys()); !slices.Equal(got, want["ID"]) {
		t.Errorf("copied declaration: keys %q", got)
	}
	if got := slices.Collect(gowalker.Declaration{GoField: first.GoField}.TagKeys()); got != nil {
		t.Errorf("built declaration: keys %q", got)
	}
	// What a caller does with a collected slice leaves the schema's keys alone.
	collected := slices.Collect(first.TagKeys())
	collected[0] = "changed"
	if got := slices.Collect(schema.Root().Declarations()[0].TagKeys()); !slices.Equal(got, want["ID"]) {
		t.Errorf("keys changed through a collected slice: %q", got)
	}
}

// Compilation refuses a malformed or repeated tag on any field the engine
// reads, a skipped one included, naming the type and the field.
func TestCompileRefusesUnreadableTags(t *testing.T) {
	for _, field := range []reflect.StructField{
		{Name: "A", Type: reflect.TypeFor[string](), Tag: `json:"a" parse: "filter,sort"`},
		{Name: "B", Type: reflect.TypeFor[string](), Tag: `json:"b" json:"c"`},
		{Name: "C", Type: reflect.TypeFor[string](), Tag: `json:"-" default:"x`},
		{Name: "d", Type: reflect.TypeFor[string](), PkgPath: "example.com/p", Tag: `db: "d"`},
	} {
		typ := reflect.StructOf([]reflect.StructField{{Name: "OK", Type: reflect.TypeFor[int](), Tag: `json:"ok"`}, field})
		for _, e := range []*gowalker.Engine{gowalker.Default(), engine(t, gowalker.WithJSONFields("json"))} {
			_, err := e.CompileType(typ)
			if _, ok := errors.AsType[*gowalker.TagError](err); !ok || !errors.Is(err, gowalker.ErrSchema) {
				t.Fatalf("%s: compiled: %v", field.Name, err)
			}
			if !strings.Contains(err.Error(), "field "+field.Name+":") || !strings.Contains(err.Error(), "struct { OK int; "+field.Name+" string }") {
				t.Errorf("%s: %v does not name the field and type", field.Name, err)
			}
		}
	}
	// A leaf's own fields are not read, so their tags are not checked.
	opaque := reflect.StructOf([]reflect.StructField{{Name: "X", Type: reflect.TypeFor[int](), Tag: `bad tag`}})
	holder := reflect.StructOf([]reflect.StructField{{Name: "Leaf", Type: opaque, Tag: `json:"leaf"`}})
	if _, err := engine(t, gowalker.WithJSONFields("json")).CompileType(holder); err != nil {
		t.Errorf("a leaf's own tags were checked: %v", err)
	}
}
