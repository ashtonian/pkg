//go:build unit

package parse

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type decodedJSON struct {
	Value string
	Calls int
}

var errJSONDecode = errors.New("custom JSON decoder rejected value")

func (v *decodedJSON) UnmarshalJSON(raw []byte) error {
	v.Calls++
	if err := json.Unmarshal(raw, &v.Value); err != nil {
		return err
	}
	if v.Value == "reject" {
		return errJSONDecode
	}
	return nil
}

func columnOf(target any) jsonColumn { return jsonColumn{target: reflect.ValueOf(target).Elem()} }

func TestJSONColumn_ReplacesOnlyAfterDecodeAndKeyValidation(t *testing.T) {
	value := map[int]string{9: "retained"}
	scan := columnOf(&value)
	require.NoError(t, scan.Scan(`{"1":"one"}`))
	assert.Equal(t, map[int]string{1: "one"}, value)
	for _, input := range []string{`{"01":"lossy"}`, `{"1":"one","01":"collision"}`, `{"1":"one","1":"duplicate"}`, `{"2":`} {
		require.Error(t, scan.Scan(input))
		assert.Equal(t, map[int]string{1: "one"}, value)
	}
	require.Error(t, scan.Scan(`null`))
	assert.Equal(t, map[int]string{1: "one"}, value)
	require.NoError(t, scan.Scan(nil))
	assert.Nil(t, value)
	assert.ErrorContains(t, scan.Scan(7), "JSON scan source must be string or []byte, got int")
}

func TestJSONColumn_CustomDecoderAndPresence(t *testing.T) {
	value := decodedJSON{Value: "old", Calls: 7}
	scan := columnOf(&value)
	require.NoError(t, scan.Scan([]byte(`"accepted"`)))
	assert.Equal(t, decodedJSON{Value: "accepted", Calls: 1}, value)
	require.ErrorIs(t, scan.Scan(`"reject"`), errJSONDecode)
	assert.Equal(t, decodedJSON{Value: "accepted", Calls: 1}, value)

	var raw json.RawMessage
	require.NoError(t, columnOf(&raw).Scan(`null`))
	assert.Equal(t, json.RawMessage(`null`), raw)
	var dynamic any = "retained"
	require.Error(t, columnOf(&dynamic).Scan(`null`))
	assert.Equal(t, "retained", dynamic)
	var pointer = new("retained")
	require.Error(t, columnOf(&pointer).Scan(`null`))
	assert.Equal(t, "retained", *pointer)
	require.NoError(t, columnOf(&pointer).Scan(nil))
	assert.Nil(t, pointer)
}
