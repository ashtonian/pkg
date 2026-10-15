# httpx documentation

[Quick start](../README.md) · [Options and API](reference.md)

| Guide | Covers |
|---|---|
| [Use cases](use-cases.md) | Which constructor and features fit each workload |
| [Requests and responses](requests.md) | Typed helpers, status handling, bodies, cancellation and streaming |
| [Resilience](resilience.md) | Retry, breaker, rate limit, concurrency and hedging tradeoffs |
| [Caching](caching.md) | Identity, HTTP policy, revalidation, invalidation and store lifetime |
| [Composition](composition.md) | Fixed stack, explicit chains and custom transports |
| [Security and observability](security-observability.md) | Origin binding, redaction boundaries, metrics and troubleshooting |
| [Codecs and compression](codecs-compression.md) | Negotiation, optional modules, body size and range semantics |
| [Reference](reference.md) | Complete option/default tables, API and sentinel errors |
| [Development](development.md) | Test lanes, executable examples, benchmarks and releases |

Executable examples: [quick start](../examples/quickstart/main.go),
[core Go examples](../example_test.go), [Gin API and client](../examples/gin/README.md),
[MessagePack](../codec/msgpack/example_test.go),
[protobuf](../codec/proto/example_test.go),
[Brotli](../compress/brotli/example_test.go).
