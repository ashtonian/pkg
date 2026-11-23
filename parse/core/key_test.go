//go:build unit

package core_test

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

// A key value's canonical text is equal for two values exactly when the
// database holds them equal: a decimal by value at any length, a
// timestamp at its instant, -0 as 0, a float32 at its own precision. NaN
// and a document are no key value.
func TestKeyTextOf(t *testing.T) {
	dec := core.TypeInfo{Type: core.FieldTypeDecimal}
	at := core.TypeInfo{Type: core.FieldTypeTimestamp}
	f64 := core.TypeInfo{Type: core.FieldTypeFloat, NumberKind: reflect.Float64}
	f32 := core.TypeInfo{Type: core.FieldTypeFloat, NumberKind: reflect.Float32}
	str := core.TypeInfo{Type: core.FieldTypeString}
	for _, tt := range []struct {
		name    string
		types   []core.TypeInfo
		keys    []core.Key
		want    string
		wantErr string
	}{
		{name: "decimal by value", types: []core.TypeInfo{dec}, keys: []core.Key{{"1.50"}, {"1.5"}, {1.5}, {decimal.RequireFromString("1.500")}}, want: "1.5"},
		{name: "20-digit decimal", types: []core.TypeInfo{dec}, keys: []core.Key{{"12345678901234567890"}, {decimal.RequireFromString("12345678901234567890.0")}}, want: "12345678901234567890"},
		{name: "timestamp at its instant", types: []core.TypeInfo{at}, keys: []core.Key{
			{"2026-01-02T03:04:05.5+01:00"}, {time.Date(2026, 1, 2, 2, 4, 5, 500_000_000, time.UTC)},
		}, want: "2026-01-02T02:04:05.500000000Z"},
		{name: "negative zero", types: []core.TypeInfo{f64}, keys: []core.Key{{math.Copysign(0, -1)}, {0.0}, {0}}, want: "0"},
		{name: "float32 precision", types: []core.TypeInfo{f32}, keys: []core.Key{{float32(0.1)}}, want: "0.1"},
		{name: "boolean", types: []core.TypeInfo{{Type: core.FieldTypeBool}}, keys: []core.Key{{true}}, want: "true"},
		{name: "device and instant", types: []core.TypeInfo{str, at}, keys: []core.Key{
			{"d1", "2026-01-02T03:04:05Z"}, {"d1", time.Date(2026, 1, 2, 4, 4, 5, 0, time.FixedZone("", 3600))},
		}, want: `["d1","2026-01-02T03:04:05.000000000Z"]`},
		{name: "NaN", types: []core.TypeInfo{f64}, keys: []core.Key{{math.NaN()}, {math.Inf(1)}}, wantErr: "number must be finite"},
		{name: "a document", types: []core.TypeInfo{{Type: core.FieldTypeJSON}}, keys: []core.Key{{"{}"}}, wantErr: "a json value is no key value"},
		{name: "a value short", types: []core.TypeInfo{str, at}, keys: []core.Key{{"d1"}}, wantErr: "key has 1 values for 2 columns"},
		{name: "a column named", types: []core.TypeInfo{str, at}, keys: []core.Key{{"d1", "yesterday"}}, wantErr: "key value 1: "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range tt.keys {
				got, err := core.KeyTextOf(tt.types, key)
				if tt.wantErr != "" {
					require.ErrorContains(t, err, tt.wantErr)
					continue
				}
				require.NoError(t, err, "%v", key)
				assert.Equal(t, tt.want, got, "%v", key)
			}
		})
	}
}

// A key's text is what PostgreSQL's array_to_json(ARRAY[col::text, …])::text
// and SQLite's json_array(CAST(col AS TEXT), …) write for the same row, so
// includes and facets name a row alike in Go and in SQL.
func TestKeyText(t *testing.T) {
	id := uuid.MustParse("0190f3c2-0000-7000-8000-000000000011")
	for _, tt := range []struct {
		name string
		key  core.Key
		want string
	}{
		{name: "one string column", key: core.Key{"w1"}, want: "w1"},
		{name: "one integer column", key: core.Key{int64(42)}, want: "42"},
		{name: "one uuid column", key: core.Key{id}, want: "0190f3c2-0000-7000-8000-000000000011"},
		{name: "string and integer", key: core.Key{"w1", int64(-7)}, want: `["w1","-7"]`},
		{name: "uuid and string", key: core.Key{id, "a b"}, want: `["0190f3c2-0000-7000-8000-000000000011","a b"]`},
		{name: "quote and backslash", key: core.Key{`a"b`, `c\d`}, want: `["a\"b","c\\d"]`},
		{name: "named controls", key: core.Key{"\b\f\n\r\t", "x"}, want: `["\b\f\n\r\t","x"]`},
		{name: "other controls", key: core.Key{"\x00\x1f", "x"}, want: `["\u0000\u001f","x"]`},
		{name: "non-ASCII is not escaped", key: core.Key{"é/<", "x"}, want: `["é/<","x"]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, core.KeyText(tt.key))
		})
	}
}
