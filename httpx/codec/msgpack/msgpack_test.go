//go:build unit

package msgpack

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/ashtonian/pkg/httpx"
)

type testUser struct {
	ID   int    `msgpack:"id"`
	Name string `msgpack:"name"`
}

func TestMsgpackRegistered(t *testing.T) {
	// The init() function should have registered msgpack
	if !httpx.HasCodec(httpx.ContentTypeMsgpack) {
		t.Error("expected msgpack to be registered")
	}
}

func TestMsgpackCodec(t *testing.T) {
	user := testUser{ID: 1, Name: "John"}

	data, err := Codec.Encode(user)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	if Codec.ContentType() != httpx.ContentTypeMsgpack {
		t.Errorf("expected content type %s, got %s", httpx.ContentTypeMsgpack, Codec.ContentType())
	}

	var decoded testUser
	if err := Codec.Decode(data, &decoded); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if decoded.ID != user.ID || decoded.Name != user.Name {
		t.Errorf("expected %+v, got %+v", user, decoded)
	}
}

// A nil MessagePack value is the JSON data model's null: decoding through a
// pointer to a pointer, as httpx's typed helpers and DecodeResponse do,
// leaves it nil, and a zero-valued map still yields a non-nil pointer. Both
// codecs follow it.
func TestNilLeavesAPointerNil(t *testing.T) {
	type item struct {
		Name string `json:"name" msgpack:"name"`
	}
	for _, codec := range []httpx.Codec{Codec, NativeCodec} {
		for _, tt := range []struct {
			name    string
			value   any
			wantNil bool
		}{
			{"nil", nil, true},
			{"zero-valued map", map[string]any{}, false},
			{"value", item{Name: "Ada"}, false},
		} {
			t.Run(fmt.Sprintf("%T/%s", codec, tt.name), func(t *testing.T) {
				data, err := codec.Encode(tt.value)
				if err != nil {
					t.Fatal(err)
				}
				var got *item
				if err := codec.Decode(data, &got); err != nil || (got == nil) != tt.wantNil {
					t.Fatalf("Decode = %v, %v", got, err)
				}
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {httpx.ContentTypeMsgpack}}, Body: io.NopCloser(bytes.NewReader(data))}
				var explicit *item
				if err := httpx.DecodeResponse(resp, codec, &explicit); err != nil || (explicit == nil) != tt.wantNil {
					t.Fatalf("DecodeResponse = %v, %v", explicit, err)
				}
				if !tt.wantNil && *got != *explicit {
					t.Fatalf("Decode %+v, DecodeResponse %+v", *got, *explicit)
				}
			})
		}
	}
}
