package gowalker_test

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
)

// distinctTypes returns n distinct struct types.
func distinctTypes(n int) []reflect.Type {
	types := make([]reflect.Type, n)
	for i := range types {
		types[i] = reflect.StructOf([]reflect.StructField{{Name: "F" + strconv.Itoa(i), Type: reflect.TypeFor[int]()}})
	}
	return types
}

// An unbounded engine keeps more types than the default capacity and returns
// one schema per type; a bounded one counts the compilations it does not keep.
func TestCacheStats(t *testing.T) {
	types := distinctTypes(300)
	// Each of two rounds compiles every type three times (twice directly,
	// once through the type cache) and builds it once.
	const rounds, compiles = 2, 3
	for _, tc := range []struct {
		name    string
		options []gowalker.Option
		kept    int
		want    gowalker.CacheStats
	}{
		{name: "default", kept: 256, want: gowalker.CacheStats{Capacity: 256}},
		{name: "unbounded", options: []gowalker.Option{gowalker.WithUnboundedCache()}, kept: 300, want: gowalker.CacheStats{Capacity: 256, Unbounded: true}},
		{name: "capacity after unbounded", options: []gowalker.Option{gowalker.WithUnboundedCache(), gowalker.WithCacheCapacity(10)}, kept: 10, want: gowalker.CacheStats{Capacity: 10}},
		{name: "unbounded after capacity", options: []gowalker.Option{gowalker.WithCacheCapacity(0), gowalker.WithUnboundedCache()}, kept: 300, want: gowalker.CacheStats{Unbounded: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := engine(t, tc.options...)
			builds := 0
			cache := gowalker.NewTypeCache(e, func(*gowalker.Schema) (int, error) { builds++; return builds, nil })
			for range rounds {
				for _, typ := range types {
					first, err := e.CompileType(typ)
					if err != nil {
						t.Fatal(err)
					}
					again, _ := e.CompileType(typ)
					if tc.want.Unbounded && first != again {
						t.Fatalf("%v compiled twice", typ)
					}
					if _, err := cache.Get(typ); err != nil {
						t.Fatal(err)
					}
				}
			}
			overflow := uint64(len(types) - tc.kept)
			want := tc.want
			want.Entries, want.Uncached = tc.kept, rounds*compiles*overflow
			if got := e.CacheStats(); got != want {
				t.Errorf("engine %+v, want %+v", got, want)
			}
			want.Uncached = rounds * overflow
			if got := cache.CacheStats(); got != want {
				t.Errorf("type cache %+v, want %+v", got, want)
			}
		})
	}
}
