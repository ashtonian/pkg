//go:build unit

package parsepgx

import (
	"encoding/json"
	"errors"
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

func TestJSONScanner_ReplacesOnlyAfterDecodeAndKeyValidation(t *testing.T) {
	value := map[int]string{9: "retained"}
	scan := jsonScanner{target: &value}
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
}

func TestJSONScanner_CustomDecoderAndPresence(t *testing.T) {
	value := decodedJSON{Value: "old", Calls: 7}
	scan := jsonScanner{target: &value}
	require.NoError(t, scan.Scan([]byte(`"accepted"`)))
	assert.Equal(t, decodedJSON{Value: "accepted", Calls: 1}, value)
	require.ErrorIs(t, scan.Scan(`"reject"`), errJSONDecode)
	assert.Equal(t, decodedJSON{Value: "accepted", Calls: 1}, value)

	var raw json.RawMessage
	require.NoError(t, (jsonScanner{target: &raw}).Scan(`null`))
	assert.Equal(t, json.RawMessage(`null`), raw)
	var dynamic any = "retained"
	require.Error(t, (jsonScanner{target: &dynamic}).Scan(`null`))
	assert.Equal(t, "retained", dynamic)
	var pointer = new("retained")
	require.Error(t, (jsonScanner{target: &pointer}).Scan(`null`))
	assert.Equal(t, "retained", *pointer)
	require.NoError(t, (jsonScanner{target: &pointer}).Scan(nil))
	assert.Nil(t, pointer)
}
