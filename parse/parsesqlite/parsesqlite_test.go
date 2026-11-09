//go:build unit

package parsesqlite_test

import (
	"database/sql"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/parsesqlite"
)

func TestRegisterIsIdempotent(t *testing.T) {
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() { errs[i] = parsesqlite.Register() })
	}
	wg.Wait()
	for _, err := range errs {
		assert.NoError(t, err)
	}
	assert.NoError(t, parsesqlite.Register())
}

func open(t *testing.T) *sql.DB {
	t.Helper()
	require.NoError(t, parsesqlite.Register())
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestDecimal(t *testing.T) {
	db := open(t)
	tests := []struct {
		name string
		expr string
		want any
	}{
		{"an integer past double precision", "parse_decimal(9007199254740993)", "9007199254740993"},
		{"decimal text of 38 digits", "parse_decimal('12345678901234567890.123456789012345678')", "12345678901234567890.123456789012345678"},
		{"text with a scale and an exponent", "parse_decimal('1.0500e1')", "10.5"},
		{"a member's number as written", `parse_decimal('{"p":9007199254740993.5}' -> '$.p')`, "9007199254740993.5"},
		{"a member's string", `parse_decimal('{"p":"9.50"}' -> '$.p')`, "9.5"},
		{"a member's string with an exponent", `parse_decimal('{"p":"1e3"}' -> '$.p')`, nil},
		{"a member of another kind", `parse_decimal('{"p":true}' -> '$.p')`, nil},
		{"a missing member", `parse_decimal('{}' -> '$.p')`, nil},
		{"text that is no number", "parse_decimal('ten')", nil},
		{"a blob of no text", "parse_decimal(x'00')", nil},
		{"NULL", "parse_decimal(NULL)", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got any
			require.NoError(t, db.QueryRow("SELECT "+tt.expr).Scan(&got))
			assert.Equal(t, tt.want, got)
		})
	}
}

// A float32 field's REAL holds the double of its float32; the function
// keys the float32.
func TestDecimalFloat32(t *testing.T) {
	db := open(t)
	_, err := db.Exec("CREATE TABLE f (v REAL)")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO f VALUES (?)", float64(float32(0.1)))
	require.NoError(t, err)
	tests := []struct {
		name string
		expr string
		want any
	}{
		{"a REAL holding a float32", "(SELECT parse_decimal_float32(v) FROM f)", "0.1"},
		{"the same REAL read as a double", "(SELECT parse_decimal(v) FROM f)", "0.10000000149011612"},
		{"an integer", "parse_decimal_float32(2)", "2"},
		{"an integer past float32 precision", "parse_decimal_float32(16777217)", "16777216"},
		{"a REAL with an exponent", "parse_decimal_float32(1.5e-7)", "0.00000015"},
		{"negative zero", "parse_decimal_float32(-0.0)", "0"},
		{"a number literal", "parse_decimal_float32('2.50')", "2.5"},
		{"beyond float32's range", "parse_decimal_float32(1e39)", nil},
		{"a JSON string literal", `parse_decimal_float32('"2.5"')`, nil},
		{"text that is no number", "parse_decimal_float32('NaN')", nil},
		{"NULL", "parse_decimal_float32(NULL)", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got any
			require.NoError(t, db.QueryRow("SELECT "+tt.expr).Scan(&got))
			assert.Equal(t, tt.want, got)
		})
	}
}

// The comparisons CAST(… AS NUMERIC) gets wrong past double precision
// (pkg#144) order exactly through the key.
func TestDecimalKeyOrdersExactly(t *testing.T) {
	db := open(t)
	for _, expr := range []string{
		"parse_decimal_key('12345678901234567.1') < parse_decimal_key('12345678901234567.2')",
		"parse_decimal_key('99999999999999999999') < parse_decimal_key('100000000000000000000')",
		`parse_decimal_key('{"a":9007199254740993.5}' -> '$.a') < parse_decimal_key('9007199254740993.6')`,
		"parse_decimal_key('-0.5') > parse_decimal_key('-0.51')",
		"parse_decimal_key('10.50') = parse_decimal_key(10.5)",
		"parse_decimal_key('ten') IS NULL",
	} {
		var holds bool
		require.NoError(t, db.QueryRow("SELECT "+expr).Scan(&holds), expr)
		assert.True(t, holds, expr)
	}
}
