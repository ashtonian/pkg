//go:build unit

package core_test

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

type textCount int64

func (c textCount) MarshalText() ([]byte, error) { return []byte(strconv.FormatInt(int64(c), 10)), nil }

type jsonCount int64

func (c jsonCount) MarshalJSON() ([]byte, error) { return []byte(strconv.FormatInt(int64(c), 10)), nil }

type countPtr *int64

type wired struct {
	ID       int64     `json:"id,string"`
	Big      uint64    `json:"big,string,omitempty"`
	Ratio    float64   `json:"ratio,string"`
	Small    float32   `json:"small,string"`
	On       bool      `json:"on,string"`
	Opt      *int64    `json:"opt,string"`
	Named    textCount `json:"named,string"`
	Self     jsonCount `json:"self,string"`
	Name     string    `json:"name,string"`
	Plain    int64     `json:"plain"`
	Omit     int64     `json:"omit,omitempty"`
	//lint:ignore SA5008 encoding/json ignores the option on a slice; so must Wire
	Tags     []int64   `json:"tags,string"`
	Indirect countPtr  `json:"indirect,string"`
}

// Wire follows encoding/json: the string option quotes a bool, integer or
// float field, through one unnamed pointer, unless the type marshals itself.
func TestFieldWire(t *testing.T) {
	walk, err := core.StructFieldsOf(reflect.TypeFor[wired](), "json", "db")
	require.NoError(t, err)
	want := map[string]core.Wire{
		"id": core.WireString, "big": core.WireString, "ratio": core.WireString, "small": core.WireString,
		"on": core.WireString, "opt": core.WireString,
		"named": "", "self": "", "name": "", "plain": "", "omit": "", "tags": "", "indirect": "",
	}
	for _, f := range walk.Fields {
		assert.Equal(t, want[f.External], f.Wire(), f.External)
	}

	// Each WireString field reads in responses as a JSON string.
	data, err := json.Marshal(wired{ID: 1, Big: 2, Ratio: 0.5, Small: 0.25, On: true, Opt: new(int64(3))})
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	for name, w := range want {
		if w == core.WireString {
			assert.IsType(t, "", doc[name], name)
		}
	}
}

func TestFromWire(t *testing.T) {
	tests := []struct {
		t    core.FieldType
		in   any
		want any
		err  string
	}{
		{core.FieldTypeInt, "9223372036854775807", json.Number("9223372036854775807"), ""},
		{core.FieldTypeInt, "-9223372036854775808", json.Number("-9223372036854775808"), ""},
		{core.FieldTypeInt, "18446744073709551615", json.Number("18446744073709551615"), ""},
		{core.FieldTypeInt, "0", json.Number("0"), ""},
		{core.FieldTypeFloat, "1.5e3", json.Number("1.5e3"), ""},
		{core.FieldTypeBool, "true", true, ""},
		{core.FieldTypeBool, "false", false, ""},
		{core.FieldTypeInt, nil, nil, ""},
		{core.FieldTypeInt, json.Number("1"), nil, "got a number"},
		{core.FieldTypeInt, float64(1), nil, "got a number"},
		{core.FieldTypeBool, true, nil, "got a boolean"},
		{core.FieldTypeInt, []any{"1"}, nil, "got an array"},
		{core.FieldTypeInt, " 1", nil, "does not hold a JSON number"},
		{core.FieldTypeInt, "1 ", nil, "does not hold a JSON number"},
		{core.FieldTypeInt, "+1", nil, "does not hold a JSON number"},
		{core.FieldTypeInt, "01", nil, "does not hold a JSON number"},
		{core.FieldTypeInt, "1_0", nil, "does not hold a JSON number"},
		{core.FieldTypeInt, "0x10", nil, "does not hold a JSON number"},
		{core.FieldTypeInt, "", nil, "does not hold a JSON number"},
		{core.FieldTypeInt, "true", nil, "does not hold a JSON number"},
		{core.FieldTypeBool, "True", nil, "does not hold true or false"},
		{core.FieldTypeBool, "1", nil, "does not hold true or false"},
		{core.FieldTypeString, "x", nil, "no string wire form"},
	}
	for _, tt := range tests {
		got, err := core.FromWire(core.WireString, tt.t, tt.in)
		if tt.err != "" {
			assert.ErrorContains(t, err, tt.err, "%v %#v", tt.t, tt.in)
			continue
		}
		require.NoError(t, err, "%v %#v", tt.t, tt.in)
		assert.Equal(t, tt.want, got, "%v %#v", tt.t, tt.in)
	}

	// The zero Wire passes every value through.
	got, err := core.FromWire("", core.FieldTypeInt, json.Number("1"))
	require.NoError(t, err)
	assert.Equal(t, json.Number("1"), got)
}

// FromWire, then NormalizeScalar, reads a quoted integer exactly, past
// 2^53 and at both 64-bit ends.
func TestFromWireNormalizesExactly(t *testing.T) {
	for _, tt := range []struct {
		kind reflect.Kind
		text string
		want any
	}{
		{reflect.Int64, "9007199254740993", int64(9007199254740993)},
		{reflect.Int64, "9223372036854775807", int64(math.MaxInt64)},
		{reflect.Int64, "-9223372036854775808", int64(math.MinInt64)},
		{reflect.Uint64, "18446744073709551615", uint64(math.MaxUint64)},
	} {
		v, err := core.FromWire(core.WireString, core.FieldTypeInt, tt.text)
		require.NoError(t, err)
		got, err := core.NormalizeScalar(core.FieldTypeInt, tt.kind, v)
		require.NoError(t, err, tt.text)
		assert.Equal(t, tt.want, got, tt.text)
	}
	v, err := core.FromWire(core.WireString, core.FieldTypeInt, "9223372036854775808")
	require.NoError(t, err)
	_, err = core.NormalizeScalar(core.FieldTypeInt, reflect.Int64, v)
	assert.Error(t, err, "past int64")
}

// ToWire writes what encoding/json writes, so a document holding it decodes
// back into the field.
func TestToWire(t *testing.T) {
	for _, v := range []any{int64(math.MinInt64), int64(9007199254740993), uint64(math.MaxUint64), 0.1, 1e21, 2.5e-7, true, false} {
		got, err := core.ToWire(core.WireString, v)
		require.NoError(t, err, "%v", v)
		s, ok := got.(string)
		require.True(t, ok, "%v", v)
		back, err := core.FromWire(core.WireString, wireType(v), s)
		require.NoError(t, err, "%v as %q", v, s)
		norm, err := core.NormalizeScalar(wireType(v), wireKind(v), back)
		require.NoError(t, err, "%v as %q", v, s)
		assert.Equal(t, v, norm, "%v as %q", v, s)
	}
	got, err := core.ToWire("", int64(1))
	require.NoError(t, err)
	assert.Equal(t, int64(1), got)
	got, err = core.ToWire(core.WireString, nil)
	require.NoError(t, err)
	assert.Nil(t, got)
	_, err = core.ToWire(core.WireString, "x")
	assert.Error(t, err)
}

func wireType(v any) core.FieldType {
	switch v.(type) {
	case bool:
		return core.FieldTypeBool
	case float64:
		return core.FieldTypeFloat
	}
	return core.FieldTypeInt
}

func wireKind(v any) reflect.Kind {
	switch v.(type) {
	case uint64:
		return reflect.Uint64
	case float64:
		return reflect.Float64
	case bool:
		return reflect.Bool
	}
	return reflect.Int64
}
