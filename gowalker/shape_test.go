package gowalker_test

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashtonian/pkg/gowalker"
)

func engine(t testing.TB, options ...gowalker.Option) *gowalker.Engine {
	t.Helper()
	e, err := gowalker.New(options...)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func schemaFor[T any](t testing.TB, e *gowalker.Engine) *gowalker.Schema {
	t.Helper()
	s, err := e.Compile[T]()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func tagged(namespace string) gowalker.FieldResolver {
	return func(sf reflect.StructField) (gowalker.FieldRule, error) {
		name, options, _ := strings.Cut(sf.Tag.Get(namespace), ",")
		return gowalker.FieldRule{Name: name, Skip: name == "-", Inline: options == "inline"}, nil
	}
}

type recursive struct {
	Next     *recursive
	Children map[string][]*recursive
}

type recursivePointer *recursivePointer

func TestFiniteRecursiveGraph(t *testing.T) {
	e := engine(t)
	s := schemaFor[recursive](t, e)
	next, ok := s.Root().Lookup("Next")
	if !ok || next.Shape().Elem() != s.Root() {
		t.Fatal("recursive field did not reference root")
	}
	children, _ := s.Root().Lookup("Children")
	if children.Shape().Elem().Elem().Elem() != s.Root() {
		t.Fatal("recursive collection did not reference root")
	}
	p := schemaFor[recursivePointer](t, e)
	if p.Root().Elem() != p.Root() {
		t.Fatal("defined pointer recursion was expanded")
	}
	if _, err := p.ResolveText("NoField"); !errors.Is(err, gowalker.ErrPath) {
		t.Fatal(err)
	}
}

func TestIndependentPoliciesAndTags(t *testing.T) {
	type row struct {
		ID int `db:"id" json:"identifier" empty:"" escaped:"a\"b\\c"`
		//lint:ignore U1000 reflection fixture for hidden metadata
		hidden int
	}
	db := schemaFor[row](t, engine(t, gowalker.WithFieldResolver(tagged("db"))))
	json := schemaFor[row](t, engine(t, gowalker.WithFieldResolver(tagged("json"))))
	if _, ok := db.Root().Lookup("identifier"); ok {
		t.Fatal("policy leaked")
	}
	if _, ok := json.Root().Lookup("identifier"); !ok {
		t.Fatal("JSON policy missing")
	}
	fields := db.Root().Fields()
	if len(fields) != 1 || fields[0].Name() != "id" {
		t.Fatal(fields)
	}
	tag := fields[0].GoField().Tag
	if value, ok := tag.Lookup("empty"); !ok || value != "" {
		t.Fatal("lost present empty tag")
	}
	if _, ok := tag.Lookup("absent"); ok {
		t.Fatal("invented tag")
	}
	if tag.Get("escaped") != "a\"b\\c" {
		t.Fatal(tag.Get("escaped"))
	}
	if len(db.Root().DeclaredFields()) != 2 {
		t.Fatal("private metadata omitted")
	}
}

type embedded struct {
	Value int `db:"value"`
}
type EmbeddedPointer struct{ Value int }

func TestInlineRoutesAndMetadataImmutability(t *testing.T) {
	type row struct {
		embedded `db:",inline" sensitive:"true"`
		ID       int `db:"id"`
	}
	aliases := []string{"identifier"}
	e := engine(t, gowalker.WithFieldResolver(func(sf reflect.StructField) (gowalker.FieldRule, error) {
		rule, err := tagged("db")(sf)
		if sf.Name == "ID" {
			rule.Aliases = aliases
		}
		return rule, err
	}))
	s := schemaFor[row](t, e)
	aliases[0] = "corrupted"
	f, _ := s.Root().Lookup("value")
	if !reflect.DeepEqual(f.Index(), []int{0, 0}) {
		t.Fatal(f.Index())
	}
	if f.InlineParents()[0].Tag.Get("sensitive") != "true" {
		t.Fatal("inline tag lost")
	}
	id, ok := s.Root().Lookup("identifier")
	if !ok {
		t.Fatal("alias not copied")
	}
	f.Index()[0] = 42
	f.GoField().Index[0] = 42
	f.InlineParents()[0].Index[0] = 42
	id.Aliases()[0] = "corrupted"
	s.Root().DeclaredFields()[0].Index[0] = 42
	s.Root().Fields()[0] = nil
	if f.Index()[0] != 0 || f.GoField().Index[0] != 0 || f.InlineParents()[0].Index[0] != 0 || id.Aliases()[0] != "identifier" || s.Root().DeclaredFields()[0].Index[0] != 0 || s.Root().Fields()[0] == nil {
		t.Fatal("metadata backing storage exposed")
	}
	a, err := s.ResolveText("value")
	if err != nil {
		t.Fatal(err)
	}
	v, err := a.Get(row{embedded: embedded{Value: 7}})
	if err != nil || v.Interface() != 7 {
		t.Fatalf("exported embedded descendant: %v, %v", v, err)
	}
}

func TestInlinePointersAndCycles(t *testing.T) {
	type row struct {
		*EmbeddedPointer `db:",inline"`
	}
	e := engine(t, gowalker.WithFieldResolver(tagged("db")))
	s := schemaFor[row](t, e)
	if fields := s.Root().Fields(); len(fields) != 1 || fields[0].Name() != "Value" {
		t.Fatal(fields)
	}
	type cycle struct {
		//lint:ignore U1000 reflection fixture for recursive inline fields
		*cycle `db:",inline"`
	}
	if _, err := e.Compile[cycle](); !errors.Is(err, gowalker.ErrSchema) {
		t.Fatal(err)
	}
	type bad struct {
		N int `db:",inline"`
	}
	if _, err := e.Compile[bad](); !errors.Is(err, gowalker.ErrSchema) {
		t.Fatal(err)
	}
	type ignored struct {
		Hidden cycle `db:"-"`
	}
	schemaFor[ignored](t, e)
}

func TestSelectedDeclarationsRetainInlineOwners(t *testing.T) {
	type empty struct{}
	type row struct {
		embedded `db:",inline"`
		Empty    empty `db:",inline" sensitive:"invalid"`
		ID       int   `db:"id"`
		Skipped  int   `db:"-"`
		//lint:ignore U1000 reflection fixture for excluded fields
		private int
	}
	s := schemaFor[row](t, engine(t, gowalker.WithFieldResolver(tagged("db"))))
	declared := s.Root().DeclaredView()
	if len(declared) != 3 || !declared[0].Inline() || !declared[1].Inline() || declared[2].Inline() {
		t.Fatalf("selected declarations: %v", declared)
	}
	if declared[1].GoField().Tag.Get("sensitive") != "invalid" || len(declared[1].Shape().Fields()) != 0 {
		t.Fatal("empty inline owner lost its annotations")
	}
	fields := s.Root().Fields()
	if len(fields) != 2 || fields[1] != declared[2] || fields[0].DeclaringType() != reflect.TypeFor[embedded]() || declared[0].DeclaringType() != reflect.TypeFor[row]() {
		t.Fatal("field identity or original declaring type lost")
	}
	if children := declared[0].Shape().Fields(); len(children) != 1 || children[0].Name() != "value" {
		t.Fatal("inline owner's own view unavailable")
	}
	declared[0] = nil
	s.Root().DeclaredView()[1].GoField().Index[0] = 99
	if s.Root().DeclaredView()[0] == nil || s.Root().DeclaredView()[1].GoField().Index[0] != 1 {
		t.Fatal("selected declarations expose mutable storage")
	}
	atomic := schemaFor[row](t, engine(t, gowalker.WithAtomicType(func(typ reflect.Type) bool { return typ == reflect.TypeFor[row]() })))
	if len(atomic.Root().DeclaredView()) != 0 || len(atomic.Root().DeclaredFields()) != 5 {
		t.Fatal("atomic view should retain raw structure without selected fields")
	}
}

func TestNameConflictStructuredDiagnostics(t *testing.T) {
	type row struct{ A, B int }
	e := engine(t, gowalker.WithCaseFoldNames(), gowalker.WithFieldResolver(func(sf reflect.StructField) (gowalker.FieldRule, error) {
		if sf.Name == "A" {
			return gowalker.FieldRule{Name: "first", Aliases: []string{"s"}}, nil
		}
		return gowalker.FieldRule{Name: "ſ"}, nil
	}))
	_, err := e.Compile[row]()
	var conflict *gowalker.NameConflict
	if !errors.Is(err, gowalker.ErrSchema) || !errors.As(err, &conflict) {
		t.Fatal(err)
	}
	if conflict.Owner != reflect.TypeFor[row]() || conflict.Name != "ſ" || conflict.First.Name() != "first" || conflict.Second.GoField().Name != "B" {
		t.Fatalf("incorrect conflict: %+v", conflict)
	}
	want := `field B of gowalker_test.row uses the name "ſ", which is an alias of field A`
	if conflict.Error() != want || !strings.Contains(err.Error(), want) {
		t.Fatalf("diagnostic = %q, want it to contain %q", err.Error(), want)
	}
}

func TestCollisionsUseLookupEquivalence(t *testing.T) {
	for _, names := range [][2]string{{"Port", "PORT"}, {"K", "K"}, {"S", "ſ"}, {"σ", "ς"}, {"same", "same"}} {
		t.Run(strings.Join(names[:], "/"), func(t *testing.T) {
			type row struct{ A, B int }
			resolver := func(sf reflect.StructField) (gowalker.FieldRule, error) {
				if sf.Name == "A" {
					return gowalker.FieldRule{Name: names[0]}, nil
				}
				return gowalker.FieldRule{Name: "Other", Aliases: []string{names[1]}}, nil
			}
			_, err := engine(t, gowalker.WithFieldResolver(resolver), gowalker.WithCaseFoldNames()).Compile[row]()
			if !errors.Is(err, gowalker.ErrSchema) {
				t.Fatal(err)
			}
			if names[0] != names[1] {
				schemaFor[row](t, engine(t, gowalker.WithFieldResolver(resolver)))
			}
		})
	}
	s := schemaFor[struct{ S int }](t, engine(t, gowalker.WithCaseFoldNames()))
	if _, ok := s.Root().Lookup("ſ"); !ok {
		t.Fatal("lookup differs from collision checking")
	}
}

func TestAtomicPolicyStopsNamedTraversal(t *testing.T) {
	e := engine(t, gowalker.WithAtomicType(func(typ reflect.Type) bool { return typ == reflect.TypeFor[time.Time]() }), gowalker.WithFieldResolver(func(sf reflect.StructField) (gowalker.FieldRule, error) {
		if sf.PkgPath == "time" {
			t.Fatal("resolver called inside atomic type")
		}
		return gowalker.FieldRule{}, nil
	}))
	s := schemaFor[struct{ When *time.Time }](t, e)
	f, _ := s.Root().Lookup("When")
	if !f.Shape().Elem().Atomic() || len(f.Shape().Elem().Fields()) != 0 || len(f.Shape().Elem().DeclaredFields()) == 0 {
		t.Fatal("atomic/raw metadata contract")
	}
	if _, err := s.ResolveText("When.wall"); !errors.Is(err, gowalker.ErrPath) {
		t.Fatal(err)
	}
}

func TestCacheAndConcurrentPublication(t *testing.T) {
	e := engine(t, gowalker.WithCacheCapacity(1))
	const workers = 32
	results := make(chan *gowalker.Schema, workers)
	var group sync.WaitGroup
	for range workers {
		group.Go(func() {
			s, err := e.Compile[recursive]()
			if err != nil {
				t.Error(err)
				return
			}
			f, ok := s.Root().Lookup("Next")
			if !ok || f.Shape().Elem() != s.Root() {
				t.Error("partially published graph")
			}
			results <- s
		})
	}
	group.Wait()
	close(results)
	first := schemaFor[recursive](t, e)
	for s := range results {
		if s != first {
			t.Fatal("cache failed to converge")
		}
	}
	fullFirst, fullSecond := schemaFor[int](t, e), schemaFor[int](t, e)
	if fullFirst == fullSecond {
		t.Fatal("capacity exceeded")
	}
	uncached := engine(t, gowalker.WithCacheCapacity(0))
	uncachedFirst, uncachedSecond := schemaFor[int](t, uncached), schemaFor[int](t, uncached)
	if uncachedFirst == uncachedSecond {
		t.Fatal("zero capacity cached")
	}
}

func TestCompileErrorsAndBudgets(t *testing.T) {
	for _, options := range [][]gowalker.Option{{nil}, {gowalker.WithFieldResolver(nil)}, {gowalker.WithAtomicType(nil)}, {gowalker.WithCacheCapacity(-1)}, {gowalker.WithLimits(gowalker.Limits{})}} {
		if _, err := gowalker.New(options...); !errors.Is(err, gowalker.ErrSchema) {
			t.Fatal(err)
		}
	}
	if _, err := (*gowalker.Engine)(nil).CompileType(reflect.TypeFor[int]()); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	if _, err := new(gowalker.Engine).CompileType(reflect.TypeFor[int]()); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	if _, err := engine(t).CompileType(nil); !errors.Is(err, gowalker.ErrType) {
		t.Fatal(err)
	}
	for _, limits := range []gowalker.Limits{
		{MaxTypes: 1, MaxFields: 100, MaxDepth: 10, MaxPathSteps: 10},
		{MaxTypes: 100, MaxFields: 1, MaxDepth: 10, MaxPathSteps: 10},
		{MaxTypes: 100, MaxFields: 100, MaxDepth: 1, MaxPathSteps: 10},
	} {
		_, err := engine(t, gowalker.WithLimits(limits)).Compile[struct{ P **int }]()
		if !errors.Is(err, gowalker.ErrLimit) {
			t.Fatal(err)
		}
	}
	cause := errors.New("consumer policy failed")
	_, err := engine(t, gowalker.WithFieldResolver(func(reflect.StructField) (gowalker.FieldRule, error) { return gowalker.FieldRule{}, cause })).Compile[struct{ N int }]()
	if !errors.Is(err, cause) || !errors.Is(err, gowalker.ErrSchema) {
		t.Fatal(err)
	}
}

func TestBudgetBoundaries(t *testing.T) {
	limits := gowalker.DefaultLimits()
	limits.MaxTypes, limits.MaxFields = 2, 4
	schemaFor[struct{ A, B int }](t, engine(t, gowalker.WithLimits(limits)))
	limits.MaxFields = 3
	if _, err := engine(t, gowalker.WithLimits(limits)).Compile[struct{ A, B int }](); !errors.Is(err, gowalker.ErrLimit) {
		t.Fatal(err)
	}
	limits = gowalker.DefaultLimits()
	typ := reflect.TypeFor[int]()
	for range limits.MaxDepth {
		typ = reflect.PointerTo(typ)
	}
	if _, err := engine(t).CompileType(typ); err != nil {
		t.Fatal(err)
	}
	if _, err := engine(t).CompileType(reflect.PointerTo(typ)); !errors.Is(err, gowalker.ErrLimit) {
		t.Fatal(err)
	}
}
