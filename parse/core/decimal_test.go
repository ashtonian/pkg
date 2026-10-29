//go:build unit

package core_test

import (
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

// fixedPoint is a decimal type whose pointer has the Decimal method set:
// units hundredths.
type fixedPoint struct{ units int64 }

func (f *fixedPoint) Coefficient() *big.Int { return big.NewInt(f.units) }
func (f *fixedPoint) Exponent() int32       { return -2 }

// textDecimal is a decimal library type known only by its text.
type textDecimal struct{ text string }

func (d textDecimal) MarshalText() ([]byte, error) { return []byte(d.text), nil }

func TestDecimalText(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"integer", "10", "10"},
		{"canonical fraction", "10.25", "10.25"},
		{"trailing zeros", "10.2500", "10.25"},
		{"integral scale", "10.00", "10"},
		{"zero", "0", "0"},
		{"zero with scale", "0.000", "0"},
		{"negative zero", "-0", "0"},
		{"negative zero fraction", "-0.0", "0"},
		{"negative", "-1.50", "-1.5"},
		{"small fraction", "0.0012", "0.0012"},
		{"exponent", "1e3", "1000"},
		{"upper-case exponent with sign", "1.5E+2", "150"},
		{"negative exponent", "125e-2", "1.25"},
		{"negative exponent below one", "-5e-3", "-0.005"},
		{"exponent of zero", "0e9", "0"},
		{"leading exponent zeros", "1e0002", "100"},
		{"fraction exponent", "0.00125e2", "0.125"},
		{"beyond float64 precision", "0.10000000000000001", "0.10000000000000001"},
		{"beyond int64", "123456789012345678901234567890", "123456789012345678901234567890"},
		{"json number", json.Number("2.50"), "2.5"},
		{"int", 42, "42"},
		{"negative int8", int8(-7), "-7"},
		{"uint64 max", uint64(math.MaxUint64), "18446744073709551615"},
		{"float shortest form", 0.1, "0.1"},
		{"float integral", 100.0, "100"},
		{"float exponent form", 1e21, "1000000000000000000000"},
		{"float small", 1.5e-7, "0.00000015"},
		{"float negative zero", math.Copysign(0, -1), "0"},
		{"float32 shortest form", float32(0.1), "0.1"},
		{"shopspring", decimal.RequireFromString("10.250"), "10.25"},
		{"shopspring pointer", new(decimal.RequireFromString("-3.5")), "-3.5"},
		{"shopspring exponent", decimal.New(5, 3), "5000"},
		{"shopspring fraction", decimal.New(-25, -4), "-0.0025"},
		{"pointer-receiver decimal", fixedPoint{units: 1050}, "10.5"},
		{"text decimal", textDecimal{text: "7.250"}, "7.25"},
		{"named string", namedString("3.0"), "3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := core.DecimalText(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecimalText_Rejects(t *testing.T) {
	tests := []struct {
		name string
		in   any
	}{
		{"empty", ""},
		{"word", "ten"},
		{"leading plus", "+1"},
		{"leading point", ".5"},
		{"trailing point", "5."},
		{"leading zero", "007"},
		{"double minus", "--1"},
		{"hexadecimal", "0x10"},
		{"fraction", "1/3"},
		{"space", " 1"},
		{"comma", "1,5"},
		{"exponent without digits", "1e"},
		{"NaN text", "NaN"},
		{"infinity text", "Infinity"},
		{"NaN", math.NaN()},
		{"infinity", math.Inf(1)},
		{"bool", true},
		{"null", nil},
		{"nil pointer", (*decimal.Decimal)(nil)},
		{"time", time.Unix(0, 0)},
		{"text not a decimal", textDecimal{text: "cheap"}},
		{"exponent past the digit bound", "1e1000"},
		{"negative exponent past the digit bound", "1e-1000"},
		{"exponent beyond int32", "1e99999999999"},
		{"plain literal past the digit bound", strings.Repeat("9", core.MaxDecimalDigits+1)},
		{"decimal past the digit bound", decimal.New(1, core.MaxDecimalDigits)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := core.DecimalText(tt.in)
			assert.Error(t, err)
		})
	}
}

func TestDecimalText_DigitBound(t *testing.T) {
	widest := strings.Repeat("9", core.MaxDecimalDigits)
	got, err := core.DecimalText(widest)
	require.NoError(t, err)
	assert.Equal(t, widest, got)

	got, err = core.DecimalText("1e999")
	require.NoError(t, err)
	assert.Len(t, got, core.MaxDecimalDigits)

	got, err = core.DecimalText("1e-999") // "0.", 998 zeros and a 1
	require.NoError(t, err)
	assert.Len(t, got, core.MaxDecimalDigits+1)
}

// CompareDecimals agrees with exact rational order over values that differ
// in sign, magnitude, scale and representation.
func TestCompareDecimals(t *testing.T) {
	values := []string{
		"-100", "-10.25", "-10", "-9.5", "-1", "-0.5", "-0.05", "-0.0001",
		"0", "0.0001", "0.05", "0.5", "1", "2", "9.5", "9.99", "10", "10.25", "10.250", "100",
		"0.10000000000000001", "0.1", "123456789012345678901234567890", "1e3", "-1E-3",
	}
	for _, a := range values {
		for _, b := range values {
			ra, _ := new(big.Rat).SetString(a)
			rb, _ := new(big.Rat).SetString(b)
			got, ok := core.CompareDecimals(a, b)
			require.True(t, ok, "%s vs %s", a, b)
			assert.Equal(t, ra.Cmp(rb), got, "%s vs %s", a, b)
		}
	}
}

func TestCompareDecimals_Operands(t *testing.T) {
	tests := []struct {
		name string
		a, b any
		want int
	}{
		{"shopspring and string", decimal.RequireFromString("10.25"), "9.5", 1},
		{"pointer and number", new(decimal.RequireFromString("2")), json.Number("2.0"), 0},
		{"string and int", "-1", 0, -1},
		{"float and string", 0.1, "0.10000000000000001", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := core.CompareDecimals(tt.a, tt.b)
			require.True(t, ok)
			assert.Equal(t, tt.want, got)
		})
	}
	_, ok := core.CompareDecimals("1", "one")
	assert.False(t, ok)
	_, ok = core.CompareDecimals(nil, "1")
	assert.False(t, ok)
}

func TestTypeOf_Decimal(t *testing.T) {
	tests := []struct {
		name string
		typ  reflect.Type
		want core.TypeInfo
	}{
		{"shopspring", reflect.TypeFor[decimal.Decimal](), core.TypeInfo{Type: core.FieldTypeDecimal}},
		{"shopspring pointer", reflect.TypeFor[*decimal.Decimal](), core.TypeInfo{Type: core.FieldTypeDecimal}},
		{"pointer-receiver method set", reflect.TypeFor[fixedPoint](), core.TypeInfo{Type: core.FieldTypeDecimal}},
		{"slice of decimals is a document", reflect.TypeFor[[]decimal.Decimal](), core.TypeInfo{Type: core.FieldTypeJSON}},
		{"NullDecimal is a struct", reflect.TypeFor[decimal.NullDecimal](), core.TypeInfo{Type: core.FieldTypeObject}},
		{"text-only decimal is a struct", reflect.TypeFor[textDecimal](), core.TypeInfo{Type: core.FieldTypeObject}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, core.TypeOf(tt.typ))
		})
	}
}

func TestNormalizeScalar_Decimal(t *testing.T) {
	got, err := core.NormalizeScalar(core.FieldTypeDecimal, reflect.Invalid, new(decimal.RequireFromString("1.50")))
	require.NoError(t, err)
	assert.Equal(t, "1.5", got)

	_, err = core.NormalizeScalar(core.FieldTypeDecimal, reflect.Invalid, nil)
	assert.ErrorContains(t, err, "expected decimal, got null")

	_, err = core.NormalizeScalar(core.FieldTypeDecimal, reflect.Invalid, "1.5.0")
	assert.ErrorContains(t, err, `"1.5.0" is not a decimal number`)
}
