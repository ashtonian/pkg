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

	for _, v := range []any{new(string), new(*string), (**wrapperspb.StringValue)(nil), nil} {
		if err := Codec.Decode([]byte{}, v); err == nil {
			t.Errorf("decoding into %T: expected an error", v)
		}
	}
}

type stringValuePtr *wrapperspb.StringValue

// A message pointer decodes in place, allocated when nil, through every
// spelling the typed helpers and DecodeResponse pass.
func TestProtoCodecDecodesThroughAMessagePointer(t *testing.T) {
	data, err := protobuf.Marshal(wrapperspb.String("hello"))
	if err != nil {
		t.Fatal(err)
	}
	var msg *wrapperspb.StringValue
	if err := Codec.Decode(data, &msg); err != nil || msg.GetValue() != "hello" {
		t.Fatalf("**StringValue: %v, %v", msg, err)
	}
	existing := wrapperspb.String("old")
	kept := existing
	if err := Codec.Decode(data, &kept); err != nil || kept != existing || existing.GetValue() != "hello" {
		t.Fatalf("an existing message was replaced or not decoded: %v, %v", kept, err)
	}
	var defined stringValuePtr
	if err := Codec.Decode(data, &defined); err != nil || defined == nil || (*wrapperspb.StringValue)(defined).GetValue() != "hello" {
		t.Fatalf("defined pointer: %v, %v", defined, err)
	}

	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {httpx.ContentTypeProtobuf}}, Body: io.NopCloser(bytes.NewReader(data))}
	var explicit *wrapperspb.StringValue
	if err := httpx.DecodeResponse(resp, Codec, &explicit); err != nil || explicit.GetValue() != "hello" {
		t.Fatalf("DecodeResponse: %v, %v", explicit, err)
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
		t.Fatalf("Do[*T]: %v", err)
	}
	if msg.GetValue() != "hello" {
		t.Fatalf("decoded %q", msg.GetValue())
	}
	defined, _, err := httpx.Do[stringValuePtr](client, req)
	if err != nil || (*wrapperspb.StringValue)(defined).GetValue() != "hello" {
		t.Fatalf("Do[stringValuePtr]: %v, %v", defined, err)
	}
}
