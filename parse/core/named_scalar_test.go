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

// A value of a named string type normalizes as its string does for every
// type a string spells: a server's `type Stamp string` on a timestamp
// field reads as the timestamp it holds, as the driver binds it.
func TestNormalizeScalar_NamedStrings(t *testing.T) {
	for _, tc := range []struct {
		dt   core.FieldType
		text string
	}{
		{core.FieldTypeString, "a"},
		{core.FieldTypeUUID, "0198A3C2-7A1E-7C00-8000-00000000000A"},
		{core.FieldTypeDecimal, "1.50"},
		{core.FieldTypeTimestamp, "2026-01-02T03:04:05+01:00"},
		{core.FieldTypeDate, "2026-01-02"},
		{core.FieldTypeTime, "03:04:05.500"},
		{core.FieldTypeInterval, "P1DT2H"},
		{core.FieldTypeInet, "10.0.0.1/8"},
		{core.FieldTypeCIDR, "10.0.0.0/8"},
		{core.FieldTypeMACAddr, "08-00-2B-01-02-03"},
	} {
		t.Run(string(tc.dt), func(t *testing.T) {
			want, err := core.NormalizeScalar(tc.dt, reflect.Invalid, tc.text)
			require.NoError(t, err)
			named := namedText(tc.text)
			for _, in := range []any{named, &named} {
				got, err := core.NormalizeScalar(tc.dt, reflect.Invalid, in)
				require.NoError(t, err, "%#v", in)
				assert.Equal(t, want, got, "%#v", in)
				stored, err := core.NormalizeStored(tc.dt, reflect.Invalid, in)
				require.NoError(t, err, "%#v", in)
				assert.Equal(t, want, stored, "%#v", in)
			}
		})
	}
	_, err := core.NormalizeScalar(core.FieldTypeDecimal, reflect.Invalid, namedText("cheap"))
	require.Error(t, err, "a named string is read as its text, not accepted whatever it holds")
}
