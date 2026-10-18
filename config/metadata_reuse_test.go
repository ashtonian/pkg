package config

import (
	"errors"
	"reflect"
	"slices"
	"testing"
)

func TestFieldCatalogCopiesProtectCachedMetadata(t *testing.T) {
	type section struct {
		Port int
		Host string
	}
	type cfg struct {
		DB      section `koanf:"db" format:"json"`
		Enabled bool
	}
	first, err := FieldsChecked[cfg]()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 {
		t.Fatal(first)
	}
	first[0].Index[0] = 99
	first[1].Index = append(first[1].Index, 99)
	first[0].Path = "changed"
	again, err := FieldsChecked[cfg]()
	if err != nil || again[0].Path != "db.port" || !slices.Equal(again[0].Index, []int{0, 0}) || !slices.Equal(again[2].Index, []int{1}) {
		t.Fatal(again, err)
	}
	variables := variableFields(reflect.TypeFor[cfg]())
	if !slices.Equal(fieldPaths(variables), []string{"db", "db.port", "db.host", "enabled"}) {
		t.Fatal(variables)
	}
	variables[0].Index[0] = 99
	if got := variableFields(reflect.TypeFor[cfg]()); got[0].Index[0] != 0 {
		t.Fatal(got)
	}
}

func TestMatchFieldsAnnotationsTargetsAndPointerChanges(t *testing.T) {
	type child struct {
		Value inspectionText `koanf:"value" example:"example"`
	}
	type cfg struct {
		Child *child `koanf:"child" sensitive:"true"`
		Skip  int    `koanf:"-"`
	}
	c := cfg{Child: &child{Value: "live"}}
	old := c.Child
	target := reflect.ValueOf(&c.Child.Value).Elem()
	before := inspectionDecodeCalls.Load()
	matches, err := MatchFields(&c, []reflect.Value{target, target, reflect.Value{}, reflect.ValueOf(1), reflect.ValueOf(&c.Skip).Elem()})
	if err != nil {
		t.Fatal(err)
	}
	if matches[0].Path != "child.value" || !matches[0].Sensitive || matches[0].Tag.Get("example") != "example" {
		t.Fatal(matches)
	}
	for _, i := range []int{2, 3, 4} {
		if matches[i].Path != "" {
			t.Fatal(matches)
		}
	}
	matches[0].Index[0] = 99
	if matches[1].Index[0] != 0 {
		t.Fatal("returned indexes alias")
	}
	if _, err := KeyOf(&c, &c.Child.Value); err != nil {
		t.Fatal(err)
	}
	if _, err := FieldFor[cfg]("child.value"); err != nil {
		t.Fatal(err)
	}
	if got := inspectionDecodeCalls.Load() - before; got != 0 {
		t.Fatalf("inspection decoded examples %d times", got)
	}
	c.Child = &child{Value: "new"}
	matches, err = MatchFields(&c, []reflect.Value{reflect.ValueOf(&old.Value).Elem(), reflect.ValueOf(&c.Child.Value).Elem()})
	if err != nil || matches[0].Path != "" || matches[1].Path != "child.value" {
		t.Fatal(matches, err)
	}
}

func TestMatchFieldsRejectsAmbiguityAndInvalidSchemas(t *testing.T) {
	type child struct{ Port int }
	c := struct{ First, Second *child }{}
	c.First = &child{}
	c.Second = c.First
	if _, err := MatchFields(&c, []reflect.Value{reflect.ValueOf(&c.First.Port).Elem()}); !errors.Is(err, ErrKey) {
		t.Fatal(err)
	}
	if _, err := MatchFields[child](nil, nil); !errors.Is(err, ErrKey) {
		t.Fatal(err)
	}
	if _, err := MatchFields(&conflictingInspection{}, nil); !errors.Is(err, ErrSchema) {
		t.Fatal(err)
	}
	i := 1
	if _, err := MatchFields(&i, nil); !errors.Is(err, ErrSchema) {
		t.Fatal(err)
	}
}

func BenchmarkFieldsChecked(b *testing.B) {
	type settings struct {
		Port  int
		Label string
		Debug bool
	}
	if _, err := FieldsChecked[settings](); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := FieldsChecked[settings](); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMatchFields(b *testing.B) {
	var cfg struct{ A, B, C, D, E, F, G, H int }
	root := reflect.ValueOf(&cfg).Elem()
	targets := make([]reflect.Value, root.NumField())
	for i := range targets {
		targets[i] = root.Field(i)
	}
	if _, err := MatchFields(&cfg, targets); err != nil {
		b.Fatal(err)
	}
	b.Run("batch", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := MatchFields(&cfg, targets); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("individual", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for _, target := range targets {
				if _, err := KeyOfValue(&cfg, target); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
}
