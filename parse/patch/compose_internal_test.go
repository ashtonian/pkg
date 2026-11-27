//go:build unit

package patch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

// A document compares as PostgreSQL's jsonb text does, a number by its
// value and the scale its literal gives; a native array as written.
func TestStoredEqual_DocumentsAsJSONBText(t *testing.T) {
	document := core.TypeInfo{Type: core.FieldTypeJSON}
	array := core.TypeInfo{Type: core.FieldTypeArray, Elem: core.FieldTypeFloat}
	for _, tc := range []struct {
		name string
		t    core.TypeInfo
		a, b string
		same bool
	}{
		{"members in another order and spacing", document, `{"b":[1, 2],"a":{"y":1,"x":2}}`, `{"a":{"x":2,"y":1},"b":[1,2]}`, true},
		{"an exponent", document, `{"a":1e2}`, `{"a":100}`, true},
		{"an exponent with a scale", document, `{"a":1.50e1}`, `{"a":15.0}`, true},
		{"a negative exponent", document, `[1E-3]`, `[0.001]`, true},
		{"a negative zero", document, `{"a":-0}`, `{"a":0}`, true},
		{"a negative zero with a scale", document, `-0.0`, `0.0`, true},
		{"a scale", document, `{"a":1.0}`, `{"a":1}`, false},
		{"a scale an exponent leaves", document, `1.5e0`, `1.5`, true},
		{"a number and its text", document, `{"a":1}`, `{"a":"1"}`, false},
		{"an extra member", document, `{"a":1}`, `{"a":1,"b":null}`, false},
		{"elements in another order", document, `[1,2]`, `[2,1]`, false},
		{"exponents too long to compare", document, `1e999999`, `1e999998`, false},
		{"the same exponent too long to compare", document, `1e999999`, `1e999999`, true},
		{"an array's negative zero", array, `[-0]`, `[0]`, false},
		{"an array's spacing", array, `[1, 2]`, `[1,2]`, true},
		{"an array's scale", array, `[1.0]`, `[1]`, false},
		{"a declared json array's negative zero", core.TypeInfo{Type: core.FieldTypeArray, SQLType: core.SQLJSONB}, `[-0]`, `[0]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			same, err := storedEqual(tc.t, tc.a, tc.b)
			require.NoError(t, err)
			assert.Equal(t, tc.same, same)
			reversed, err := storedEqual(tc.t, tc.b, tc.a)
			require.NoError(t, err)
			assert.Equal(t, same, reversed, "equality is symmetric")
		})
	}
}
