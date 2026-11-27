package gowalker_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/gowalker/path"
)

func BenchmarkReadModes(b *testing.B) {
	row := accessRoot{Child: &child{N: 7}, Rows: map[string]child{}}
	s := schemaFor[accessRoot](b, engine(b))
	a := accessor(b, s, "Child.N")
	b.Run("typed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if n, err := a.GetAs[int](&row); err != nil || n != 7 {
				b.Fatal(n, err)
			}
		}
	})
	b.Run("typed_pointer", func(b *testing.B) {
		a := accessor(b, s, "Child")
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			if got, err := a.GetAs[*child](&row); err != nil || got != row.Child {
				b.Fatal(got, err)
			}
		}
	})
	b.Run("typed_nil_interface", func(b *testing.B) {
		a := accessor(b, s, "Dynamic")
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			if got, err := a.GetAs[any](&row); err != nil || got != nil {
				b.Fatal(got, err)
			}
		}
	})
	b.Run("reflected", func(b *testing.B) {
		root := reflect.ValueOf(&row).Elem()
		b.ReportAllocs()
		for b.Loop() {
			if _, err := a.GetValue(root); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("nil_ancestor", func(b *testing.B) {
		row := accessRoot{}
		b.ReportAllocs()
		for b.Loop() {
			v, err := a.Get(&row)
			if err != nil || v.State != gowalker.Missing {
				b.Fatal(v, err)
			}
		}
	})
	b.Run("missing_map_entry", func(b *testing.B) {
		a := accessor(b, s, `Rows["absent"].N`)
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			v, err := a.Get(&row)
			if err != nil || v.State != gowalker.Missing {
				b.Fatal(v, err)
			}
		}
	})
}

func BenchmarkWriteModes(b *testing.B) {
	s := schemaFor[accessRoot](b, engine(b))
	a := accessor(b, s, "Child.N")
	b.Run("allocate_pointer", func(b *testing.B) {
		row := accessRoot{}
		option := gowalker.WithPointerAllocation()
		b.ReportAllocs()
		for b.Loop() {
			row.Child = nil
			if err := a.Set(&row, 7, option); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("checked_number", func(b *testing.B) {
		row := accessRoot{Child: &child{}}
		option := gowalker.WithCheckedNumbers()
		b.ReportAllocs()
		for b.Loop() {
			if err := a.Set(&row, int64(7), option); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkResolve(b *testing.B) {
	s := schemaFor[accessRoot](b, engine(b))
	p := path.Of(path.Field("Rows"), path.Key("one"), path.Field("N"))
	b.Run("typed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := s.Resolve(p); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("text", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := s.ResolveText(`Rows["one"].N`); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkOneCall(b *testing.B) {
	row := accessRoot{Child: &child{N: 7}}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := gowalker.Get(&row, "Child.N"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWalkCollection(b *testing.B) {
	for _, size := range []int{0, 32, 1024} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			values := make(map[string]int, size)
			for i := range size {
				values[fmt.Sprintf("key-%04d", i)] = i
			}
			s := schemaFor[map[string]int](b, engine(b))
			visitor := func(gowalker.ValueVisit) (gowalker.Control, error) { return gowalker.Continue, nil }
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := s.WalkValue(&values, visitor); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkFullCache(b *testing.B) {
	types := make([]reflect.Type, 257)
	for i := range types {
		types[i] = reflect.StructOf([]reflect.StructField{{Name: "N", Type: reflect.TypeFor[int](), Tag: reflect.StructTag(fmt.Sprintf(`fixture:"%d"`, i))}})
	}
	e := engine(b)
	for _, typ := range types[:256] {
		if _, err := e.CompileType(typ); err != nil {
			b.Fatal(err)
		}
	}
	for _, fixture := range []struct {
		name string
		typ  reflect.Type
	}{{"hit", types[0]}, {"uncached", types[256]}} {
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := e.CompileType(fixture.typ); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkAccessField(b *testing.B) {
	s := schemaFor[accessRoot](b, engine(b))
	field, _ := s.Root().Lookup("Child")
	if _, err := s.AccessField(field); err != nil {
		b.Fatal(err)
	}
	b.Run("serial", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, err := s.AccessField(field)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, err := s.AccessField(field)
				if err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
}

func BenchmarkAccessName(b *testing.B) {
	s := schemaFor[accessRoot](b, engine(b))
	if _, err := s.AccessName("Child"); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.AccessName("Child"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFieldTags(b *testing.B) {
	type row struct {
		ID int `db:"id"`
	}
	s := schemaFor[row](b, engine(b))
	f, _ := s.Root().Lookup("ID")
	b.ReportAllocs()
	for b.Loop() {
		if value, ok := f.Tags().Lookup("db"); !ok || value != "id" {
			b.Fatal(value, ok)
		}
	}
}

func BenchmarkFieldTag(b *testing.B) {
	type row struct {
		ID int `db:"id,pk"`
	}
	s := schemaFor[row](b, engine(b))
	f, _ := s.Root().Lookup("ID")
	b.ReportAllocs()
	for b.Loop() {
		if tag, ok := f.Tag("db"); !ok || !tag.Has("pk") {
			b.Fatal(tag, ok)
		}
	}
}

func BenchmarkTypeCacheHit(b *testing.B) {
	cache := gowalker.NewTypeCache(engine(b), func(s *gowalker.Schema) (int, error) { return len(s.Root().Fields()), nil })
	if _, err := cache.For[accessRoot](); err != nil {
		b.Fatal(err)
	}
	b.Run("serial", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, err := cache.For[accessRoot]()
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	// Concurrent row scans read one type's entry from every goroutine.
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, err := cache.For[accessRoot]()
				if err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
}

func BenchmarkCachedCompileParallel(b *testing.B) {
	e := engine(b)
	_, err := e.Compile[accessRoot]()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := e.Compile[accessRoot]()
			if err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func BenchmarkLocate(b *testing.B) {
	s := schemaFor[accessRoot](b, engine(b))
	a := accessor(b, s, "Child.N")
	b.Run("through_pointer", func(b *testing.B) {
		row := accessRoot{Child: &child{}}
		root := reflect.ValueOf(&row).Elem()
		b.ReportAllocs()
		for b.Loop() {
			if _, err := a.Locate(root); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("allocate_pointer", func(b *testing.B) {
		row := accessRoot{}
		root := reflect.ValueOf(&row).Elem()
		option := gowalker.WithPointerAllocation()
		b.ReportAllocs()
		for b.Loop() {
			row.Child = nil
			if _, err := a.Locate(root, option); err != nil {
				b.Fatal(err)
			}
		}
	})
	// The same location by hand, as a library walking an index route does.
	b.Run("reflect_baseline", func(b *testing.B) {
		row := accessRoot{Child: &child{}}
		root := reflect.ValueOf(&row).Elem()
		b.ReportAllocs()
		for b.Loop() {
			v := root.Field(0)
			if v.IsNil() {
				b.Fatal("nil")
			}
			if !v.Elem().Field(0).CanSet() {
				b.Fatal("unsettable")
			}
		}
	})
}
