//go:build unit

package patch

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

type declaredZero int

func (declaredZero) IsZero() bool { return true }

type declaredNonZeroMap map[string]string

func (declaredNonZeroMap) IsZero() bool { return false }

func TestDeclaredJSONReplacementOmissionMatchesJSON(t *testing.T) {
	type row struct {
		Any    any                        `json:"any,omitzero"`
		Zero   declaredZero               `json:"zero,omitzero"`
		Nil    interface{ IsZero() bool } `json:"nil,omitzero"`
		NilMap interface{ IsZero() bool } `json:"nil_map,omitzero"`
		Map    declaredNonZeroMap         `json:"map,omitzero"`
	}
	var nilZero *declaredZero
	value := row{Any: declaredZero(7), Zero: 7, Nil: nilZero, NilMap: declaredNonZeroMap(nil)}
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	assert.JSONEq(t, `{"any":7,"nil_map":null,"map":null}`, string(raw))
	var emitted map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &emitted))
	fields, err := core.StructFieldsOf(reflect.TypeFor[row](), "json", "db")
	require.NoError(t, err)
	for field := range fields.Fields() {
		fv, err := field.Value(reflect.ValueOf(value))
		require.NoError(t, err)
		_, present := emitted[field.External()]
		assert.Equal(t, !present, jsonFieldOmitted(field, fv), field.External())
	}
}

type omitEmptyText string

func (omitEmptyText) MarshalJSON() ([]byte, error) { return []byte(`"encoded"`), nil }

// jsonFieldOmitted's omitempty is encoding/json's: false, 0, a nil pointer
// or interface, and an empty array, slice, map or string, whatever the value
// marshals as; never a struct.
func TestDeclaredJSONOmitEmptyMatchesJSON(t *testing.T) {
	type inner struct{ A int }
	type row struct {
		False      bool            `json:"false,omitempty"`
		True       bool            `json:"true,omitempty"`
		Zero       int             `json:"zero,omitempty"`
		One        int8            `json:"one,omitempty"`
		Uint       uint            `json:"uint,omitempty"`
		Uintptr    uintptr         `json:"uintptr,omitempty"`
		Float      float64         `json:"float,omitempty"`
		NegZero    float64         `json:"neg_zero,omitempty"`
		Float32    float32         `json:"float32,omitempty"`
		Empty      string          `json:"empty,omitempty"`
		Text       string          `json:"text,omitempty"`
		NilSlice   []int           `json:"nil_slice,omitempty"`
		EmptySlice []int           `json:"empty_slice,omitempty"`
		Slice      []int           `json:"slice,omitempty"`
		NilMap     map[string]int  `json:"nil_map,omitempty"`
		EmptyMap   map[string]int  `json:"empty_map,omitempty"`
		NilPtr     *int            `json:"nil_ptr,omitempty"`
		ZeroPtr    *int            `json:"zero_ptr,omitempty"`
		NilAny     any             `json:"nil_any,omitempty"`
		ZeroAny    any             `json:"zero_any,omitempty"`
		EmptyArray [0]int          `json:"empty_array,omitempty"`
		ZeroArray  [1]int          `json:"zero_array,omitempty"`
		Struct     inner           `json:"struct,omitempty"`
		Raw        json.RawMessage `json:"raw,omitempty"`
		Custom     omitEmptyText   `json:"custom,omitempty"`
		Quoted     int64           `json:"quoted,string,omitempty"`
	}
	zero := 0
	value := row{
		True: true, One: 1, NegZero: math.Copysign(0, -1), Text: "x",
		EmptySlice: []int{}, Slice: []int{0}, EmptyMap: map[string]int{},
		ZeroPtr: &zero, ZeroAny: 0,
	}
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	var emitted map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &emitted))
	fields, err := core.StructFieldsOf(reflect.TypeFor[row](), "json", "db")
	require.NoError(t, err)
	for field := range fields.Fields() {
		fv, err := field.Value(reflect.ValueOf(value))
		require.NoError(t, err)
		_, present := emitted[field.External()]
		assert.Equal(t, !present, jsonFieldOmitted(field, fv), "%s (encoding/json wrote %s)", field.External(), raw)
	}
}
