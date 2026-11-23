//go:build unit

package httpjson

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Buffering changes no byte of a response: json.Encoder's escaping and its
// trailing newline stay.
func TestEncode_WritesWhatAStreamingEncoderWould(t *testing.T) {
	v := map[string]any{"html": "<a href=x>", "n": 1}
	var streamed bytes.Buffer
	require.NoError(t, json.NewEncoder(&streamed).Encode(v))

	body, err := Encode(v)

	require.NoError(t, err)
	assert.Equal(t, streamed.Bytes(), body)
}
