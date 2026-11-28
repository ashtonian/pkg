//go:build unit

package canonical

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

// Two stored values share a form exactly where they are one value: a
// document as PostgreSQL's jsonb text, an array by each element's value, a
// scalar by its canonical text.
func TestText_OneFormPerValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind Kind
		a, b any
		same bool
	}{
		{"members in another order and spacing", Document, `{"b":[1, 2],"a":{"y":1,"x":2}}`, `{"a":{"x":2,"y":1},"b":[1,2]}`, true},
		{"an exponent", Document, `{"a":1e2}`, `{"a":100}`, true},
		{"an exponent with a scale", Document, `{"a":1.50e1}`, `{"a":15.0}`, true},
		{"a negative exponent", Document, `[1E-3]`, `[0.001]`, true},
		{"a negative zero", Document, `{"a":-0}`, `{"a":0}`, true},
		{"a negative zero with a scale", Document, `-0.0`, `0.0`, true},
		{"a scale", Document, `{"a":1.0}`, `{"a":1}`, false},
		{"a scale an exponent leaves", Document, `1.5e0`, `1.5`, true},
		{"a number and its text", Document, `{"a":1}`, `{"a":"1"}`, false},
		{"an extra member", Document, `{"a":1}`, `{"a":1,"b":null}`, false},
		{"a duplicated member, by its last value", Document, `{"a":2,"a":1}`, `{"a":1}`, true},
		{"elements in another order", Document, `[1,2]`, `[2,1]`, false},
		{"a string's escapes", Document, `{"a":"A\/"}`, `{"a":"A/"}`, true},
		{"another exponent", Document, `1e999999`, `1e999998`, false},
		{"a long exponent's value in another spelling", Document, `1e999999`, `10e999998`, true},
		{"a value past a thousand digits in plain notation", Document, `1` + strings.Repeat("0", 1001), `1e1001`, true},
		{"a fraction an exponent covers", Document, `1.0e1001`, `1e1001`, true},
		{"a scale past a thousand digits", Document, `1e-1000`, `1.0e-1000`, false},
		{"a scale past a thousand digits in another spelling", Document, `1e-1000`, `0.1e-999`, true},
		{"a long scale of zero", Document, `0e-999999999`, `-0.0e-999999998`, true},
		{"another long scale of zero", Document, `0e-999999999`, `0e-999999998`, false},
		{"an exponent past int64", Document, `1e99999999999999999999`, `10e99999999999999999998`, true},
		{"a scale past int64", Document, `1.0e-99999999999999999999`, `1e-99999999999999999999`, false},
		{"text and bytes", Document, `{"a":1}`, []byte(`{"a": 1}`), true},
		{"an array's spacing", "array:float", `[1, 2]`, `[1,2]`, true},
		{"an array's negative zero", "array:float", `[-0]`, `[0]`, false},
		{"an array's float in another form", "array:float", `[1.0, 1e2]`, `[1,100]`, true},
		{"an array's UUIDs in either case", "array:uuid", `["0190A5B4-7C1E-7D8A-9B2C-3D4E5F6A7B8C"]`, `["0190a5b4-7c1e-7d8a-9b2c-3d4e5f6a7b8c"]`, true},
		{"an array's timestamps in another offset", "array:timestamp", `["2025-12-31T19:00:00-05:00"]`, `["2026-01-01T00:00:00.000000000Z"]`, true},
		{"an array's decimals in another scale", "array:decimal", `[1.50,"2.0"]`, `["1.5",2]`, true},
		{"an array's string and number", "array:string", `["1"]`, `[1]`, false},
		{"an array's NULL elements", "array:uuid", `[null]`, `[null]`, true},
		{"an array of no scalar type, as written", "array", `[1.0]`, `[1]`, false},
		{"a UUID's case and spelling", "uuid", "{0190A5B4-7C1E-7D8A-9B2C-3D4E5F6A7B8C}", "0190a5b47c1e7d8a9b2c3d4e5f6a7b8c", true},
		{"another UUID", "uuid", "0190a5b4-7c1e-7d8a-9b2c-3d4e5f6a7b8c", "0190a5b4-7c1e-7d8a-9b2c-3d4e5f6a7b8d", false},
		{"a decimal's scale", "decimal", "1.50", "1.5", true},
		{"a decimal as an INTEGER and a REAL", "decimal", int64(2), 2.0, true},
		{"a decimal's exponent", "decimal", "1.05e1", "10.5", true},
		{"another decimal", "decimal", "1.5", "1.51", false},
		{"a timestamp's offset and precision", "timestamp", "2025-12-31T19:00:00-05:00", "2026-01-01T00:00:00.000000000Z", true},
		{"another instant", "timestamp", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00.000001Z", false},
		{"an interval's zero fields", "interval", "P1DT0H", "P1D", true},
		{"an interval in other units", "interval", "PT24H", "P1D", false},
		{"a time of day's fraction", "time", "10:30:00.000", "10:30:00", true},
		{"a host's prefix", "inet", "192.0.2.1/32", "192.0.2.1", true},
		{"a network's case", "cidr", "2001:DB8::/32", "2001:db8::/32", true},
		{"a MAC address's separators", "macaddr", "08-00-2B-01-02-03", "08:00:2b:01:02:03", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, err := Text(tc.kind, tc.a)
			require.NoError(t, err)
			y, err := Text(tc.kind, tc.b)
			require.NoError(t, err)
			assert.Equal(t, tc.same, x == y, "%s and %s", x, y)
			for _, form := range []string{x, y} {
				again, err := Text(tc.kind, form)
				require.NoError(t, err)
				assert.Equal(t, form, again, "the form of a form is itself")
			}
		})
	}
}

// A document's form is its members sorted and unspaced, each number in its
// form, whose length is linear in its literal's: no exponent is padded out.
func TestText_DocumentForm(t *testing.T) {
	got, err := Text(Document, `{"b": 1.50e1, "a": [1e2, -0, 1E-3, 1.0, -1.50, "<&>", 1e1000000000]}`)
	require.NoError(t, err)
	assert.Equal(t, `{"a":[1e2,0,1e-3,10e-1,-150e-2,"<&>",1e1000000000],"b":150e-1}`, got) //nolint:testifylint // the text is the form under test; JSONEq would take 150e-1 for 15
}

func TestText_RefusesWhatHoldsNoValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind Kind
		v    any
		err  string
	}{
		{"a document that is not JSON", Document, `{`, "decoding the stored document"},
		{"a document followed by text", Document, `{} {}`, "decoding the stored document: text follows its value"},
		{"a document of invalid UTF-8", Document, "\"\xff\"", "decoding the stored document: its text is not valid UTF-8"},
		{"a document that is not text", Document, int64(1), "a stored document is JSON text, not int64"},
		{"an array that is not JSON", "array:uuid", `[`, "decoding the stored document"},
		{"text that is no UUID", "uuid", "ten", `invalid UUID "ten"`},
		{"a BLOB that is no decimal", "decimal", []byte("1.5"), "holds no decimal"},
		{"a date with a time of day", "date", "2026-01-02T00:00:00Z", "invalid civil date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Text(tc.kind, tc.v)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.err)
		})
	}
}

// SQLite's function gives NULL as NULL, a document or array in its form,
// a JSON BLOB included, and a scalar that holds no value of its kind as it
// is stored, a BLOB included.
func TestValue_ComparesAScalarThatHoldsNoValueAsStored(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind Kind
		v    any
		want any
	}{
		{"a document", Document, `{"a": 1.0}`, `{"a":10e-1}`},
		{"NULL", Document, nil, nil},
		{"a BLOB of JSON text", Document, []byte(`{"b":1, "a":2}`), `{"a":2,"b":1}`},
		{"an array", "array:uuid", `["0190A5B4-7C1E-7D8A-9B2C-3D4E5F6A7B8C"]`, `["0190a5b4-7c1e-7d8a-9b2c-3d4e5f6a7b8c"]`},
		{"text that is no UUID", "uuid", "ten", "ten"},
		{"a legacy midnight timestamp in a date column", "date", "2026-01-02T00:00:00Z", "2026-01-02T00:00:00Z"},
		{"a BLOB that is no decimal", "decimal", []byte("1.5"), []byte("1.5")},
		{"a REAL decimal", "decimal", 1.5, "1.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Value(tc.kind, tc.v)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A document or array that holds no JSON value has no form: SQLite's
// function refuses it, as PostgreSQL's cast to jsonb does.
func TestValue_RefusesADocumentOrArrayThatHoldsNoJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind Kind
		v    any
		err  string
	}{
		{"a document that is not JSON", Document, "not json", "decoding the stored document"},
		{"a BLOB that is not JSON", Document, []byte("{"), "decoding the stored document"},
		{"a document of invalid UTF-8", Document, "\"\xff\"", "its text is not valid UTF-8"},
		{"a number in a document column", Document, int64(1), "a stored document is JSON text, not int64"},
		{"an array that is not JSON", "array:uuid", "[", "decoding the stored document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Value(tc.kind, tc.v)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.err)
		})
	}
}

// Each column type a value can spell more than one way has a kind its name
// parses back to; a type compared as stored has none.
func TestKindOf(t *testing.T) {
	for _, tc := range []struct {
		t    core.TypeInfo
		want Kind
	}{
		{core.TypeInfo{Type: core.FieldTypeJSON}, Document},
		{core.TypeInfo{Type: core.FieldTypeObject}, Document},
		{core.TypeInfo{Type: core.FieldTypeArray, SQLType: core.SQLJSONB}, Document},
		{core.TypeInfo{Type: core.FieldTypeString, SQLType: core.SQLJSON}, Document},
		{core.TypeInfo{Type: core.FieldTypeArray, Elem: core.FieldTypeUUID}, "array:uuid"},
		{core.TypeInfo{Type: core.FieldTypeArray, Elem: core.FieldTypeFloat, NumberKind: reflect.Float32}, "array:float"},
		{core.TypeInfo{Type: core.FieldTypeArray}, "array"},
		{core.TypeInfo{Type: core.FieldTypeDecimal}, "decimal"},
		{core.TypeInfo{Type: core.FieldTypeUUID}, "uuid"},
		{core.TypeInfo{Type: core.FieldTypeTimestamp}, "timestamp"},
		{core.TypeInfo{Type: core.FieldTypeDate, SQLType: core.SQLDate}, "date"},
		{core.TypeInfo{Type: core.FieldTypeMACAddr}, "macaddr"},
	} {
		t.Run(string(tc.want), func(t *testing.T) {
			got, ok := KindOf(tc.t)
			require.True(t, ok)
			assert.Equal(t, tc.want, got)
			parsed, ok := ParseKind(string(got))
			assert.True(t, ok)
			assert.Equal(t, got, parsed)
		})
	}
	for _, t0 := range []core.FieldType{core.FieldTypeString, core.FieldTypeInt, core.FieldTypeFloat, core.FieldTypeBool, core.FieldTypeGeo, core.FieldTypeAny, ""} {
		_, ok := KindOf(core.TypeInfo{Type: t0})
		assert.False(t, ok, "%q compares as stored", t0)
	}
	for _, name := range []string{"", "string", "int", "array:", "array:geo", "array:array", "Document", "array:uuid "} {
		_, ok := ParseKind(name)
		assert.False(t, ok, "%q", name)
	}
}
