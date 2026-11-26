//go:build unit

package core_test

import (
	"cmp"
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/ordertest"
)

// Every value of a type orders against every other as its case lists
// them, in every spelling; the spellings of one value are equal and share
// its canonical text; NULL orders after every value and equals NULL.
func TestCompareCanonical_Conformance(t *testing.T) {
	for _, c := range ordertest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			for i, x := range c.Values {
				for j, y := range c.Values {
					for _, a := range x.Spellings {
						for _, b := range y.Spellings {
							got, err := core.CompareCanonical(c.Type, a, b)
							require.NoError(t, err, "%#v vs %#v", a, b)
							assert.Equal(t, cmp.Compare(i, j), got, "%#v vs %#v", a, b)
						}
					}
				}
				for _, a := range x.Spellings {
					text, err := core.CanonicalText(c.Type, a)
					require.NoError(t, err, "%#v", a)
					assert.Equal(t, x.Text, text, "%#v", a)

					got, err := core.CompareCanonical(c.Type, a, nil)
					require.NoError(t, err)
					assert.Equal(t, -1, got, "%#v before NULL", a)
					got, err = core.CompareCanonical(c.Type, (*string)(nil), a)
					require.NoError(t, err)
					assert.Equal(t, 1, got, "NULL after %#v", a)
				}
			}
			got, err := core.CompareCanonical(c.Type, nil, new(any))
			require.NoError(t, err)
			assert.Zero(t, got, "NULL equals NULL")
		})
	}
}

// A value its type cannot hold, NaN, a type with no order, and object
// values of different scalar kinds are errors: nothing is ordered by a
// second reading.
func TestCompareCanonical_Refuses(t *testing.T) {
	f64 := core.TypeInfo{Type: core.FieldTypeFloat, NumberKind: reflect.Float64}
	for name, tc := range map[string]struct {
		t       core.TypeInfo
		a, b    any
		wantErr string
	}{
		"text as an integer":       {core.TypeInfo{Type: core.FieldTypeInt}, "7", int64(7), "cannot compare int values string and int64"},
		"NaN":                      {f64, math.NaN(), 1.0, "cannot compare float values float64 and float64"},
		"not a UUID":               {core.TypeInfo{Type: core.FieldTypeUUID}, "nope", "00000000-0000-0000-0000-000000000001", `invalid UUID "nope"`},
		"a document":               {core.TypeInfo{Type: core.FieldTypeJSON}, "a", "b", "a json value has no order"},
		"a number and text object": {core.TypeInfo{Type: core.FieldTypeObject}, int64(1), "1", "cannot compare object values int64 and string"},
		"untyped mixed kinds":      {core.TypeInfo{}, true, "true", "cannot compare bool with string"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := core.CompareCanonical(tc.t, tc.a, tc.b)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}
