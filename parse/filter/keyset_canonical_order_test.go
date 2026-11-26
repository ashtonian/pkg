//go:build unit

package filter_test

import (
	"cmp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/ordertest"
)

// A keyset orders key values as their type's case orders them, in every
// spelling, NULL after every value.
func TestCompareKeyValues_CanonicalOrder(t *testing.T) {
	for _, c := range ordertest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			key := filter.CursorKey{Name: "v", Column: "v", DataType: c.Type.Type, NumberKind: c.Type.NumberKind}
			for i, x := range c.Values {
				for _, a := range x.Spellings {
					for j, y := range c.Values {
						for _, b := range y.Spellings {
							got, err := filter.CompareKeyValues(key, a, b)
							require.NoError(t, err, "%#v vs %#v", a, b)
							assert.Equal(t, cmp.Compare(i, j), got, "%#v vs %#v", a, b)
						}
					}
					got, err := filter.CompareKeyValues(key, a, nil)
					require.NoError(t, err)
					assert.Equal(t, -1, got, "%#v before NULL", a)
				}
			}
		})
	}
}

// Values a key's type cannot order are an error, never ordered by their
// text.
func TestCompareKeyValues_RefusesWhatItsTypeCannotOrder(t *testing.T) {
	_, err := filter.CompareKeyValues(filter.CursorKey{Name: "n", DataType: core.FieldTypeInt}, "7", int64(7))
	require.ErrorContains(t, err, `comparing values of key "n": cannot compare int values string and int64`)
}
