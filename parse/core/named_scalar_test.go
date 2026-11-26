//go:build unit

package core_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

type (
	namedText  string
	namedInt8  int8
	namedUint  uint16
	namedFloat float32
	namedFlag  bool
)

// A value of a named type normalizes as a value of its kind does: a server
// that builds a patch or a key from its own named types (type Active bool)
// is not refused as if a client sent the wrong type.
func TestNormalizeScalar_NamedScalars(t *testing.T) {
	flag := namedFlag(true)
	for _, tc := range []struct {
		name string
		dt   core.FieldType
		kind reflect.Kind
		in   any
		want any
	}{
		{"string", core.FieldTypeString, reflect.String, namedText("a"), "a"},
		{"signed integer", core.FieldTypeInt, reflect.Int8, namedInt8(-3), int64(-3)},
		{"unsigned integer", core.FieldTypeInt, reflect.Uint16, namedUint(7), uint64(7)},
		{"float", core.FieldTypeFloat, reflect.Float32, namedFloat(1.5), float64(1.5)},
		{"bool", core.FieldTypeBool, reflect.Bool, namedFlag(true), true},
		{"pointer to a named bool", core.FieldTypeBool, reflect.Bool, &flag, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := core.NormalizeScalar(tc.dt, tc.kind, tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	_, err := core.NormalizeScalar(core.FieldTypeBool, reflect.Bool, namedText("true"))
	assert.ErrorContains(t, err, "expected boolean, got core_test.namedText")
}
