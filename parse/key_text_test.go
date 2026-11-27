//go:build unit

package parse_test

import (
	"testing"
	"time"
	"uuid"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
)

// keyTextRow has a column of each scalar type a key's text is read as.
type keyTextRow struct {
	S   string          `json:"s"   db:"s"`
	I8  int8            `json:"i8"  db:"i8"`
	I64 int64           `json:"i64" db:"i64"`
	U16 uint16          `json:"u16" db:"u16"`
	U64 uint64          `json:"u64" db:"u64"`
	F32 float32         `json:"f32" db:"f32"`
	F64 float64         `json:"f64" db:"f64"`
	B   bool            `json:"b"   db:"b"`
	U   uuid.UUID       `json:"u"   db:"u"`
	TS  time.Time       `json:"ts"  db:"ts"`
	D   core.Date       `json:"d"   db:"d"`
	Dec decimal.Decimal `json:"dec" db:"dec"`
	Qty int             `json:"qty" db:"qty" parse:"patch"`
}

// ParseKey reads each column's text by the column's type and normalizes the
// key as ByKey does. A text its column cannot hold, U+0000 and invalid UTF-8
// included, is the request's ErrNoSuchKey naming the column; another number
// of texts than columns is the caller's ErrKeyInvalid.
func TestResource_ParseKey(t *testing.T) {
	tests := []struct {
		name    string
		key     []string
		texts   []string
		want    parse.Key
		wantErr string
		// callers marks the caller's mistake (ErrKeyInvalid) rather than
		// the request's (ErrNoSuchKey).
		callers bool
	}{
		{name: "string", key: []string{"s"}, texts: []string{"a/b c"}, want: parse.Key{"a/b c"}},
		{name: "negative int64", key: []string{"i64"}, texts: []string{"-42"}, want: parse.Key{int64(-42)}},
		{name: "largest uint64", key: []string{"u64"}, texts: []string{"18446744073709551615"}, want: parse.Key{uint64(18446744073709551615)}},
		{name: "leading zeros", key: []string{"i64"}, texts: []string{"007"}, want: parse.Key{int64(7)}},
		{name: "float64", key: []string{"f64"}, texts: []string{"1e3"}, want: parse.Key{float64(1000)}},
		{name: "float32 at its precision", key: []string{"f32"}, texts: []string{"0.1"}, want: parse.Key{float64(float32(0.1))}},
		{name: "bool", key: []string{"b"}, texts: []string{"true"}, want: parse.Key{true}},
		{name: "bool as ParseBool reads it", key: []string{"b"}, texts: []string{"0"}, want: parse.Key{false}},
		{name: "uuid canonical", key: []string{"u"}, texts: []string{"0198A3C2-7A1E-7C00-8000-000000000001"}, want: parse.Key{"0198a3c2-7a1e-7c00-8000-000000000001"}},
		{name: "timestamp canonical", key: []string{"ts"}, texts: []string{"2026-01-02T05:04:05+02:00"}, want: parse.Key{"2026-01-02T03:04:05.000000000Z"}},
		{name: "date", key: []string{"d"}, texts: []string{"2028-02-29"}, want: parse.Key{"2028-02-29"}},
		{name: "decimal", key: []string{"dec"}, texts: []string{"10.25"}, want: parse.Key{"10.25"}},
		{name: "composite", key: []string{"s", "i64"}, texts: []string{"w1", "42"}, want: parse.Key{"w1", int64(42)}},

		{name: "int8 overflow", key: []string{"i8"}, texts: []string{"128"}, wantErr: "key column i8: 128 overflows int8"},
		{name: "uint64 overflow", key: []string{"u64"}, texts: []string{"18446744073709551616"}, wantErr: "key column u64: 18446744073709551616 overflows uint64"},
		{name: "negative unsigned", key: []string{"u16"}, texts: []string{"-1"}, wantErr: "key column u16: -1 is negative"},
		{name: "plus sign", key: []string{"i64"}, texts: []string{"+5"}, wantErr: `key column i64: "+5" is not a decimal integer`},
		{name: "exponent integer", key: []string{"i64"}, texts: []string{"1e3"}, wantErr: `key column i64: "1e3" is not a decimal integer`},
		{name: "empty integer", key: []string{"i64"}, texts: []string{""}, wantErr: `key column i64: "" is not a decimal integer`},
		{name: "NaN", key: []string{"f64"}, texts: []string{"NaN"}, wantErr: "key column f64:"},
		{name: "hexadecimal float", key: []string{"f64"}, texts: []string{"0x1p-2"}, wantErr: "key column f64:"},
		{name: "not a bool", key: []string{"b"}, texts: []string{"yes"}, wantErr: `key column b: "yes" is not true or false`},
		{name: "not a uuid", key: []string{"u"}, texts: []string{"nope"}, wantErr: "key column u: invalid UUID"},
		{name: "not a date", key: []string{"d"}, texts: []string{"2027-02-29"}, wantErr: "key column d:"},
		{name: "composite column", key: []string{"s", "i64"}, texts: []string{"w1", "x"}, wantErr: `key column i64: "x" is not a decimal integer`},
		{name: "U+0000", key: []string{"s"}, texts: []string{"a\x00b"}, wantErr: "key column s: invalid text: it holds U+0000"},
		{name: "invalid UTF-8", key: []string{"s", "i64"}, texts: []string{"a\xff", "1"}, wantErr: "key column s: invalid text: it is not valid UTF-8"},
		{name: "too few texts", key: []string{"s", "i64"}, texts: []string{"w1"}, wantErr: "1 texts for the 2 key columns (s, i64)", callers: true},
		{name: "too many texts", key: []string{"s"}, texts: []string{"w1", "a"}, wantErr: "2 texts for the 1 key columns (s)", callers: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := parse.NewResource[keyTextRow](onPostgres, "key_text_rows", parse.PrimaryKey(tt.key...))
			require.NoError(t, err)
			got, err := r.ParseKey(tt.texts...)
			if tt.wantErr != "" {
				want, not := parse.ErrNoSuchKey, parse.ErrKeyInvalid
				if tt.callers {
					want, not = not, want
				}
				require.ErrorIs(t, err, want)
				require.NotErrorIs(t, err, not)
				require.ErrorContains(t, err, "parse: key_text_rows: ")
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A key ParseKey reads from text targets the row the same key's values
// target through ByKey.
func TestResource_ParseKeyTargetsAsByKey(t *testing.T) {
	r, err := parse.NewResource[keyTextRow](onPostgres, "key_text_rows", parse.PrimaryKey("s", "i64"))
	require.NoError(t, err)
	p, err := r.ParseMergePatch([]byte(`{"qty":7}`))
	require.NoError(t, err)
	parsed, err := r.ParseKey("w1", "42")
	require.NoError(t, err)

	fromText, err := r.BuildUpdate(p, parse.ByKey(parsed), parse.Scope{})
	require.NoError(t, err)
	fromValues, err := r.BuildUpdate(p, parse.ByKey(parse.Key{"w1", 42}), parse.Scope{})
	require.NoError(t, err)
	assert.Equal(t, fromValues.Owner, fromText.Owner)
}
