package jsonvalue_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/jsonvalue"
)

func TestFreshReplacesWithoutMutatingAliases(t *testing.T) {
	old := map[string]int{"old": 1}
	value := old
	require.NoError(t, jsonvalue.UnmarshalFresh([]byte(`{"new":2}`), &value))
	assert.Equal(t, map[string]int{"new": 2}, value)
	assert.Equal(t, map[string]int{"old": 1}, old)

	slice := []int{8, 9, 10}
	alias := slice
	require.NoError(t, jsonvalue.UnmarshalFresh([]byte(`[1]`), &slice))
	assert.Equal(t, []int{1}, slice)
	assert.Equal(t, []int{8, 9, 10}, alias)

	record := struct{ Old, New int }{Old: 1}
	require.NoError(t, jsonvalue.UnmarshalFresh([]byte(`{"New":2}`), &record))
	assert.Zero(t, record.Old)
	assert.Equal(t, 2, record.New)
}

func TestFreshFailurePreservesDestinationAndErrorType(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		check       func(*testing.T, error)
	}{
		{"type", `{"new":3,"bad":"integer"}`, func(t *testing.T, err error) {
			var target *json.UnmarshalTypeError
			assert.ErrorAs(t, err, &target)
		}},
		{"syntax", `{"new":3,`, func(t *testing.T, err error) {
			var target *json.SyntaxError
			assert.ErrorAs(t, err, &target)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := map[string]int{"old": 1}
			alias := value
			err := jsonvalue.UnmarshalFresh([]byte(tc.input), &value)
			require.Error(t, err)
			tc.check(t, err)
			assert.Equal(t, map[string]int{"old": 1}, value)
			assert.Equal(t, map[string]int{"old": 1}, alias)
		})
	}
}

var errDecode = errors.New("custom decoder rejected value")

type customValue struct{ Value int }

func (v *customValue) UnmarshalJSON(data []byte) error {
	if v.Value != 0 {
		return errors.New("receiver was not fresh")
	}
	v.Value = 42
	if string(data) == `"reject"` {
		return errDecode
	}
	return nil
}

func TestFreshCustomDecoderUsesFreshReceiverAndPreservesCause(t *testing.T) {
	value := customValue{Value: 7}
	require.ErrorIs(t, jsonvalue.UnmarshalFresh([]byte(`"reject"`), &value), errDecode)
	assert.Equal(t, 7, value.Value)
	require.NoError(t, jsonvalue.UnmarshalFresh([]byte(`"accept"`), &value))
	assert.Equal(t, 42, value.Value)
}

func TestInvalidDestination(t *testing.T) {
	for _, decode := range []struct {
		name string
		call func([]byte, any) error
	}{
		{"fresh", jsonvalue.UnmarshalFresh}, {"present", jsonvalue.UnmarshalPresent},
	} {
		t.Run(decode.name, func(t *testing.T) {
			for _, target := range []any{nil, "not a pointer", (*string)(nil)} {
				var invalid *json.InvalidUnmarshalError
				assert.ErrorAs(t, decode.call([]byte(`"text"`), target), &invalid)
			}
		})
	}
}

func TestPresenceRejectsNilWithoutReplacement(t *testing.T) {
	number := 7
	for _, tc := range []struct {
		name         string
		target, want any
	}{
		{"map", new(map[string]int{"old": 1}), new(map[string]int{"old": 1})},
		{"slice", new([]int{1}), new([]int{1})},
		{"pointer", new(&number), new(&number)},
		{"interface", new(any("retained")), new(any("retained"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, jsonvalue.UnmarshalPresent([]byte("null"), tc.target))
			assert.Equal(t, tc.want, tc.target)
			require.NoError(t, jsonvalue.UnmarshalFresh([]byte("null"), tc.target))
			assert.True(t, reflect.ValueOf(tc.target).Elem().IsNil())
		})
	}
}

func TestPresenceRetainsEmptyValuesAndRawNull(t *testing.T) {
	value := map[string]any{"old": true}
	require.NoError(t, jsonvalue.UnmarshalPresent([]byte("{}"), &value))
	assert.NotNil(t, value)
	assert.Empty(t, value)
	slice := []int{1}
	require.NoError(t, jsonvalue.UnmarshalPresent([]byte("[]"), &slice))
	assert.NotNil(t, slice)
	assert.Empty(t, slice)
	raw := json.RawMessage(`{"old":true}`)
	require.NoError(t, jsonvalue.UnmarshalPresent([]byte("null"), &raw))
	assert.Equal(t, json.RawMessage("null"), raw)
	scalar := 7
	require.NoError(t, jsonvalue.UnmarshalPresent([]byte("null"), &scalar))
	assert.Zero(t, scalar)
}
