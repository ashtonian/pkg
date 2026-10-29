//go:build unit

package core_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

func boundsOf(t *testing.T, goType reflect.Type, tag string) (core.Bounds, error) {
	t.Helper()
	typ := reflect.StructOf([]reflect.StructField{{Name: "V", Type: goType, Tag: reflect.StructTag(`json:"v" ` + tag)}})
	walk, err := core.StructFieldsOf(typ, "json", "db")
	if err != nil {
		return core.Bounds{}, err
	}
	return walk.Fields[0].Tags.Bounds, nil
}

func TestBounds_ReadFromTags(t *testing.T) {
	tests := []struct {
		name string
		tag  string
		want core.Bounds
	}{
		{name: "none", tag: ``, want: core.Bounds{}},
		{name: "lengths", tag: `minlen:"3" maxlen:"3"`, want: core.Bounds{MinLen: new(3), MaxLen: new(3)}},
		{name: "an empty string's length", tag: `maxlen:"0"`, want: core.Bounds{MaxLen: new(0)}},
		{name: "integers", tag: `min:"-5" max:"10"`, want: core.Bounds{Min: "-5", Max: "10"}},
		{name: "decimals as canonical text", tag: `min:"0.010" max:"1.5e2"`, want: core.Bounds{Min: "0.01", Max: "150"}},
		{name: "equal bounds", tag: `min:"2" max:"2.0"`, want: core.Bounds{Min: "2", Max: "2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := boundsOf(t, reflect.TypeFor[string](), tt.tag)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.tag == "", got.IsZero())
		})
	}
}

// A malformed or contradictory bound fails the walk, whatever the field's
// capabilities, instead of bounding nothing.
func TestBounds_RefuseMalformedTags(t *testing.T) {
	tests := map[string]struct{ tag, want string }{
		"negative length":        {`maxlen:"-1"`, `maxlen "-1" is not a non-negative integer`},
		"fractional length":      {`minlen:"1.5"`, `minlen "1.5" is not a non-negative integer`},
		"empty length":           {`maxlen:""`, `maxlen "" is not a non-negative integer`},
		"minlen above maxlen":    {`minlen:"4" maxlen:"3"`, `minlen 4 exceeds maxlen 3`},
		"not a number":           {`min:"ten"`, `min: "ten" is not a decimal number`},
		"hexadecimal":            {`max:"0x10"`, `max: "0x10" is not a decimal number`},
		"min above max":          {`min:"10.5" max:"10.25"`, `min 10.5 exceeds max 10.25`},
		"negative min above max": {`min:"-1" max:"-2"`, `min -1 exceeds max -2`},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := boundsOf(t, reflect.TypeFor[int](), tt.tag)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

func TestBounds_CheckTheFieldType(t *testing.T) {
	length := core.Bounds{MaxLen: new(3)}
	number := core.Bounds{Max: "3"}
	tests := []struct {
		name   string
		bounds core.Bounds
		info   core.TypeInfo
		want   string
	}{
		{name: "length on a string", bounds: length, info: core.TypeInfo{Type: core.FieldTypeString}},
		{name: "length on strings", bounds: length, info: core.TypeInfo{Type: core.FieldTypeArray, Elem: core.FieldTypeString}},
		{name: "number on an int", bounds: number, info: core.TypeInfo{Type: core.FieldTypeInt, NumberKind: reflect.Int32}},
		{name: "number on a float", bounds: number, info: core.TypeInfo{Type: core.FieldTypeFloat, NumberKind: reflect.Float64}},
		{name: "number on a decimal", bounds: number, info: core.TypeInfo{Type: core.FieldTypeDecimal}},
		{name: "number on ints", bounds: number, info: core.TypeInfo{Type: core.FieldTypeArray, Elem: core.FieldTypeInt, NumberKind: reflect.Int16}},
		{name: "length on an int", bounds: length, info: core.TypeInfo{Type: core.FieldTypeInt}, want: "minlen and maxlen bound strings and arrays of strings, not int"},
		{name: "length on a document", bounds: length, info: core.TypeInfo{Type: core.FieldTypeJSON}, want: "not json"},
		{name: "number on a string", bounds: number, info: core.TypeInfo{Type: core.FieldTypeString}, want: "min and max bound numbers and arrays of numbers, not string"},
		{name: "number on uuids", bounds: number, info: core.TypeInfo{Type: core.FieldTypeArray, Elem: core.FieldTypeUUID}, want: "not an array of uuid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.bounds.Check(tt.info)
			if tt.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.want)
		})
	}
}
