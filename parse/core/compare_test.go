//go:build unit

package core_test

import (
	"cmp"
	"database/sql"
	"encoding/json"
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

// A float column orders as PostgreSQL's float8 does: -Infinity, the
// finite values, +Infinity, then NaN, every NaN equal, NULL after all; a
// float32 column alike, its greatest finite value float32's.
func TestCompareCanonical_NonFiniteFloats(t *testing.T) {
	payload := math.Float64frombits(0x7ff8000000000001)
	for kind, greatest := range map[reflect.Kind]float64{reflect.Float64: math.MaxFloat64, reflect.Float32: math.MaxFloat32} {
		ascending := [][]any{
			{math.Inf(-1), float32(math.Inf(-1))},
			{-1.5, json.Number("-1.5")},
			{0.0, int64(0)},
			{greatest},
			{math.Inf(1), float32(math.Inf(1))},
			{math.NaN(), payload, float32(math.NaN()), sql.NullFloat64{Float64: math.NaN(), Valid: true}},
			{nil, sql.NullFloat64{}},
		}
		f := core.TypeInfo{Type: core.FieldTypeFloat, NumberKind: kind}
		for i, xs := range ascending {
			for j, ys := range ascending {
				for _, a := range xs {
					for _, b := range ys {
						got, err := core.CompareCanonical(f, a, b)
						require.NoError(t, err, "%#v vs %#v", a, b)
						assert.Equal(t, cmp.Compare(i, j), got, "%s: %#v vs %#v", kind, a, b)
					}
				}
			}
		}
	}
}

// A value its type cannot hold, NaN outside a float column, a type with
// no order, and object values of different scalar kinds are errors:
// nothing is ordered by a second reading.
func TestCompareCanonical_Refuses(t *testing.T) {
	for name, tc := range map[string]struct {
		t       core.TypeInfo
		a, b    any
		wantErr string
	}{
		"text as an integer":       {core.TypeInfo{Type: core.FieldTypeInt}, "7", int64(7), "cannot compare int values string and int64"},
		"NaN as an integer":        {core.TypeInfo{Type: core.FieldTypeInt}, math.NaN(), int64(1), "cannot compare int values float64 and int64"},
		"NaN as a decimal":         {core.TypeInfo{Type: core.FieldTypeDecimal}, math.NaN(), "1", "cannot compare decimal values float64 and string"},
		"a float no float32 holds": {core.TypeInfo{Type: core.FieldTypeFloat, NumberKind: reflect.Float32}, math.MaxFloat64, 0.0, "overflows float32"},
		"an untyped NaN":           {core.TypeInfo{}, math.NaN(), 1.0, "cannot compare float64 with float64"},
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
