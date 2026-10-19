package gowalker_test

import (
	"encoding"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
)

// check is a minimal assertion shim: the module keeps a standard-library-only
// dependency graph, tests included.
type check struct{}

var assert check

func (check) Equal(t testing.TB, want, got any, msg ...any) bool {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Errorf("%v: got %#v, want %#v", fmt.Sprint(msg...), got, want)
		return false
	}
	return true
}

func (check) Same(t testing.TB, want, got any, msg ...any) {
	t.Helper()
	if reflect.ValueOf(want).Pointer() != reflect.ValueOf(got).Pointer() {
		t.Errorf("%v: got %p, want the same pointer %p", fmt.Sprint(msg...), got, want)
	}
}

func (check) True(t testing.TB, ok bool, msg ...any) {
	t.Helper()
	if !ok {
		t.Errorf("%v: want true", fmt.Sprint(msg...))
	}
}

func (check) False(t testing.TB, ok bool, msg ...any) {
	t.Helper()
	if ok {
		t.Errorf("%v: want false", fmt.Sprint(msg...))
	}
}

func (check) NoError(t testing.TB, err error, msg ...any) bool {
	t.Helper()
	if err != nil {
		t.Errorf("%v: unexpected error: %v", fmt.Sprint(msg...), err)
		return false
	}
	return true
}

func (check) ErrorIs(t testing.TB, err, target error, msg ...any) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Errorf("%v: error %v does not match %v", fmt.Sprint(msg...), err, target)
	}
}

func (check) ErrorAs(t testing.TB, err error, target any) bool {
	t.Helper()
	if !errors.As(err, target) {
		t.Errorf("error %v is not a %T", err, target)
		return false
	}
	return true
}

func (check) EqualError(t testing.TB, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

func (check) Panics(t testing.TB, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("did not panic")
		}
	}()
	fn()
}

func TestParseTag(t *testing.T) {
	tests := []struct {
		name, value, wantName, wantOptions string
		has, hasNot                        []string
	}{
		{name: "name only", value: "id", wantName: "id", hasNot: []string{"id", ""}},
		{name: "options", value: "id,omitempty,string", wantName: "id", wantOptions: "omitempty,string", has: []string{"omitempty", "string"}, hasNot: []string{"omit", " omitempty"}},
		{name: "no name", value: ",squash", wantOptions: "squash", has: []string{"squash"}},
		{name: "whitespace kept", value: " id , pk", wantName: " id ", wantOptions: " pk", has: []string{" pk"}, hasNot: []string{"pk"}},
		{name: "empty", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tag := gowalker.ParseTag(tt.value)
			assert.Equal(t, tt.wantName, tag.Name)
			assert.Equal(t, tt.wantOptions, tag.Options)
			for _, option := range tt.has {
				assert.True(t, tag.Has(option), option)
			}
			for _, option := range tt.hasNot {
				assert.False(t, tag.Has(option), option)
			}
		})
	}
}

func TestLookupTagAndFieldTag(t *testing.T) {
	type row struct {
		Empty  int `db:""`
		Absent int
		ID     int `db:"id,pk"`
	}
	s := schemaFor[row](t, engine(t))
	fields := s.Root().Fields()

	tag, ok := fields[0].Tag("db")
	assert.True(t, ok, "an explicitly empty tag is present")
	assert.Equal(t, gowalker.Tag{}, tag)

	_, ok = fields[1].Tag("db")
	assert.False(t, ok)

	tag, ok = gowalker.LookupTag(fields[2].Tags(), "db")
	assert.True(t, ok)
	assert.Equal(t, "id", tag.Name)
	assert.True(t, tag.Has("pk"))
}

type stamp struct{ Unix int64 }

func (s *stamp) UnmarshalText(b []byte) error { return nil }

func TestTaggedFieldsConventions(t *testing.T) {
	type Base struct {
		ID int `json:"id"`
	}
	type Audit struct {
		By string `json:"by"`
	}
	type Server struct {
		Port int `koanf:"port"`
	}
	// window has no methods: embedding a type with methods, such as
	// time.Time, promotes them and can make the outer struct a leaf itself.
	type window struct{ Days int }
	type document struct {
		Base
		*Audit
		window
		Named Base   `json:"named"`
		Name  string // untagged
		Note  string `json:",omitempty"`
		Skip  string `json:"-"`
	}
	type config struct {
		Server Server
		Label  string `koanf:"label"`
	}
	identity := func(name string) string { return name }

	tests := []struct {
		name    string
		options []gowalker.Option
		compile func(*gowalker.Engine) (*gowalker.Schema, error)
		want    []string
		paths   map[string]error
	}{
		{
			name:    "explicit names only by default",
			options: []gowalker.Option{gowalker.WithTaggedFields("json")},
			compile: func(e *gowalker.Engine) (*gowalker.Schema, error) { return e.Compile[document]() },
			want:    []string{"named"},
			paths:   map[string]error{"named": nil, "named.id": gowalker.ErrPath},
		},
		{
			name: "json-style: untagged Go names and promoted embeds",
			options: []gowalker.Option{
				gowalker.WithTaggedFields("json", gowalker.UntaggedName(identity), gowalker.PromoteEmbedded()),
				gowalker.WithAtomicType(func(t reflect.Type) bool { return t == reflect.TypeFor[window]() }),
			},
			compile: func(e *gowalker.Engine) (*gowalker.Schema, error) { return e.Compile[document]() },
			want:    []string{"id", "by", "named", "Name", "Note"},
			paths:   map[string]error{"id": nil, "by": nil, "window": gowalker.ErrPath, "Days": gowalker.ErrPath},
		},
		{
			name:    "koanf-style: lowercase untagged names, nested sections",
			options: []gowalker.Option{gowalker.WithTaggedFields("koanf", gowalker.UntaggedName(strings.ToLower), gowalker.NestedFields())},
			compile: func(e *gowalker.Engine) (*gowalker.Schema, error) { return e.Compile[config]() },
			want:    []string{"server", "label"},
			paths:   map[string]error{"server.port": nil, "label": nil},
		},
		{
			name:    "sections are whole values without NestedFields",
			options: []gowalker.Option{gowalker.WithTaggedFields("koanf", gowalker.UntaggedName(strings.ToLower))},
			compile: func(e *gowalker.Engine) (*gowalker.Schema, error) { return e.Compile[config]() },
			want:    []string{"server", "label"},
			paths:   map[string]error{"server": nil, "server.port": gowalker.ErrPath},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := tt.compile(engine(t, tt.options...))
			if !assert.NoError(t, err) {
				return
			}
			var got []string
			for _, f := range s.Root().Fields() {
				got = append(got, f.Name())
			}
			assert.Equal(t, tt.want, got)
			for text, want := range tt.paths {
				_, err := s.ResolveText(text)
				if want == nil {
					assert.NoError(t, err, text)
				} else {
					assert.ErrorIs(t, err, want, text)
				}
			}
		})
	}
}

func TestTaggedFieldsPromotedCollisionNamesBothRoutes(t *testing.T) {
	type Base struct {
		ID int `col:"id"`
	}
	type Audit struct {
		ID int `col:"id"`
	}
	type row struct {
		Base
		Audit
	}
	e := engine(t, gowalker.WithTaggedFields("col", gowalker.PromoteEmbedded()))
	_, err := e.Compile[row]()
	assert.ErrorIs(t, err, gowalker.ErrSchema)
	var conflict *gowalker.NameConflict
	if assert.ErrorAs(t, err, &conflict) {
		assert.Equal(t, `fields Base.ID and Audit.ID of gowalker_test.row both use the name "id"`, conflict.Error())
	}
}

func TestTaggedFieldsOptionValidationAndPrecedence(t *testing.T) {
	_, err := gowalker.New(gowalker.WithTaggedFields("db", gowalker.UntaggedName(nil)))
	assert.ErrorIs(t, err, gowalker.ErrSchema)
	_, err = gowalker.New(gowalker.WithTaggedFields("db", nil))
	assert.ErrorIs(t, err, gowalker.ErrSchema)

	type row struct {
		ID int `db:"id"`
	}
	resolver := gowalker.WithFieldResolver(func(sf reflect.StructField) (gowalker.FieldRule, error) {
		return gowalker.FieldRule{Name: "resolved"}, nil
	})
	last := schemaFor[row](t, engine(t, gowalker.WithTaggedFields("db"), resolver))
	_, ok := last.Root().Lookup("resolved")
	assert.True(t, ok, "the later resolver applies")
	last = schemaFor[row](t, engine(t, resolver, gowalker.WithTaggedFields("db")))
	_, ok = last.Root().Lookup("id")
	assert.True(t, ok, "the later tag convention applies")
}

func TestAtomicImplementers(t *testing.T) {
	for _, ifaces := range [][]reflect.Type{nil, {reflect.TypeFor[int]()}, {nil}} {
		_, err := gowalker.New(gowalker.WithAtomicImplementers(ifaces...))
		assert.ErrorIs(t, err, gowalker.ErrSchema, fmt.Sprint(ifaces))
	}

	type window struct{ Days int }
	type row struct {
		At     stamp
		AtPtr  *stamp
		Window window
		Plain  struct{ N int }
	}
	e := engine(t,
		gowalker.WithAtomicImplementers(reflect.TypeFor[encoding.TextUnmarshaler]()),
		gowalker.WithAtomicType(func(t reflect.Type) bool { return t == reflect.TypeFor[window]() }),
	)
	s := schemaFor[row](t, e)
	for text, want := range map[string]error{
		"At": nil, "At.Unix": gowalker.ErrPath, "AtPtr.Unix": gowalker.ErrPath,
		"Window": nil, "Window.Days": gowalker.ErrPath, "Plain.N": nil,
	} {
		_, err := s.ResolveText(text)
		if want == nil {
			assert.NoError(t, err, text)
		} else {
			assert.ErrorIs(t, err, want, "leaf rules accumulate: "+text)
		}
	}
}

func TestTypeVisitInherited(t *testing.T) {
	type tls struct {
		Cert string
		Key  string `reload:"boot"`
	}
	type shared struct {
		Window int
	}
	type listener struct {
		Shared shared `inline:"true"`
		TLS    tls
		Port   int
	}
	type root struct {
		Listener listener `reload:"hot"`
		Limit    int
	}
	e := engine(t, gowalker.WithFieldResolver(func(sf reflect.StructField) (gowalker.FieldRule, error) {
		return gowalker.FieldRule{Inline: sf.Tag.Get("inline") == "true"}, nil
	}))
	got := map[string]string{}
	err := schemaFor[root](t, e).WalkType(func(v gowalker.TypeVisit) (gowalker.Control, error) {
		var names []string
		for _, edge := range v.Route {
			if edge.Field != nil {
				names = append(names, edge.Field.Name())
			}
		}
		if class, ok := v.Inherited("reload"); ok {
			got[strings.Join(names, ".")] = class
		}
		return gowalker.Continue, nil
	})
	assert.NoError(t, err)
	assert.Equal(t, map[string]string{
		"Listener":          "hot",
		"Listener.Window":   "hot",
		"Listener.TLS":      "hot",
		"Listener.TLS.Cert": "hot",
		"Listener.TLS.Key":  "boot",
		"Listener.Port":     "hot",
	}, got)
}

func TestTypeVisitInheritedFromInlineOwner(t *testing.T) {
	type credentials struct {
		Token string
	}
	type root struct {
		credentials `sensitive:"true"`
		Name        string
	}
	e := engine(t, gowalker.WithFieldResolver(func(sf reflect.StructField) (gowalker.FieldRule, error) {
		return gowalker.FieldRule{Inline: sf.Anonymous}, nil
	}))
	got := map[string]bool{}
	err := schemaFor[root](t, e).WalkType(func(v gowalker.TypeVisit) (gowalker.Control, error) {
		if len(v.Route) == 1 {
			_, sensitive := v.Inherited("sensitive")
			got[v.Route[0].Field.Name()] = sensitive
		}
		return gowalker.Continue, nil
	})
	assert.NoError(t, err)
	assert.Equal(t, map[string]bool{"Token": true, "Name": false}, got)
}

func TestTypeCache(t *testing.T) {
	type row struct {
		ID   int `db:"id"`
		Name int `db:"name"`
	}
	type broken struct {
		A int `db:"same"`
		B int `db:"same"`
	}
	var builds atomic.Int64
	columns := func(s *gowalker.Schema) ([]string, error) {
		builds.Add(1)
		var names []string
		for _, f := range s.Root().Fields() {
			names = append(names, f.Name())
		}
		return names, nil
	}
	cache := gowalker.NewTypeCache(engine(t, gowalker.WithTaggedFields("db")), columns)

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			got, err := cache.For[row]()
			assert.NoError(t, err)
			assert.Equal(t, []string{"id", "name"}, got)
		})
	}
	wg.Wait()
	before := builds.Load()
	_, err := cache.Get(reflect.TypeFor[row]())
	assert.NoError(t, err)
	assert.Equal(t, before, builds.Load(), "a cached type is not rebuilt")

	_, err = cache.For[broken]()
	assert.ErrorIs(t, err, gowalker.ErrSchema)
	_, err = cache.For[broken]()
	assert.ErrorIs(t, err, gowalker.ErrSchema, "compile errors are retained")

	failing := gowalker.NewTypeCache(engine(t), func(*gowalker.Schema) (int, error) { return 0, errors.ErrUnsupported })
	_, err = failing.For[row]()
	assert.ErrorIs(t, err, errors.ErrUnsupported, "build errors are returned as built")

	builds.Store(0)
	uncached := gowalker.NewTypeCache(engine(t, gowalker.WithCacheCapacity(0)), columns)
	for range 3 {
		_, err := uncached.For[row]()
		assert.NoError(t, err)
	}
	assert.Equal(t, int64(3), builds.Load(), "zero capacity retains nothing")

	assert.Panics(t, func() { gowalker.NewTypeCache[int](nil, func(*gowalker.Schema) (int, error) { return 0, nil }) })
	assert.Panics(t, func() { gowalker.NewTypeCache[int](engine(t), nil) })
}

func TestDefaultEngine(t *testing.T) {
	type row struct{ ID int }
	first, err := gowalker.Compile[row]()
	assert.NoError(t, err)
	second, err := gowalker.CompileType(reflect.TypeFor[row]())
	assert.NoError(t, err)
	assert.Same(t, first, second, "package functions share the default engine's cache")
	viaEngine, err := gowalker.Default().Compile[row]()
	assert.NoError(t, err)
	assert.Same(t, first, viaEngine)

	assert.Panics(t, func() { gowalker.MustNew(gowalker.WithCacheCapacity(-1)) })
}

func TestCompileErrorTextNamesTypeAndCause(t *testing.T) {
	type row struct{ ID int }
	e := engine(t, gowalker.WithFieldResolver(func(sf reflect.StructField) (gowalker.FieldRule, error) {
		return gowalker.FieldRule{}, errors.New("field ID: unsupported tag")
	}))
	_, err := e.Compile[row]()
	assert.ErrorIs(t, err, gowalker.ErrSchema)
	assert.EqualError(t, err, "gowalker: compile gowalker_test.row: invalid schema: field ID: unsupported tag")
}

func TestNameConflictWording(t *testing.T) {
	type row struct{ A, B int }
	tests := []struct {
		name string
		a, b gowalker.FieldRule
		want string
	}{
		{"names", gowalker.FieldRule{Name: "x"}, gowalker.FieldRule{Name: "x"}, `fields A and B of gowalker_test.row both use the name "x"`},
		{"aliases", gowalker.FieldRule{Name: "a", Aliases: []string{"x"}}, gowalker.FieldRule{Name: "b", Aliases: []string{"x"}}, `fields A and B of gowalker_test.row both have the alias "x"`},
		{"alias of a name", gowalker.FieldRule{Name: "x"}, gowalker.FieldRule{Name: "b", Aliases: []string{"x"}}, `field B of gowalker_test.row has the alias "x", which is the name of field A`},
		{"name of an alias", gowalker.FieldRule{Name: "a", Aliases: []string{"x"}}, gowalker.FieldRule{Name: "x"}, `field B of gowalker_test.row uses the name "x", which is an alias of field A`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := engine(t, gowalker.WithFieldResolver(func(sf reflect.StructField) (gowalker.FieldRule, error) {
				if sf.Name == "A" {
					return tt.a, nil
				}
				return tt.b, nil
			}))
			_, err := e.Compile[row]()
			var conflict *gowalker.NameConflict
			if assert.ErrorAs(t, err, &conflict) {
				assert.Equal(t, tt.want, conflict.Error())
			}
		})
	}
}
