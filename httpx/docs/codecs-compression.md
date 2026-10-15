# Compression and optional codecs

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

## Negotiate and decode compressed responses

Go's default transport already negotiates and decodes gzip when it adds the
`Accept-Encoding` header itself. Use `CompressionTransport` when you want an
explicit, composable policy: gzip and deflate support, optional Brotli,
encoding preferences, or a custom compressor. Your requests and responses
still use `net/http`:

```go
transport, err := httpx.NewCompressionTransport(http.DefaultTransport,
    httpx.WithEncodings("gzip", "deflate"),
)
if err != nil {
    return err
}
client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
defer client.CloseIdleConnections()
// Read the decoded bytes from the response returned by client.Do(req).
```

Importing [compress/brotli](../compress/brotli/README.md) registers Brotli.
Without an explicit `WithEncodings`, preference is `br`, `gzip`, `deflate`
among registered encodings. `WithCompressor` supplies an implementation for
one transport. `NewChain` uses the same options through `WithCompressionT`;
the client builders use `WithCompression`.

The layer supplies `Accept-Encoding` when it is absent and decodes supported
response `Content-Encoding` values. An explicit request header is preserved.
Unknown or stacked content codings pass through unchanged for the caller.
HTTP deflate uses zlib framing; the decoder also accepts raw DEFLATE for
interoperability. Decoding starts on the first body read, so malformed data
returns a read error. A decoded response has `Uncompressed` set, its wire
`Content-Encoding` and `Content-Length` removed, and unknown content length.

## Body and range semantics

HEAD, 1xx, 204, 205 and 304 responses are not decompressed. Range requests do
not get an automatic `Accept-Encoding`; partial representations (206 or
`Content-Range`) preserve encoded bytes and coding headers. A fragment of a
gzip stream is not independently decompressible.

Raw `client.Do` reads need application-owned bounds. Use `ReadAtMost` for a
bounded complete body, or process a stream with your own limits. Optional
typed-helper limits apply after decompression. Compression negotiation does
not automatically compress request bodies; `CompressionTransport.Compress`
is available when the upstream explicitly accepts a compressed request.

## Optional typed representation codecs

Compression is about wire encoding (`Content-Encoding`). Codecs are about
the representation (`Content-Type`). You can read bytes, stream a response,
or use `encoding/json` with any of the transports above; a codec registry is
only needed when you choose httpx's encoding/decoding helpers.

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

See [compression options](reference.md#compression), [decode options](reference.md#decoding-helpers)
and the runnable optional-module examples.
