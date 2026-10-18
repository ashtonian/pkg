package gowalker_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
)

func BenchmarkCompile(b *testing.B) {
	fields := make([]reflect.StructField, 128)
	for i := range fields {
		fields[i] = reflect.StructField{Name: fmt.Sprintf("Field%d", i), Type: reflect.TypeFor[string]()}
	}
	for _, fixture := range []struct {
		name string
		typ  reflect.Type
	}{
		{"recursive", reflect.TypeFor[recursive]()},
		{"128_fields", reflect.StructOf(fields)},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			e := engine(b, gowalker.WithCacheCapacity(0))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := e.CompileType(fixture.typ); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCachedCompile(b *testing.B) {
	e := engine(b)
	schemaFor[accessRoot](b, e)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := e.Compile[accessRoot](); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGet(b *testing.B) {
	row := accessRoot{Child: &child{N: 7}, Rows: map[string]child{"one": {N: 7}}, Dynamic: &child{N: 7}}
	schema := schemaFor[accessRoot](b, engine(b))
	for _, text := range []string{"Child.N", "Rows[\"one\"].N", "Dynamic.N"} {
		b.Run(text, func(b *testing.B) {
			a := accessor(b, schema, text)
			if _, err := a.Get(&row); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := a.Get(&row); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkSet(b *testing.B) {
	row := accessRoot{Child: &child{}, Rows: map[string]child{"one": {}}, Dynamic: child{}}
	schema := schemaFor[accessRoot](b, engine(b))
	for _, text := range []string{"Child.N", "Rows[\"one\"].N", "Dynamic.N"} {
		b.Run(text, func(b *testing.B) {
			a := accessor(b, schema, text)
			if err := a.Set(&row, 1); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := a.Set(&row, 2); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkWalk(b *testing.B) {
	row := accessRoot{Child: &child{}, Rows: map[string]child{"one": {}}, Dynamic: child{}}
	schema := schemaFor[accessRoot](b, engine(b))
	b.Run("type", func(b *testing.B) {
		visitor := func(gowalker.TypeVisit) (gowalker.Control, error) { return gowalker.Continue, nil }
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			if err := schema.WalkType(visitor); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("value", func(b *testing.B) {
		visitor := func(gowalker.ValueVisit) (gowalker.Control, error) { return gowalker.Continue, nil }
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			if err := schema.WalkValue(&row, visitor); err != nil {
				b.Fatal(err)
			}
		}
	})
}
