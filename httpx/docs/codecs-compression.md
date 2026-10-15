# Codecs and compression

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

## Codecs describe the representation

JSON (`application/json`) and XML (`application/xml`) are built in. Typed
helpers set `Accept` from the registered codecs when it is absent and choose
the response decoder by `Content-Type`, ignoring its parameters. Set an
explicit `Accept` when the API must return one format.

| Format | Module | When to use |
|---|---|---|
| JSON | Core | Ordinary REST APIs and Go structs |
| XML | Core | XML APIs that match `encoding/xml`'s model |
| MessagePack | [codec/msgpack](../codec/msgpack/README.md) | A server that supports MessagePack's compact typed representation |
| Protobuf | [codec/proto](../codec/proto/README.md) | Generated protobuf messages and an agreed schema; decode with `Do[*pb.Message]` |

Blank-import an optional module to register it. Use a named import when
calling its codec with `EncodeRequest`. Registration replaces the same
content type globally and is concurrency-safe; register at startup so
content negotiation stays predictable. `RegisterCodec(nil)` panics.

The API does not guess a vendor-specific `application/*+json` media type;
register a codec for the exact type or explicitly call `DecodeResponse`
with the appropriate codec. `DecodeResponse` decodes the body you supply;
the caller decides whether that response/status has a representation.

## Compression describes the wire encoding

`WithCompression` negotiates gzip and deflate; importing
[compress/brotli](../compress/brotli/README.md) adds Brotli. The default
preference is `br`, `gzip`, `deflate` among registered encodings. Use
`WithEncodings` to restrict it or `WithCompressor` for a custom implementation.

HTTP deflate uses zlib framing. The decoder also accepts raw DEFLATE for
interoperability. Decoding starts on the first body read, so malformed
compressed data reports a read error rather than delaying receipt of the
response headers. The response is marked `Uncompressed` when decoded.

HEAD, 1xx, 204, 205 and 304 responses are not decompressed. Range requests do
not get an automatic `Accept-Encoding`; partial representations (206 or
`Content-Range`) preserve encoded bytes and coding headers. A fragment of a
gzip stream is not independently decompressible.

Typed helper limits apply after decompression. Raw streaming reads need
application-owned limits. Compression negotiation does not automatically
compress request bodies; `CompressionTransport.Compress` is available when
the upstream explicitly accepts a compressed request representation.

See [compression options](reference.md#compression), [decode options](reference.md#decoding-helpers)
and the runnable optional-module examples.
