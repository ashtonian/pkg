//go:build unit

package msgpack

import (
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
