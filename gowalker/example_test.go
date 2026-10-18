package gowalker_test

import (
	"encoding"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/gowalker/path"
)

func ExampleGetAs() {
	type Database struct{ Port int }
	cfg := struct{ Database *Database }{Database: &Database{Port: 5432}}
	port, err := gowalker.GetAs[int](cfg, "Database.Port")
	if err != nil {
		panic(err)
	}
	fmt.Println(port)
	cfg.Database = nil
	value, err := gowalker.Get(cfg, "Database.Port")
	if err != nil {
		panic(err)
	}
	fmt.Println(value.State == gowalker.Missing, cfg.Database == nil)
	if err := gowalker.Set(&cfg, "Database.Port", 8080, gowalker.WithPointerAllocation()); err != nil {
		panic(err)
	}
	fmt.Println(cfg.Database.Port)
	// Output:
	// 5432
	// true true
	// 8080
}

func ExampleAccessor_Get() {
	type Row struct {
		ID int `db:"id"`
	}
	e, err := gowalker.New(gowalker.WithFieldResolver(func(field reflect.StructField) (gowalker.FieldRule, error) {
		name, _, _ := strings.Cut(field.Tag.Get("db"), ",")
		return gowalker.FieldRule{Name: name, Skip: name == "" || name == "-", Atomic: true}, nil
	}))
	if err != nil {
		panic(err)
	}
	schema, err := e.Compile[Row]()
	if err != nil {
		panic(err)
	}
	field, ok := schema.Root().Lookup("id")
	if !ok {
		panic("missing id field")
	}
	id, err := schema.AccessField(field)
	if err != nil {
		panic(err)
	}
	for _, row := range []Row{{ID: 10}, {ID: 20}} {
		value, err := id.Get(row)
		if err != nil {
			panic(err)
		}
		fmt.Println(value.Interface())
	}
	// Output:
	// 10
	// 20
}

func ExampleEngine_Compile() {
	type Database struct{ Port int }
	e, err := gowalker.New()
	if err != nil {
		panic(err)
	}
	schema, err := e.Compile[Database]()
	if err != nil {
		panic(err)
	}
	port, err := schema.ResolveText("Port")
	if err != nil {
		panic(err)
	}
	value, err := port.Get(Database{Port: 5432})
	if err != nil {
		panic(err)
	}
	fmt.Println(value.Interface())
	// Output: 5432
}

func ExampleAccessor_GetAs() {
	type Database struct{ Port int }
	e, err := gowalker.New()
	if err != nil {
		panic(err)
	}
	schema, err := e.Compile[Database]()
	if err != nil {
		panic(err)
	}
	port, err := schema.ResolveText("Port")
	if err != nil {
		panic(err)
	}
	value, err := port.GetAs[int](Database{Port: 5432})
	if err != nil {
		panic(err)
	}
	fmt.Println(value)
	// Output: 5432
}

func ExampleSchema_Resolve() {
	type Row struct{ Name string }
	input := map[string]any{"primary.eu": Row{Name: "primary"}}
	e, err := gowalker.New()
	if err != nil {
		panic(err)
	}
	schema, err := e.Compile[map[string]any]()
	if err != nil {
		panic(err)
	}
	name, err := schema.Resolve(path.Of(path.Key("primary.eu"), path.Field("Name")))
	if err != nil {
		panic(err)
	}
	value, err := name.Get(input)
	if err != nil {
		panic(err)
	}
	fmt.Println(value.Interface())
	if err := name.Set(&input, "updated"); err != nil {
		panic(err)
	}
	fmt.Println(input["primary.eu"].(Row).Name)
	// Output:
	// primary
	// updated
}

// This example is a consumer-owned naming policy, not a complete config decoder.
func ExampleWithFieldResolver() {
	type Database struct {
		Port    int       `koanf:"port" alias:"old_port,listen_port"`
		Started time.Time `koanf:"started"`
	}
	type Config struct {
		Database `koanf:",squash" sensitive:"true"`
		Ignored  string `koanf:"-"`
	}
	rule := func(field reflect.StructField) (gowalker.FieldRule, error) {
		name, options, _ := strings.Cut(field.Tag.Get("koanf"), ",")
		flags := strings.Split(options, ",")
		if name == "" {
			name = strings.ToLower(field.Name)
		}
		var aliases []string
		if value, ok := field.Tag.Lookup("alias"); ok {
			aliases = strings.Split(value, ",")
		}
		return gowalker.FieldRule{
			Name:    name,
			Aliases: aliases,
			Skip:    name == "-" || slices.Contains(flags, "remain"),
			Inline:  slices.Contains(flags, "squash"),
		}, nil
	}
	textDecoder := reflect.TypeFor[encoding.TextUnmarshaler]()
	e, err := gowalker.New(
		gowalker.WithFieldResolver(rule),
		gowalker.WithCaseFoldNames(),
		gowalker.WithAtomicType(func(t reflect.Type) bool {
			return t.Kind() == reflect.Struct && (t.Implements(textDecoder) || reflect.PointerTo(t).Implements(textDecoder))
		}),
	)
	if err != nil {
		panic(err)
	}
	schema, err := e.Compile[Config]()
	if err != nil {
		panic(err)
	}
	port, err := schema.ResolveText("OLD_PORT")
	if err != nil {
		panic(err)
	}
	cfg := Config{}
	if err := port.Set(&cfg, 5432); err != nil {
		panic(err)
	}
	value, err := port.Get(cfg)
	if err != nil {
		panic(err)
	}
	field, _ := schema.Root().Lookup("port")
	fmt.Println(value.Interface(), field.Index())
	fmt.Println(field.InlineParents()[0].Tag.Get("sensitive"))
	started, _ := schema.Root().Lookup("started")
	fmt.Println(started.Shape().Atomic())
	// Output:
	// 5432 [0 0]
	// true
	// true
}

func ExampleSet() {
	type Database struct{ Port int }
	var cfg struct{ Database *Database }
	if err := gowalker.Set(&cfg, "Database.Port", 5432, gowalker.WithPointerAllocation()); err != nil {
		panic(err)
	}
	fmt.Println(cfg.Database.Port)
	// Output: 5432
}

func ExampleSchema_WalkValue() {
	cfg := struct{ Ports map[string]int }{Ports: map[string]int{"api": 8080, "metrics": 9090}}
	e, err := gowalker.New()
	if err != nil {
		panic(err)
	}
	schema, err := e.Compile[struct{ Ports map[string]int }]()
	if err != nil {
		panic(err)
	}
	var locations []path.Path
	err = schema.WalkValue(&cfg, func(visit gowalker.ValueVisit) (gowalker.Control, error) {
		if visit.Kind == gowalker.Enter && visit.Shape.Kind() == reflect.Int {
			locations = append(locations, visit.Path)
		}
		return gowalker.Continue, nil
	})
	if err != nil {
		panic(err)
	}
	for _, location := range locations {
		port, err := schema.Resolve(location)
		if err != nil {
			panic(err)
		}
		current, err := port.GetAs[int](&cfg)
		if err != nil {
			panic(err)
		}
		if err := port.Set(&cfg, current+1); err != nil {
			panic(err)
		}
	}
	fmt.Println(cfg.Ports["api"], cfg.Ports["metrics"])
	// Output: 8081 9091
}

func ExampleSchema_WalkType() {
	type Node struct{ Next *Node }
	e, err := gowalker.New()
	if err != nil {
		panic(err)
	}
	schema, err := e.Compile[Node]()
	if err != nil {
		panic(err)
	}
	err = schema.WalkType(func(visit gowalker.TypeVisit) (gowalker.Control, error) {
		if visit.Kind == gowalker.Reference {
			fmt.Println(visit.Shape.Type() == reflect.TypeFor[Node]())
		}
		return gowalker.Continue, nil
	})
	if err != nil {
		panic(err)
	}
	// Output: true
}

func ExampleAccessor_Set_failure() {
	type Row struct{ Count uint8 }
	var cfg struct{ Row *Row }
	err := gowalker.Set(&cfg, "Row.Count", 256,
		gowalker.WithPointerAllocation(), gowalker.WithCheckedNumbers(),
	)
	fmt.Println(errors.Is(err, gowalker.ErrConversion), cfg.Row == nil)
	// Output: true true
}

func ExampleAccessor_SetValue() {
	var boxed any = 7
	e, err := gowalker.New()
	if err != nil {
		panic(err)
	}
	schema, err := e.Compile[any]()
	if err != nil {
		panic(err)
	}
	root, err := schema.Resolve(path.Path{})
	if err != nil {
		panic(err)
	}
	err = root.SetValue(reflect.ValueOf(&boxed).Elem(), reflect.Zero(reflect.TypeFor[any]()))
	if err != nil {
		panic(err)
	}
	fmt.Println(boxed == nil)
	// Output: true
}

func ExampleShape_DeclaredView() {
	type Config struct {
		Metadata struct{} `inline:"true" desc:"validate this empty owner"`
		Port     int
	}
	e, err := gowalker.New(gowalker.WithFieldResolver(func(field reflect.StructField) (gowalker.FieldRule, error) {
		return gowalker.FieldRule{Inline: field.Tag.Get("inline") == "true"}, nil
	}))
	if err != nil {
		panic(err)
	}
	schema, err := e.Compile[Config]()
	if err != nil {
		panic(err)
	}
	for _, declaration := range schema.Root().DeclaredView() {
		if declaration.Inline() {
			fmt.Println(declaration.GoField().Tag.Get("desc"))
		}
	}
	fmt.Println(len(schema.Root().Fields()))
	// Output:
	// validate this empty owner
	// 1
}

func ExampleSchema_AccessField() {
	type Row struct{ ID int }
	e, err := gowalker.New()
	if err != nil {
		panic(err)
	}
	s, err := e.Compile[Row]()
	if err != nil {
		panic(err)
	}
	field, ok := s.Root().Lookup("ID")
	if !ok {
		panic("missing ID")
	}
	id, err := s.AccessField(field)
	if err != nil {
		panic(err)
	}
	again, err := s.AccessField(field)
	if err != nil {
		panic(err)
	}
	value, err := id.GetAs[int](&Row{ID: 42})
	if err != nil {
		panic(err)
	}
	fmt.Println(value, id == again)
	// Output: 42 true
}

func ExampleGet_states() {
	type Row struct {
		Count  int
		Child  *Row
		Labels map[string]string
	}
	row := Row{}
	for _, location := range []string{"Count", "Child", "Child.Count", `Labels["absent"]`} {
		v, err := gowalker.Get(&row, location)
		if err != nil {
			panic(err)
		}
		fmt.Println(location, v.State == gowalker.Present, v.State == gowalker.Nil, v.State == gowalker.Missing)
	}
	// Output:
	// Count true false false
	// Child false true false
	// Child.Count false false true
	// Labels["absent"] false false true
}

func ExampleAccessor_GetAs_interface() {
	row := struct{ Value any }{Value: 42}
	e, err := gowalker.New()
	if err != nil {
		panic(err)
	}
	s, err := e.Compile[struct{ Value any }]()
	if err != nil {
		panic(err)
	}
	declared, err := s.ResolveText("Value")
	if err != nil {
		panic(err)
	}
	_, err = declared.GetAs[int](&row)
	fmt.Println(errors.Is(err, gowalker.ErrType))
	concrete, err := s.Resolve(path.Of(path.Field("Value"), path.Deref()))
	if err != nil {
		panic(err)
	}
	value, err := concrete.GetAs[int](&row)
	if err != nil {
		panic(err)
	}
	fmt.Println(value)
	// Output:
	// true
	// 42
}
