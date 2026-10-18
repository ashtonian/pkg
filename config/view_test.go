package config

import (
	"reflect"
	"sync"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/stretchr/testify/require"
)

type ViewInlineCycle struct {
	Next *ViewInlineCycle `koanf:",squash"`
}

func TestViewRejectsRecursiveSquash(t *testing.T) {
	_, err := Load[ViewInlineCycle](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	require.ErrorIs(t, err, gowalker.ErrSchema)
}

func TestViewRejectsFoldedCanonicalCollisions(t *testing.T) {
	for _, names := range [][2]string{{"Port", "PORT"}, {"S", "ſ"}, {"K", "K"}, {"σ", "ς"}} {
		t.Run(names[0]+"/"+names[1], func(t *testing.T) {
			typ := reflect.StructOf([]reflect.StructField{
				{Name: "A", Type: reflect.TypeFor[int](), Tag: reflect.StructTag(`koanf:"` + names[0] + `"`)},
				{Name: "B", Type: reflect.TypeFor[int](), Tag: reflect.StructTag(`koanf:"` + names[1] + `"`)},
			})
			_, err := schemaOf(typ)
			require.ErrorIs(t, err, ErrSchema)
			var conflict *gowalker.NameConflict
			require.ErrorAs(t, err, &conflict)
			require.Equal(t, names[1], conflict.Name)
		})
	}
}

func TestViewValidatesEmptySquashOwnerTags(t *testing.T) {
	type config struct {
		Empty struct{} `koanf:",squash" sensitive:"yes"`
	}
	_, err := Load[config](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	require.Contains(t, err.Error(), `sensitive:"yes"`)
}

func TestViewAccessPreservesCapabilitiesAndMissingState(t *testing.T) {
	type Inner struct {
		Port int `koanf:"port"`
	}
	type config struct {
		*Inner `koanf:",squash" sensitive:"true"`
	}
	n, err := rootSchema(reflect.TypeFor[config]())
	require.NoError(t, err)
	f := n.byKey["port"]
	require.True(t, f.tagged)
	var missing config
	_, err = f.value(reflect.ValueOf(&missing).Elem())
	require.ErrorIs(t, err, gowalker.ErrMissing)
	require.Nil(t, missing.Inner)
	got := config{Inner: &Inner{Port: 80}}
	v, err := f.value(reflect.ValueOf(&got).Elem())
	require.NoError(t, err)
	require.True(t, v.CanAddr())
	require.True(t, v.CanSet())
	require.Same(t, &got.Port, v.Addr().Interface())
}

func TestViewConcurrentPublicationAndCopiedCatalog(t *testing.T) {
	type config struct {
		Port int `koanf:"port" alias:"listen"`
	}
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			n, err := rootSchema(reflect.TypeFor[config]())
			if err != nil {
				t.Error(err)
				return
			}
			f, alias := n.lookup("LISTEN")
			if f == nil || !alias || f.key != "port" {
				t.Errorf("lookup returned %v, %v", f, alias)
			}
			fields := Fields[config]()
			fields[0].Index[0] = 99
		})
	}
	wg.Wait()
	require.Equal(t, []int{0}, Fields[config]()[0].Index)
}
