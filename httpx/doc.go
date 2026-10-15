// Package httpx implements composable HTTP behavior as [http.RoundTripper]
// decorators: conditional caching, compression negotiation, and safe retry
// with backoff. Use them with an ordinary [http.Client], standard requests
// and responses, and an existing transport's TLS, proxy and pool settings.
//
// # Transports first
//
// [NewRetryTransport], [NewCompressionTransport] and [NewCacheTransport]
// each wrap one http.RoundTripper. [NewChain] combines layers into another
// RoundTripper; the first layer listed is innermost. Assign the result to
// http.Client.Transport and use http.NewRequestWithContext and client.Do.
// Request contexts, cookies, redirects, status codes and streaming bodies
// keep their standard net/http interfaces. Reuse clients and transports.
//
// Put compression inside retry and cache outside retry. This lets fresh
// cache hits skip the network, while revalidation uses the retry policy and
// the cache stores decoded responses. If used, auth runs before cache lookup;
// rate limits, concurrency limits and breakers normally run per attempt.
//
// # HTTP caching
//
// [CacheTransport] is an opt-in private HTTP cache. It follows freshness,
// Cache-Control, Vary and identity boundaries, revalidates with ETag or
// Last-Modified, and merges a 304 response with the stored representation.
// [MemoryCache] bounds retained entries and bytes; [CacheStore] permits a
// custom store. Stale reuse is opt-in through origin directives or options.
//
// Identity includes Authorization, Cookie, effective authority and configured
// identity headers. Configure all additional identity headers, including
// API-key headers; use separate stores for identities hidden in a transport,
// such as mTLS.
// Caller-conditional, ranged, HEAD and body-bearing GET requests bypass cache.
//
// # Compression and retry
//
// [CompressionTransport] negotiates gzip and deflate, with optional Brotli,
// and decodes supported complete response bodies lazily. Read errors remain
// body-read errors; partial representations keep their encoded bytes. Go's
// default transport already handles basic gzip; this layer supplies
// configurable accepted encodings and lazy decoding as a reusable decorator.
//
// [RetryTransport] defaults to three total attempts with exponential backoff,
// jitter and Retry-After handling, subject to the request's deadline. Retry
// requires an idempotent method, an Idempotency-Key, or explicit
// [WithRetryNonIdempotent] permission, plus a replayable body. The server must
// implement deduplication for an idempotency key to make writes safe.
// Unknown streams are recorded as sent, within a bound, rather than buffered
// before the transport can honor cancellation.
//
// # Optional policies and helpers
//
// Circuit breakers, rate and concurrency limits, hedging, origin-bound auth,
// redacted logging and metrics are independent transports too. Their order
// determines whether they observe logical calls or individual attempts.
// [TransportStack] inspects layers; built-in layers expose Unwrap.
//
// [NewClient] and [NewStandard] are optional builders returning *http.Client.
// Both default to a tuned base transport and 30-second timeout; NewStandard
// also enables compression and retry. Other policies must be enabled.
// [Get], [Post], [Put], [Patch] and [Do] optionally decode bounded 2xx values
// by Content-Type. JSON and XML are built in; MessagePack and protobuf use
// separate modules. [WithMaxResponseBytes] defaults to 10 MiB after
// decompression. [ReadAtMost] bounds complete reads without typed decoding.
//
// # Status, bodies and shutdown
//
// A nil error is not an HTTP success check. Inspect the response status and
// close each response body, including one returned with a typed decode error.
// Raw client.Do bodies remain streaming and need application-owned bounds.
// EOF, read error or Close releases limiter slots and hedge winner contexts.
// Call [CacheTransport.Close] to stop background refreshes before closing
// idle transport connections with http.Client.CloseIdleConnections.
//
// # Optional modules
//
// Blank-import codec/msgpack, codec/proto or compress/brotli to register
// MessagePack, protobuf or Brotli. Named imports expose implementations.
// Each optional dependency is a separate Go module; the core depends only on
// golang.org/x/time and github.com/sony/gobreaker/v2.
package httpx
