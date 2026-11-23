//go:build unit

package cursor

import (
	"encoding/base64"
	"errors"
	"testing"
)

type fuzzField struct {
	Name      string `msgpack:"n"`
	Value     any    `msgpack:"v"`
	Direction string `msgpack:"d"`
}

type fuzzToken struct {
	Fields  []fuzzField `msgpack:"f"`
	Reverse bool        `msgpack:"r,omitempty"`
}

// fuzzFutureField has a member fuzzField lacks, as a later generation's
// token might.
type fuzzFutureField struct {
	Name      string `msgpack:"n"`
	Value     any    `msgpack:"v"`
	Direction string `msgpack:"d"`
	Collation string `msgpack:"c"`
}

type fuzzFutureToken struct {
	Fields []fuzzFutureField `msgpack:"f"`
}

// FuzzDecode feeds Decode arbitrary tokens. Decode must answer every one with
// a payload or with ErrInvalidCursor or ErrCursorGeneration, and a token
// whose envelope reads but names another generation must be
// ErrCursorGeneration. The payload decoder is also run without the recover
// Decode wraps it in, whatever the generation, so a shape the structural
// checks let through to a decoder panic fails here rather than being absorbed.
func FuzzDecode(f *testing.F) {
	for _, tok := range []fuzzToken{
		{Fields: []fuzzField{{Name: "id", Value: "a", Direction: "asc"}}},
		{Fields: []fuzzField{{Name: "n", Value: int64(-1 << 62), Direction: "desc"}, {Name: "id", Value: nil, Direction: "asc"}}, Reverse: true},
		{Fields: []fuzzField{{Name: "x", Value: 1.5, Direction: "asc"}, {Name: "b", Value: true, Direction: "asc"}}},
	} {
		cur, err := Encode(tok, WithGeneration(3))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(cur)
	}
	for _, g := range []uint32{3, 4} {
		cur, err := Encode(fuzzFutureToken{Fields: []fuzzFutureField{{Name: "id", Value: "a", Direction: "asc", Collation: "C"}}}, WithGeneration(g))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(cur)
	}
	for _, s := range []string{
		"gqFnA6FwgaFmkYShbqJpZKF2oWGhdqFioWSjYXNj",
		"gqFnA6FwgaFmkYShbqJpZKF2AaF2AqFko2FzYw",
		"gqFnzgAAAAOhcIKhZpKDoW6qY3JlYXRlZF9hdKF20wAgAAAAAAABoWSkZGVzY4OhbqJpZKF2omExoWSjYXNjoXLD",
		"",
		"wA",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, cur string) {
		_, err := Decode[fuzzToken](cur, ExpectGeneration(3))
		if err != nil && !errors.Is(err, ErrInvalidCursor) && !errors.Is(err, ErrCursorGeneration) {
			t.Fatalf("Decode(%q) = %v, want ErrInvalidCursor or ErrCursorGeneration", cur, err)
		}
		data, b64err := base64.RawURLEncoding.DecodeString(cur)
		if b64err != nil {
			return
		}
		env, envErr := readEnvelope(data)
		if envErr != nil {
			return
		}
		if env.generation != 3 && !errors.Is(err, ErrCursorGeneration) {
			t.Fatalf("Decode(%q) of generation %d = %v, want ErrCursorGeneration", cur, env.generation, err)
		}
		_, _ = decodePayload[fuzzToken](env.payload)
	})
}
