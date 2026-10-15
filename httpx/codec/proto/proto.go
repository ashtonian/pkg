// Package proto provides Protocol Buffers codec support for httpx.
//
// Import this package to register the protobuf codec:
//
//	import _ "github.com/ashtonian/pkg/httpx/codec/proto"
//
// Once imported, httpx will automatically decode protobuf responses.
// To encode request bodies as protobuf, use [httpx.EncodeRequest]:
//
//	httpx.EncodeRequest(req, proto.Codec, myProtoMessage)
//	result, resp, err := httpx.Do[*pb.MyMessage](client, req)
package proto

import (
	"fmt"

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

func (codec) Decode(data []byte, v any) error {
	msg, ok := v.(proto.Message)
	if !ok {
		return fmt.Errorf("httpx/proto: value does not implement proto.Message")
	}
	return proto.Unmarshal(data, msg)
}
