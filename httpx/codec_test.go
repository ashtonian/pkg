//go:build unit

package httpx

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

type testUser struct {
	ID   int    `json:"id" xml:"id"`
	Name string `json:"name" xml:"name"`
}

func TestJSONCodec(t *testing.T) {
	user := testUser{ID: 1, Name: "John"}

	data, err := JSON.Encode(user)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	if JSON.ContentType() != "application/json" {
		t.Errorf("expected content type application/json, got %s", JSON.ContentType())
	}

	var decoded testUser
	if err := JSON.Decode(data, &decoded); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if decoded.ID != user.ID || decoded.Name != user.Name {
		t.Errorf("expected %+v, got %+v", user, decoded)
	}
}

func TestXMLCodec(t *testing.T) {
	user := testUser{ID: 1, Name: "John"}

	data, err := XML.Encode(user)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	if XML.ContentType() != "application/xml" {
		t.Errorf("expected content type application/xml, got %s", XML.ContentType())
	}

	var decoded testUser
	if err := XML.Decode(data, &decoded); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if decoded.ID != user.ID || decoded.Name != user.Name {
		t.Errorf("expected %+v, got %+v", user, decoded)
	}
}

func TestEncodeRequest(t *testing.T) {
	user := testUser{ID: 1, Name: "John"}

	req, _ := http.NewRequest("POST", "http://example.com", nil)
	if err := EncodeRequest(req, JSON, user); err != nil {
		t.Fatalf("encode request failed: %v", err)
	}

	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", req.Header.Get("Content-Type"))
	}

	body, _ := io.ReadAll(req.Body)
	if !bytes.Contains(body, []byte(`"name":"John"`)) {
		t.Errorf("unexpected body: %s", string(body))
	}
}

func TestDecodeResponse(t *testing.T) {
	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"id":1,"name":"John"}`))),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	var user testUser
	if err := DecodeResponse(resp, JSON, &user); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}

	if user.ID != 1 || user.Name != "John" {
		t.Errorf("expected {1, John}, got %+v", user)
	}
}

func TestRegistry_Codec(t *testing.T) {
	// Test that JSON and XML are registered by default
	if !HasCodec(ContentTypeJSON) {
		t.Error("expected JSON to be registered")
	}
	if !HasCodec(ContentTypeXML) {
		t.Error("expected XML to be registered")
	}

	// Msgpack and Proto should not be registered unless imported
	if HasCodec(ContentTypeMsgpack) {
		t.Error("expected msgpack to NOT be registered by default")
	}
	if HasCodec(ContentTypeProtobuf) {
		t.Error("expected protobuf to NOT be registered by default")
	}

	// Test GetCodec
	c, ok := GetCodec(ContentTypeJSON)
	if !ok {
		t.Error("expected to get JSON codec")
	}
	if c == nil {
		t.Error("expected non-nil codec")
	}

	// Test RegisteredCodecs
	codecs := RegisteredCodecs()
	if len(codecs) < 2 {
		t.Errorf("expected at least 2 registered codecs, got %d", len(codecs))
	}
}
