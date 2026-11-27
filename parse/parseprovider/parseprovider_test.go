//go:build unit || integration

package parseprovider_test

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/parseprovider"
)

// defaults is the Config its default tags describe, as
// github.com/ashtonian/pkg/config loads it with no source set.
func defaults(t *testing.T) parseprovider.Config {
	t.Helper()
	var cfg parseprovider.Config
	applyDefaults(t, reflect.ValueOf(&cfg).Elem())
	return cfg
}

func applyDefaults(t *testing.T, v reflect.Value) {
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
			require.NoError(t, err, field.Name)
			value.SetInt(int64(d))
		case field.Type.Kind() == reflect.String:
			value.SetString(tag)
		case value.CanInt():
			n, err := strconv.ParseInt(tag, 10, 64)
			require.NoError(t, err, field.Name)
			value.SetInt(n)
		default:
			t.Fatalf("no default reader for %s %s", field.Name, field.Type)
		}
	}
}
