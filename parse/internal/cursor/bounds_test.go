//go:build unit

package cursor

import (
	"encoding/base64"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type boundsItem struct {
	Name  string `msgpack:"n"`
	Value any    `msgpack:"v"`
}

func TestCheckMsgpackBounds_AcceptsEncodedEnvelopes(t *testing.T) {
	payloads := []any{
		[]boundsItem{{Name: "a", Value: int64(-5)}, {Name: "b", Value: "x"}, {Name: "c", Value: nil}},
		map[string]any{"deep": map[string]any{"list": []any{1.5, true, uint64(1 << 63), strings.Repeat("s", 300)}}},
		[]byte{1, 2, 3},
		"",
	}
	for _, p := range payloads {
		cur, err := Encode(p, WithGeneration(7), WithTenantHash(42))
		require.NoError(t, err)
		data, err := base64.RawURLEncoding.DecodeString(string(cur))
		require.NoError(t, err)
		assert.NoError(t, checkMsgpackBounds(data), "%#v", p)
	}
}

func TestCheckMsgpackBounds_RejectsHostileLengths(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"array32 longer than the input", []byte{0x81, 0xa1, 'p', 0xdd, 0xff, 0xff, 0xff, 0xff}},
		{"map32 longer than the input", []byte{0xdf, 0x00, 0x10, 0x00, 0x00, 0xc0, 0xc0}},
		{"array16 one element short", []byte{0xdc, 0x00, 0x03, 0x01, 0x02}},
		{"str32 longer than the input", []byte{0xdb, 0x7f, 0xff, 0xff, 0xff, 'a'}},
		{"bin8 longer than the input", []byte{0xc4, 0x05, 0x01}},
		{"ext32 longer than the input", []byte{0xc9, 0x00, 0x01, 0x00, 0x00, 0x05}},
		{"truncated float64", []byte{0xcb, 0x00, 0x00}},
		{"unused type byte", []byte{0xc1}},
		{"trailing bytes", []byte{0xc0, 0xc0}},
		{"empty", nil},
		{"nesting beyond the limit", append(bytesRepeat(0x91, maxEnvelopeDepth+1), 0xc0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Error(t, checkMsgpackBounds(tt.data))
		})
	}
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// A cursor declaring four billion payload elements is rejected without the
// decoder allocating for them (review N01: msgpack sizes the slice from the
// header before reading any element).
func TestDecode_HostileLengthDoesNotAllocate(t *testing.T) {
	hostile := base64.RawURLEncoding.EncodeToString([]byte{0x81, 0xa1, 'p', 0xdd, 0xff, 0xff, 0xff, 0xff, 0x80})

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := Decode[[]boundsItem](hostile)
	runtime.ReadMemStats(&after)

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidCursor))
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(64<<10), "decode must not allocate for the declared length")
}
