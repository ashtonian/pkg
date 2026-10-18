package config_test

import (
	"fmt"
	"reflect"

	"github.com/ashtonian/pkg/config"
)

func ExampleMatchFields() {
	type Settings struct {
		Port int `koanf:"port" desc:"HTTP port"`
	}
	cfg := Settings{Port: 8080}
	verbose := false
	fields, err := config.MatchFields(&cfg, []reflect.Value{
		reflect.ValueOf(&cfg.Port).Elem(),
		reflect.ValueOf(&verbose).Elem(),
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(fields[0].Path, fields[0].Tag.Get("desc"))
	fmt.Println(fields[1].Path == "")
	// Output:
	// port HTTP port
	// true
}
