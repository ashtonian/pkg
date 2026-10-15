# httpx documentation

[Quick start](../README.md) · [Options and API](reference.md)

| Guide | Covers |
|---|---|
| [Use cases](use-cases.md) | Which HTTP behaviors to add and when standard Go is sufficient |
| [Composition](composition.md) | Wrap an existing client, combine transports, choose order and manage lifetime |
| [Caching](caching.md) | ETags, Last-Modified, freshness, identity, invalidation and stores |
| [Compression and codecs](codecs-compression.md) | Wire encoding, range semantics and optional representation codecs |
| [Resilience](resilience.md) | Safe retry/backoff, Retry-After, breakers, request budgets and hedging |
| [Requests and responses](requests.md) | Standard requests, statuses, streaming, cancellation and optional typed helpers |
| [Security and observability](security-observability.md) | Origin binding, redaction boundaries, metrics and troubleshooting |
| [Reference](reference.md) | Transport constructors, complete option/default tables and sentinel errors |
| [Development](development.md) | Test lanes, executable examples, benchmarks and releases |

Executable examples: [quick start](../examples/quickstart/main.go),
[core Go examples](../example_test.go), [Gin API and client](../examples/gin/README.md),
[MessagePack](../codec/msgpack/example_test.go),
[protobuf](../codec/proto/example_test.go),
[Brotli](../compress/brotli/example_test.go).
