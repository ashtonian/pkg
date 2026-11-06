// Package msgpack provides MessagePack codec support for httpx.
//
// Import this package to register the msgpack codec:
//
//	import _ "github.com/ashtonian/pkg/httpx/codec/msgpack"
//
// Once imported, httpx's typed request helpers can decode MessagePack
// responses by Content-Type. Ordinary http.Client.Do still returns bytes.
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

// Codec encodes the JSON data model as MessagePack. JSON field selection,
// tags and marshal methods apply in both directions; bytes and text values
// have their JSON representation. Use [NativeCodec] for native MessagePack.
var Codec httpx.Codec = jsonCodec{}

// NativeCodec uses msgpack tags, binary values, timestamp extensions and
// MessagePack marshal methods. It does not honor json:"-". It is not
// registered automatically; use it only with a matching native peer.
var NativeCodec httpx.Codec = nativeCodec{}

type nativeCodec struct{}

func (nativeCodec) ContentType() string             { return httpx.ContentTypeMsgpack }
func (nativeCodec) Encode(v any) ([]byte, error)    { return msgpack.Marshal(v) }
func (nativeCodec) Decode(data []byte, v any) error { return msgpack.Unmarshal(data, v) }
