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
	"github.com/ashtonian/pkg/parse/internal/parsetest"
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
	require.ErrorContains(t, err, `comparing values of key "n": int values string and int64 are incomparable`)
}

// Every spelling of a key type's value, the Go values drivers scan a column
// into among them (a time.Time for a date, a time of day and a timestamp),
// mints through KeyValue a cursor the next request accepts (EncodeCursor,
// DecodeCursor, ValidateCursor) holding the value its case lists.
func TestKeyValue_CursorsRoundTripEverySpelling(t *testing.T) {
	for _, c := range ordertest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			key := filter.CursorKey{Name: "v", Column: "v", DataType: c.Type.Type, NumberKind: c.Type.NumberKind, Nullable: true, Direction: filter.SortAsc}
			for _, x := range c.Values {
				for _, a := range x.Spellings {
					resumed := parsetest.ResumeKeyValue(t, key, a)
					got, err := filter.CompareKeyValues(key, resumed, x.Spellings[0])
					require.NoError(t, err, "%#v resumed as %#v", a, resumed)
					assert.Zero(t, got, "%#v resumed as %#v", a, resumed)
				}
			}
		})
	}
}
