//go:build unit

package cursor_test

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/internal/cursor"
)

// samplePayload exercises exact integer cursor values.
type samplePayload struct {
	Field string `msgpack:"f"`
	Value int64  `msgpack:"v"`
}

// pageField and pageToken have the wire shape of parse/filter's PageToken:
// an interface-typed value is what the decoder panicked on.
type pageField struct {
	Name      string `msgpack:"n"`
	Value     any    `msgpack:"v"`
	Direction string `msgpack:"d"`
}

type pageToken struct {
	Fields  []pageField `msgpack:"f"`
	Reverse bool        `msgpack:"r,omitempty"`
}

// The envelope retains exact integers and checks the generation.
func TestEnvelopeRoundTrip(t *testing.T) {
	// An int64 above 2^53 survives: the payload is msgpack, which keeps it
	// an integer where JSON would read a float64.
	want := samplePayload{Field: "created_at", Value: 9_007_199_254_740_993}

	cur, err := cursor.Encode(want, cursor.WithGeneration(3))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if cur == "" {
		t.Fatal("Encode returned an empty cursor")
	}

	got, err := cursor.Decode[samplePayload](cur, cursor.ExpectGeneration(3))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != want {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
}

func TestEnvelopeGenerationMismatch(t *testing.T) {
	cur, err := cursor.Encode(samplePayload{Field: "x"}, cursor.WithGeneration(1))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := cursor.Decode[samplePayload](cur, cursor.ExpectGeneration(2)); !errors.Is(err, cursor.ErrCursorGeneration) {
		t.Fatalf("gen mismatch: err = %v, want ErrCursorGeneration", err)
	}
}

func TestEnvelopeMalformed(t *testing.T) {
	if _, err := cursor.Decode[samplePayload]("!!!not-base64!!!"); !errors.Is(err, cursor.ErrInvalidCursor) {
		t.Fatalf("malformed: err = %v, want ErrInvalidCursor", err)
	}
}

// A token's wire bytes are pinned: Encode writes exactly these, and Decode
// reads them back.
func TestEncodedBytesArePinned(t *testing.T) {
	tests := []struct {
		name  string
		token pageToken
		wire  string
	}{
		{
			name: "two keys, reverse seek",
			token: pageToken{
				Fields: []pageField{
					{Name: "created_at", Value: int64(9_007_199_254_740_993), Direction: "desc"},
					{Name: "id", Value: "a1", Direction: "asc"},
				},
				Reverse: true,
			},
			wire: "gqFnzgAAAAOhcIKhZpKDoW6qY3JlYXRlZF9hdKF20wAgAAAAAAABoWSkZGVzY4OhbqJpZKF2omExoWSjYXNjoXLD",
		},
		{
			name:  "one key",
			token: pageToken{Fields: []pageField{{Name: "id", Value: "a", Direction: "asc"}}},
			wire:  "gqFnzgAAAAOhcIGhZpGDoW6iaWShdqFhoWSjYXNj",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cursor.Encode(tt.token, cursor.WithGeneration(3))
			require.NoError(t, err)
			assert.Equal(t, tt.wire, got)

			decoded, err := cursor.Decode[pageToken](tt.wire, cursor.ExpectGeneration(3))
			require.NoError(t, err)
			assert.Equal(t, tt.token, decoded)
		})
	}
}

// A map that names a member twice made msgpack decode the second occurrence
// into the interface the first one filled, which panics. Each shape is
// ErrInvalidCursor instead.
func TestDecodeRefusesRepeatedMembers(t *testing.T) {
	field := func(value ...[]byte) []byte {
		parts := [][]byte{{byte(0x80 | (len(value) + 2))}, fixstr("n"), fixstr("id")}
		for _, v := range value {
			parts = append(parts, fixstr("v"), v)
		}
		return cat(append(parts, fixstr("d"), fixstr("asc"))...)
	}
	payload := func(fields ...[]byte) []byte {
		return cat(cat([]byte{0x81}, fixstr("f"), []byte{byte(0x90 | len(fields))}), cat(fields...))
	}
	env := func(p []byte) []byte { return cat([]byte{0x82}, fixstr("g"), []byte{0x03}, fixstr("p"), p) }

	tests := []struct {
		name string
		wire string
	}{
		{"repeated string value, as issued", "gqFnA6FwgaFmkYShbqJpZKF2oWGhdqFioWSjYXNj"},
		{"repeated integer value, as issued", "gqFnA6FwgaFmkYShbqJpZKF2AaF2AqFko2FzYw"},
		{"repeated value", b64(env(payload(field(fixstr("a"), fixstr("b")))))},
		{"repeated value, fixstr and str8 names", b64(env(payload(cat(
			[]byte{0x84}, fixstr("n"), fixstr("id"), fixstr("v"), fixstr("a"),
			[]byte{0xd9, 0x01, 'v'}, fixstr("b"), fixstr("d"), fixstr("asc"),
		))))},
		{"repeated value, fixstr and bin8 names", b64(env(payload(cat(
			[]byte{0x84}, fixstr("n"), fixstr("id"), fixstr("v"), fixstr("a"),
			[]byte{0xc4, 0x01, 'v'}, fixstr("b"), fixstr("d"), fixstr("asc"),
		))))},
		{"repeated field list", b64(env(cat(
			[]byte{0x82}, fixstr("f"), []byte{0x91}, field(fixstr("a")),
			fixstr("f"), []byte{0x91}, field(fixstr("b")),
		)))},
		{"repeated payload", b64(cat(
			[]byte{0x83}, fixstr("g"), []byte{0x03},
			fixstr("p"), payload(field(fixstr("a"))),
			fixstr("p"), payload(field(fixstr("b"))),
		))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() {
				_, err = cursor.Decode[pageToken](tt.wire, cursor.ExpectGeneration(3))
			})
			assert.ErrorIs(t, err, cursor.ErrInvalidCursor)
		})
	}
}

// A member no struct of the token declares, at any level, is refused, and so
// is an envelope that is not a map or holds no payload.
func TestDecodeRefusesUndefinedMembers(t *testing.T) {
	onePayload := cat([]byte{0x81}, fixstr("f"), []byte{0x91, 0x83}, fixstr("n"), fixstr("id"), fixstr("v"), fixstr("a"), fixstr("d"), fixstr("asc"))

	tests := []struct {
		name string
		wire []byte
	}{
		{"undefined envelope member z", cat([]byte{0x83}, fixstr("g"), []byte{0x03}, fixstr("p"), onePayload, fixstr("z"), []byte{0x01})},
		{"undefined envelope member e", cat([]byte{0x83}, fixstr("g"), []byte{0x03}, fixstr("e"), []byte{0x01}, fixstr("p"), onePayload)},
		{"undefined envelope member h", cat([]byte{0x83}, fixstr("g"), []byte{0x03}, fixstr("h"), []byte{0x09}, fixstr("p"), onePayload)},
		{"integer envelope key", cat([]byte{0x83}, fixstr("g"), []byte{0x03}, fixstr("p"), onePayload, []byte{0x01, 0x01})},
		{"undefined payload member", cat([]byte{0x82}, fixstr("g"), []byte{0x03}, fixstr("p"), cat([]byte{0x82}, onePayload[1:], fixstr("x"), []byte{0xc3}))},
		{"undefined field member", cat([]byte{0x82}, fixstr("g"), []byte{0x03}, fixstr("p"), cat([]byte{0x81}, fixstr("f"), []byte{0x91, 0x84}, fixstr("n"), fixstr("id"), fixstr("v"), fixstr("a"), fixstr("d"), fixstr("asc"), fixstr("w"), []byte{0x01}))},
		{"envelope as an array", cat([]byte{0x92, 0x03}, onePayload)},
		{"envelope as nil", []byte{0xc0}},
		{"envelope without a payload", cat([]byte{0x81}, fixstr("g"), []byte{0x03})},
		{"empty envelope", []byte{0x80}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cursor.Decode[pageToken](b64(tt.wire), cursor.ExpectGeneration(3))
			assert.ErrorIs(t, err, cursor.ErrInvalidCursor)
		})
	}

	// The same payload in a well-formed envelope decodes.
	_, err := cursor.Decode[pageToken](b64(cat([]byte{0x82}, fixstr("g"), []byte{0x03}, fixstr("p"), onePayload)), cursor.ExpectGeneration(3))
	assert.NoError(t, err)
}

// The generation is compared before the payload is decoded: a token of
// another generation is ErrCursorGeneration whatever its payload holds, and
// only a token of the expected generation is held to the payload's members.
// The token's structure and the envelope's own members are checked first,
// whatever the generation.
func TestDecodeComparesGenerationBeforePayload(t *testing.T) {
	field := cat([]byte{0x83}, fixstr("n"), fixstr("id"), fixstr("v"), fixstr("a"), fixstr("d"), fixstr("asc"))
	fieldWithMember := cat([]byte{0x84}, fixstr("n"), fixstr("id"), fixstr("v"), fixstr("a"), fixstr("d"), fixstr("asc"), fixstr("w"), []byte{0x01})
	payload := cat([]byte{0x81}, fixstr("f"), []byte{0x91}, field)
	payloadWithMember := cat([]byte{0x82}, fixstr("f"), []byte{0x91}, field, fixstr("x"), []byte{0xc3})
	payloadWithFieldMember := cat([]byte{0x81}, fixstr("f"), []byte{0x91}, fieldWithMember)
	env := func(generation []byte, p []byte) []byte {
		return cat([]byte{0x82}, fixstr("g"), generation, fixstr("p"), p)
	}

	tests := []struct {
		name string
		wire []byte
		want error
	}{
		{"a newer generation with an undefined payload member", env([]byte{0x04}, payloadWithMember), cursor.ErrCursorGeneration},
		{"an older generation with an undefined field member", env([]byte{0x02}, payloadWithFieldMember), cursor.ErrCursorGeneration},
		{"another generation with the expected members", env([]byte{0x04}, payload), cursor.ErrCursorGeneration},
		{"another generation with a string payload", env([]byte{0x04}, fixstr("x")), cursor.ErrCursorGeneration},
		{"another generation with an array payload", env([]byte{0x04}, []byte{0x93, 0x01, 0x02, 0x03}), cursor.ErrCursorGeneration},
		{"another generation as a uint64", env([]byte{0xcf, 0, 0, 0, 0, 0, 0, 0, 0x04}, payloadWithMember), cursor.ErrCursorGeneration},
		{"no generation", cat([]byte{0x81}, fixstr("p"), payloadWithMember), cursor.ErrCursorGeneration},
		{"the expected generation with an undefined payload member", env([]byte{0x03}, payloadWithMember), cursor.ErrInvalidCursor},
		{"the expected generation with an undefined field member", env([]byte{0x03}, payloadWithFieldMember), cursor.ErrInvalidCursor},
		{"the expected generation with a string payload", env([]byte{0x03}, fixstr("x")), cursor.ErrInvalidCursor},
		{"another generation with an undefined envelope member", cat([]byte{0x83}, fixstr("g"), []byte{0x04}, fixstr("p"), payload, fixstr("e"), []byte{0x01}), cursor.ErrInvalidCursor},
		{"another generation with a repeated payload member", env([]byte{0x04}, cat([]byte{0x82}, fixstr("f"), []byte{0x90}, fixstr("f"), []byte{0x90})), cursor.ErrInvalidCursor},
		{"another generation with a hostile payload length", env([]byte{0x04}, []byte{0xdd, 0xff, 0xff, 0xff, 0xff}), cursor.ErrInvalidCursor},
		{"a repeated generation", cat([]byte{0x83}, fixstr("g"), []byte{0x04}, fixstr("g"), []byte{0x03}, fixstr("p"), payload), cursor.ErrInvalidCursor},
		{"a generation beyond 32 bits", env([]byte{0xcf, 0, 0, 0, 1, 0, 0, 0, 0x03}, payload), cursor.ErrInvalidCursor},
		{"a negative generation", env([]byte{0xff}, payload), cursor.ErrInvalidCursor},
		{"a string generation", env(fixstr("3"), payload), cursor.ErrInvalidCursor},
		{"a nil generation", env([]byte{0xc0}, payload), cursor.ErrInvalidCursor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cursor.Decode[pageToken](b64(tt.wire), cursor.ExpectGeneration(3))
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want)
		})
	}

	got, err := cursor.Decode[pageToken](b64(env([]byte{0xcd, 0x00, 0x03}, payload)), cursor.ExpectGeneration(3))
	require.NoError(t, err, "the expected generation as a uint16")
	assert.Equal(t, pageToken{Fields: []pageField{{Name: "id", Value: "a", Direction: "asc"}}}, got)
}

func fixstr(s string) []byte { return append([]byte{0xa0 | byte(len(s))}, s...) }

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func b64(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }
