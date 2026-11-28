//go:build unit

package parsesql_test

import (
	"bytes"
	"database/sql/driver"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/parsesql"
)

// The dialect compares decimals by key, equality and sets included, where
// it once compared their canonical text: the two must agree on every value
// SQLite can hold, so the stored forms below are compared pairwise in SQL,
// each with every other and with the canonical text a filter binds.
func TestDecimalKey_EqualExactlyWhereTextIsEqual(t *testing.T) {
	stored := []string{
		"5", "5.0", "'5'", "'5.0'", "'5e0'", "'0.5e1'", `'"5"'`, `'"5.0"'`,
		"0", "0.0", "-0.0", "'0'", "'-0'", "'0.000'", "'-0e5'", `'"-0.0"'`,
		"1.5", "'1.50'", "'15e-1'", `'"1.50"'`, "-1.5", "'-1.50'", "'-15E-1'",
		"0.1", "'0.1'", "'0.10000000000000001'", "0.30000000000000004", "'0.3'",
		"9223372036854775807", "'9223372036854775808'", "-9223372036854775808",
		"1e300", "'1e300'", "'12345678901234567890.123456789012345678'",
		`'"1e3"'`, "'ten'", "x'35'", "CAST('5' AS BLOB)", "NULL", "''", "'+5'", "'.5'", "'05'", "'1e1001'",
		"1", "-1", "10", "'1.0000000000000000000000000000000000001'", "'-1.0000000000000000000000000000000000001'",
	}
	// Values a key could merge: each sign, significant digits that extend
	// one another, and every exponent between, each as exponent text.
	for _, sign := range []string{"", "-"} {
		for _, digits := range []string{"1", "15", "105", "1005", "2", "100000000000000000001"} {
			for exp := -3; exp <= 3; exp++ {
				stored = append(stored, fmt.Sprintf("'%s%se%d'", sign, digits, exp))
			}
		}
	}
	for _, d := range databases(t) {
		t.Run(d.name, func(t *testing.T) {
			// One connection: an in-memory database is the connection's own.
			conn, err := d.db.Conn(t.Context())
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			_, err = conn.ExecContext(t.Context(), "CREATE TEMP TABLE forms (x)")
			require.NoError(t, err)
			for _, v := range stored {
				_, err := conn.ExecContext(t.Context(), "INSERT INTO forms VALUES ("+v+")")
				require.NoError(t, err, v)
			}

			pairRows, err := conn.QueryContext(t.Context(), `SELECT quote(a.x), quote(b.x),
				parse_decimal(a.x) = parse_decimal(b.x), parse_decimal_key(a.x) = parse_decimal_key(b.x)
				FROM forms a, forms b`)
			require.NoError(t, err)
			defer func() { require.NoError(t, pairRows.Close()) }()
			pairs := 0
			for pairRows.Next() {
				var a, b string
				var byText, byKey *int64
				require.NoError(t, pairRows.Scan(&a, &b, &byText, &byKey))
				assert.Equal(t, byText, byKey, "%s and %s", a, b)
				pairs++
			}
			require.NoError(t, pairRows.Err())
			assert.Equal(t, len(stored)*len(stored), pairs)

			// A filter binds a value's canonical text (core.DecimalText).
			paramRows, err := conn.QueryContext(t.Context(), `SELECT quote(a.x), quote(p.t),
				parse_decimal(a.x) = p.t, parse_decimal_key(a.x) = parse_decimal_key(p.t)
				FROM forms a, (SELECT DISTINCT parse_decimal(x) AS t FROM forms WHERE parse_decimal(x) IS NOT NULL) p`)
			require.NoError(t, err)
			defer func() { require.NoError(t, paramRows.Close()) }()
			for paramRows.Next() {
				var a, p string
				var byText, byKey *int64
				require.NoError(t, paramRows.Scan(&a, &p, &byText, &byKey))
				assert.Equal(t, byText, byKey, "%s and the parameter %s", a, p)
			}
			require.NoError(t, paramRows.Err())
		})
	}
}

// Decimal and DecimalKey agree on the Go values a driver hands a function:
// the key is nil exactly where the text is, a value's canonical text, bound
// as a filter binds it, has the value's text and key, and two keys are equal
// exactly where the texts are.
func TestDecimalKey_AgreesWithDecimal(t *testing.T) {
	tests := []struct {
		name   string
		values []driver.Value
		texts  []driver.Value
	}{
		{
			name:   "zero of each sign, as text, a member's JSON string, an integer and a real",
			values: []driver.Value{"-0", `"-0.0"`, int64(0), math.Copysign(0, -1), []byte("-0")},
			texts:  []driver.Value{"0", "0", "0", "0", nil},
		},
		{
			name:   "around 2^63, as text, exponent text, an integer and a real",
			values: []driver.Value{"9223372036854775808", "9.223372036854775807e18", int64(math.MaxInt64), 9.223372036854775807e18},
			texts:  []driver.Value{"9223372036854775808", "9223372036854775807", "9223372036854775807", "9223372036854776000"},
		},
		{
			name:   "values holding no decimal beside the least integer",
			values: []driver.Value{"ten", "", int64(math.MinInt64), math.Inf(1), math.NaN()},
			texts:  []driver.Value{nil, nil, "-9223372036854775808", nil, nil},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Len(t, tt.texts, len(tt.values))
			keys := make([][]byte, len(tt.values))
			for n, v := range tt.values {
				text, err := parsesql.Decimal(v)
				require.NoError(t, err)
				assert.Equal(t, tt.texts[n], text, "%#v", v)
				key, err := parsesql.DecimalKey(v)
				require.NoError(t, err)
				if text == nil {
					assert.Nil(t, key, "%#v", v)
					continue
				}
				keyBytes, ok := key.([]byte)
				require.True(t, ok, "%#v: key %#v", v, key)
				keys[n] = keyBytes

				bound, err := parsesql.Decimal(text)
				require.NoError(t, err)
				assert.Equal(t, text, bound, "%#v", v)
				boundKey, err := parsesql.DecimalKey(text)
				require.NoError(t, err)
				assert.Equal(t, key, boundKey, "%#v", v)
			}
			for n := range tt.values {
				for m := range tt.values {
					if tt.texts[n] == nil || tt.texts[m] == nil {
						continue
					}
					assert.Equal(t, tt.texts[n] == tt.texts[m], bytes.Equal(keys[n], keys[m]), "%#v and %#v", tt.values[n], tt.values[m])
				}
			}
		})
	}
}
