//go:build unit || integration

package providertest

import (
	"reflect"
	"strconv"
	"testing"
	"time"
)

// Defaults is the T its default tags describe, as
// github.com/ashtonian/pkg/config loads a provider's Config with no source
// set: each field tagged default holds that value, nested structs included.
func Defaults[T any](t testing.TB) T {
	t.Helper()
	var cfg T
	applyDefaults(t, reflect.ValueOf(&cfg).Elem())
	return cfg
}

func applyDefaults(t testing.TB, v reflect.Value) {
	t.Helper()
	for i := range v.NumField() {
		field, value := v.Type().Field(i), v.Field(i)
		if field.Type.Kind() == reflect.Struct {
			applyDefaults(t, value)
			continue
		}
		tag, ok := field.Tag.Lookup("default")
		if !ok {
			continue
		}
		switch {
		case field.Type == reflect.TypeFor[time.Duration]():
			d, err := time.ParseDuration(tag)
			if err != nil {
				t.Fatalf("providertest: %s's default: %v", field.Name, err)
			}
			value.SetInt(int64(d))
		case field.Type.Kind() == reflect.String:
			value.SetString(tag)
		case value.CanInt():
			n, err := strconv.ParseInt(tag, 10, 64)
			if err != nil {
				t.Fatalf("providertest: %s's default: %v", field.Name, err)
			}
			value.SetInt(n)
		default:
			t.Fatalf("providertest: no default reader for %s %s", field.Name, field.Type)
		}
	}
}
