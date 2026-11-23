// Package proto provides Protocol Buffers codec support for httpx.
//
// Import this package to register the protobuf codec:
//
//	import _ "github.com/ashtonian/pkg/httpx/codec/proto"
//
// Once imported, httpx's typed request helpers can decode protobuf
// responses by Content-Type. Ordinary http.Client.Do still returns bytes.
// To encode request bodies as protobuf, use [httpx.EncodeRequest]:
//
//	httpx.EncodeRequest(req, proto.Codec, myProtoMessage)
//	result, resp, err := httpx.Do[*pb.MyMessage](client, req)
package proto

import (
	"fmt"
	"reflect"

	"github.com/ashtonian/pkg/httpx"
	"google.golang.org/protobuf/proto"
)

func init() {
	httpx.RegisterCodec(Codec)
}

// Codec is the protobuf codec singleton.
// Use this with [httpx.EncodeRequest] for request body encoding.
var Codec httpx.Codec = codec{}

// codec is the internal protobuf codec implementation.
type codec struct{}

func (codec) ContentType() string { return httpx.ContentTypeProtobuf }

func (codec) Encode(v any) ([]byte, error) {
	msg, ok := v.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("httpx/proto: value does not implement proto.Message")
	}
	return proto.Marshal(msg)
}

// Decode unmarshals into v, a proto.Message or a pointer to a message
// pointer. httpx's typed helpers decode a T through a *T, so Do[*pb.Message]
// passes a **pb.Message: Decode allocates the message when the pointer is
// nil and stores it there. A defined pointer type is decoded the same way.
func (codec) Decode(data []byte, v any) error {
	if msg, ok := v.(proto.Message); ok {
		return proto.Unmarshal(data, msg)
	}
	target := reflect.ValueOf(v)
	if target.Kind() != reflect.Pointer || target.IsNil() || target.Elem().Kind() != reflect.Pointer {
		return notMessage(v)
	}
	ptr := target.Elem()
	// The method set of a defined pointer type is empty: reach the message
	// through the plain pointer type with the same element.
	msgPtr := ptr.Convert(reflect.PointerTo(ptr.Type().Elem()))
	if msgPtr.IsNil() {
		msgPtr = reflect.New(msgPtr.Type().Elem())
	}
	msg, ok := msgPtr.Interface().(proto.Message)
	if !ok {
		return notMessage(v)
	}
	if err := proto.Unmarshal(data, msg); err != nil {
		return err
	}
	ptr.Set(msgPtr.Convert(ptr.Type()))
	return nil
}

func notMessage(v any) error {
	return fmt.Errorf("httpx/proto: %T is neither a proto.Message nor a pointer to one", v)
}
