//go:build unit

package proto

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/ashtonian/pkg/httpx"
	protobuf "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestProtoRegistered(t *testing.T) {
	// The init() function should have registered protobuf
	if !httpx.HasCodec(httpx.ContentTypeProtobuf) {
		t.Error("expected protobuf to be registered")
	}
}

func TestProtoCodecContentType(t *testing.T) {
	if Codec.ContentType() != httpx.ContentTypeProtobuf {
		t.Errorf("expected content type %s, got %s", httpx.ContentTypeProtobuf, Codec.ContentType())
	}
}

func TestProtoCodecNonProtoValue(t *testing.T) {
	// Test that encoding a non-proto value returns an error
	_, err := Codec.Encode("not a proto message")
	if err == nil {
		t.Error("expected error for non-proto value")
	}

	// Test that decoding to a non-proto value returns an error
	err = Codec.Decode([]byte{}, new(string))
	if err == nil {
		t.Error("expected error for non-proto value")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDoDecodesIntoAGeneratedMessage(t *testing.T) {
	data, err := protobuf.Marshal(wrapperspb.String("hello"))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		h := http.Header{"Content-Type": {httpx.ContentTypeProtobuf}}
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(bytes.NewReader(data)), Request: r}, nil
	})}
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/msg", nil)
	msg, _, err := httpx.Do[*wrapperspb.StringValue](client, req)
	if err != nil {
		t.Fatalf("Do[*T] must hand the codec a *T: %v", err)
	}
	if msg.GetValue() != "hello" {
		t.Fatalf("decoded %q", msg.GetValue())
	}
}
