//go:build unit

package core

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeInt(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		kind    reflect.Kind
		want    any
		wantErr bool
	}{
		{name: "2^53+1 literal stays exact", in: json.Number("9007199254740993"), kind: reflect.Int64, want: int64(9007199254740993)},
		{name: "max int64", in: json.Number("9223372036854775807"), kind: reflect.Invalid, want: int64(math.MaxInt64)},
		{name: "int64 overflow", in: json.Number("9223372036854775808"), kind: reflect.Int64, wantErr: true},
		{name: "uint64 above int64", in: json.Number("9223372036854775808"), kind: reflect.Uint64, want: uint64(1 << 63)},
		{name: "max uint64", in: json.Number("18446744073709551615"), kind: reflect.Uint64, want: uint64(math.MaxUint64)},
		{name: "uint64 overflow", in: json.Number("18446744073709551616"), kind: reflect.Uint64, wantErr: true},
		{name: "int8 max", in: json.Number("127"), kind: reflect.Int8, want: int64(127)},
		{name: "int8 overflow", in: json.Number("128"), kind: reflect.Int8, wantErr: true},
		{name: "int8 min", in: json.Number("-128"), kind: reflect.Int8, want: int64(-128)},
		{name: "int8 underflow", in: json.Number("-129"), kind: reflect.Int8, wantErr: true},
		{name: "int16 overflow", in: json.Number("32768"), kind: reflect.Int16, wantErr: true},
		{name: "int32 overflow", in: json.Number("2147483648"), kind: reflect.Int32, wantErr: true},
		{name: "uint8 max", in: json.Number("255"), kind: reflect.Uint8, want: uint64(255)},
		{name: "uint8 overflow", in: json.Number("256"), kind: reflect.Uint8, wantErr: true},
		{name: "unsigned rejects negative", in: json.Number("-1"), kind: reflect.Uint64, wantErr: true},
		{name: "uint16 overflow", in: json.Number("65536"), kind: reflect.Uint16, wantErr: true},
		{name: "uint32 overflow", in: json.Number("4294967296"), kind: reflect.Uint32, wantErr: true},
		{name: "exponent integer", in: json.Number("1e3"), kind: reflect.Int64, want: int64(1000)},
		{name: "trailing zero fraction", in: json.Number("42.0"), kind: reflect.Int64, want: int64(42)},
		{name: "fraction rejected", in: json.Number("3.7"), kind: reflect.Int64, wantErr: true},
		{name: "exponent overflow", in: json.Number("1e400"), kind: reflect.Int64, wantErr: true},
		{name: "huge negative exponent bounded", in: json.Number("1e-1000000000"), kind: reflect.Int64, wantErr: true},
		{name: "integral float within 2^53", in: float64(1 << 53), kind: reflect.Int64, want: int64(1 << 53)},
		{name: "float beyond 2^53 lost precision", in: float64(1<<53) + 2, kind: reflect.Int64, wantErr: true},
		{name: "fractional float", in: 3.7, kind: reflect.Int, wantErr: true},
		{name: "NaN", in: math.NaN(), kind: reflect.Int64, wantErr: true},
		{name: "native int8 widens", in: int8(-5), kind: reflect.Int64, want: int64(-5)},
		{name: "native uint64 above int64 into int64", in: uint64(math.MaxUint64), kind: reflect.Int64, wantErr: true},
		{name: "named integer type", in: namedInt(7), kind: reflect.Int32, want: int64(7)},
		{name: "string is not a number", in: "5", kind: reflect.Int64, wantErr: true},
		{name: "null", in: nil, kind: reflect.Int64, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got any
			var err error
			if isUnsignedKind(tt.kind) {
				got, err = NormalizeUint(tt.in, tt.kind)
			} else {
				got, err = NormalizeInt(tt.in, tt.kind)
			}
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeUint_Unkinded(t *testing.T) {
	got, err := NormalizeUint(json.Number("18446744073709551615"), reflect.Invalid)
	require.NoError(t, err)
	assert.Equal(t, uint64(math.MaxUint64), got)

	_, err = NormalizeUint(json.Number("-1"), reflect.Invalid)
	assert.ErrorContains(t, err, "-1 is negative; uint64 is unsigned")
}

func TestNormalizeInteger_KindOfTheOtherSign(t *testing.T) {
	_, err := NormalizeInt(json.Number("1"), reflect.Uint8)
	assert.ErrorContains(t, err, "use NormalizeUint")
	_, err = NormalizeUint(json.Number("1"), reflect.Int8)
	assert.ErrorContains(t, err, "use NormalizeInt")
}

type namedInt int16

func TestNormalizeFloat(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		kind    reflect.Kind
		want    float64
		wantErr bool
	}{
		{name: "literal", in: json.Number("1.25"), kind: reflect.Float64, want: 1.25},
		{name: "float32 literal rounds once", in: json.Number("0.1"), kind: reflect.Float32, want: float64(float32(0.1))},
		{name: "float32 native value rounds", in: 0.1, kind: reflect.Float32, want: float64(float32(0.1))},
		{name: "float32 integer rounds", in: int64(16777217), kind: reflect.Float32, want: 16777216},
		{name: "float32 large integer rounds once", in: int64(1<<62 + 1<<38 + 1), kind: reflect.Float32, want: float64(float32(int64(1<<62 + 1<<38 + 1)))},
		{name: "float32 literal avoids double rounding", in: json.Number("1.0000000596046448"), kind: reflect.Float32, want: float64(math.Nextafter32(1, 2))},
		{name: "float32 largest shortest literal", in: json.Number("3.4028235e38"), kind: reflect.Float32, want: math.MaxFloat32},
		{name: "named float", in: namedFloat(1.5), kind: reflect.Float64, want: 1.5},
		{name: "integer literal", in: json.Number("7"), kind: reflect.Float64, want: 7},
		{name: "native int", in: 3, kind: reflect.Float64, want: 3},
		{name: "float32 overflow", in: json.Number("1e39"), kind: reflect.Float32, wantErr: true},
		{name: "float64 overflow", in: json.Number("1e400"), kind: reflect.Float64, wantErr: true},
		{name: "infinity", in: math.Inf(1), kind: reflect.Float64, wantErr: true},
		{name: "string", in: "1.5", kind: reflect.Float64, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeFloat(tt.in, tt.kind)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCompareNumbers(t *testing.T) {
	tests := []struct {
		name string
		a, b any
		want int
		ok   bool
	}{
		{name: "2^53 neighbours differ", a: int64(9007199254740993), b: int64(9007199254740992), want: 1, ok: true},
		{name: "int vs float at 2^53+1", a: int64(9007199254740993), b: float64(9007199254740992), want: 1, ok: true},
		{name: "json.Number vs native", a: json.Number("9007199254740993"), b: uint64(9007199254740993), want: 0, ok: true},
		{name: "uint64 above int64 vs int64", a: uint64(math.MaxUint64), b: int64(math.MaxInt64), want: 1, ok: true},
		{name: "negative vs uint64", a: int64(-1), b: uint64(1 << 63), want: -1, ok: true},
		{name: "int vs fractional float", a: 2, b: 2.5, want: -1, ok: true},
		{name: "named float", a: namedFloat(1.5), b: 1.5, want: 0, ok: true},
		{name: "not a number", a: "1", b: 1, ok: false},
		{name: "NaN", a: math.NaN(), b: 1.0, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := CompareNumbers(tt.a, tt.b)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

type namedFloat float64

func TestStdlibJSONDecodePreservesNumbers(t *testing.T) {
	var v map[string]any
	require.NoError(t, StdlibJSON{}.Decode([]byte(`{"n": 9007199254740993, "f": 1.5}`), &v))
	assert.Equal(t, json.Number("9007199254740993"), v["n"])
	assert.Equal(t, json.Number("1.5"), v["f"])

	var s struct{ N int8 }
	assert.Error(t, StdlibJSON{}.Decode([]byte(`{"N": 128}`), &s), "typed decode must range-check")
	assert.Error(t, StdlibJSON{}.Decode([]byte(`{"n": 1} {"n": 2}`), &v), "trailing data must be rejected")
	assert.Error(t, StdlibJSON{}.Decode([]byte(`{"n": 1} x`), &v), "trailing garbage must be rejected")
	assert.NoError(t, StdlibJSON{}.Decode([]byte("{\"n\": 1}\n  "), &v), "trailing whitespace is fine")
}

func TestNormalizeScalar(t *testing.T) {
	ts := time.Date(2026, 4, 1, 2, 0, 0, 500, time.FixedZone("x", 2*3600))
	tests := []struct {
		name    string
		dt      FieldType
		kind    reflect.Kind
		in      any
		want    any
		wantErr bool
	}{
		{name: "uuid canonicalized", dt: FieldTypeUUID, in: "0190A000-0000-7000-8000-00000000000A", want: "0190a000-0000-7000-8000-00000000000a"},
		{name: "uuid malformed", dt: FieldTypeUUID, in: "not-a-uuid", wantErr: true},
		{name: "timestamp to UTC", dt: FieldTypeTimestamp, in: "2026-04-01T02:00:00+02:00", want: "2026-04-01T00:00:00.000000000Z"},
		{name: "time.Time accepted", dt: FieldTypeTimestamp, in: ts, want: "2026-04-01T00:00:00.000000500Z"},
		{name: "pointer dereferenced", dt: FieldTypeInt, kind: reflect.Int64, in: new(int64(5)), want: int64(5)},
		{name: "number is not a string", dt: FieldTypeString, in: json.Number("5"), wantErr: true},
		{name: "null scalar rejected", dt: FieldTypeString, in: nil, wantErr: true},
		{name: "json passes through", dt: FieldTypeJSON, in: map[string]any{"a": json.Number("1")}, want: map[string]any{"a": json.Number("1")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeScalar(tt.dt, tt.kind, tt.in)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
