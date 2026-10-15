// Package msgpack provides MessagePack codec support for httpx.
//
// Import this package to register the msgpack codec:
//
//	import _ "github.com/ashtonian/pkg/httpx/codec/msgpack"
//
// Once imported, httpx will automatically decode msgpack responses.
// To encode request bodies as msgpack, use [httpx.EncodeRequest]:
//
//	httpx.EncodeRequest(req, msgpack.Codec, myValue)
//	result, resp, err := httpx.Do[MyType](client, req)
package msgpack

import (
	"github.com/ashtonian/pkg/httpx"
	"github.com/vmihailenco/msgpack/v5"
)

func init() {
	httpx.RegisterCodec(Codec)
}

// Codec is the msgpack codec singleton.
// Use this with [httpx.EncodeRequest] for request body encoding.
var Codec httpx.Codec = codec{}

// codec is the internal msgpack codec implementation.
type codec struct{}

func (codec) ContentType() string             { return httpx.ContentTypeMsgpack }
func (codec) Encode(v any) ([]byte, error)    { return msgpack.Marshal(v) }
func (codec) Decode(data []byte, v any) error { return msgpack.Unmarshal(data, v) }
