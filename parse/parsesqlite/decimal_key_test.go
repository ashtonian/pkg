//go:build unit

package parsesqlite_test

import (
	"bytes"
	"database/sql/driver"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/parsesqlite"
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

			rows, err := conn.QueryContext(t.Context(), `SELECT quote(a.x), quote(b.x),
				parse_decimal(a.x) = parse_decimal(b.x), parse_decimal_key(a.x) = parse_decimal_key(b.x)
				FROM forms a, forms b`)
			require.NoError(t, err)
			pairs := 0
			for rows.Next() {
				var a, b string
				var byText, byKey *int64
				require.NoError(t, rows.Scan(&a, &b, &byText, &byKey))
				assert.Equal(t, byText, byKey, "%s and %s", a, b)
				pairs++
			}
			require.NoError(t, rows.Err())
			assert.Equal(t, len(stored)*len(stored), pairs)

			// A filter binds a value's canonical text (core.DecimalText).
			rows, err = conn.QueryContext(t.Context(), `SELECT quote(a.x), quote(p.t),
				parse_decimal(a.x) = p.t, parse_decimal_key(a.x) = parse_decimal_key(p.t)
				FROM forms a, (SELECT DISTINCT parse_decimal(x) AS t FROM forms WHERE parse_decimal(x) IS NOT NULL) p`)
			require.NoError(t, err)
			for rows.Next() {
				var a, p string
				var byText, byKey *int64
				require.NoError(t, rows.Scan(&a, &p, &byText, &byKey))
				assert.Equal(t, byText, byKey, "%s and the parameter %s", a, p)
			}
			require.NoError(t, rows.Err())
		})
	}
}

// FuzzDecimalKey_EqualExactlyWhereTextIsEqual holds the functions to the
// rule the dialect's key comparisons rest on, over values a column can
// hold: parse_decimal_key is NULL exactly where parse_decimal is, two keys
// are equal exactly where the texts are, and a value's canonical text,
// bound as a filter binds it, has the value's text and key.
func FuzzDecimalKey_EqualExactlyWhereTextIsEqual(f *testing.F) {
	f.Add("5", "5.0", int64(5), 5.0)
	f.Add("-0", `"-0.0"`, int64(0), math.Copysign(0, -1))
	f.Add("1.50", "15e-1", int64(-1), 1.5)
	f.Add("0.10000000000000001", "0.1", int64(1), 0.1)
	f.Add("9223372036854775808", "9.223372036854775807e18", int64(math.MaxInt64), 9.223372036854775807e18)
	f.Add(`"1e3"`, "1e3", int64(1000), 1e3)
	f.Add("ten", "", int64(math.MinInt64), math.Inf(1))
	f.Add("1", "1.5", int64(10), 0.01)
	f.Add("-1", "-1.05", int64(-10), -0.1)
	f.Add("1", "1.0000000000000000000000000000000000001", int64(1), 1.0)
	f.Add("-0.5", "-0.51", int64(0), -0.5)
	f.Fuzz(func(t *testing.T, a, b string, i int64, x float64) {
		values := []driver.Value{a, b, i, x, []byte(a)}
		texts := make([]any, len(values))
		keys := make([]any, len(values))
		for n, v := range values {
			text, err := parsesqlite.Decimal(v)
			require.NoError(t, err)
			key, err := parsesqlite.DecimalKey(v)
			require.NoError(t, err)
			if (text == nil) != (key == nil) {
				t.Fatalf("%#v: text %#v, key %#v", v, text, key)
			}
			texts[n], keys[n] = text, key
			if text == nil {
				continue
			}
			bound, err := parsesqlite.Decimal(text)
			require.NoError(t, err)
			boundKey, err := parsesqlite.DecimalKey(text)
			require.NoError(t, err)
			if bound != text || !bytes.Equal(boundKey.([]byte), key.([]byte)) {
				t.Fatalf("%#v: text %q reads back as %#v with key %x, not key %x", v, text, bound, boundKey, key)
			}
		}
		for n := range values {
			for m := range values {
				if texts[n] == nil || texts[m] == nil {
					continue
				}
				if (texts[n] == texts[m]) != bytes.Equal(keys[n].([]byte), keys[m].([]byte)) {
					t.Fatalf("%#v and %#v: texts %q and %q, keys %x and %x", values[n], values[m], texts[n], texts[m], keys[n], keys[m])
				}
			}
		}
	})
}
