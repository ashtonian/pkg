# Config documentation

[Overview and quick start](../README.md) · [Go API](https://pkg.go.dev/github.com/ashtonian/pkg/config) · [Repository modules](../../README.md)

## Using config

- [Overview and design](overview.md): the struct as a schema, the pipeline and package guarantees.
- [Schema and validation](schema.md): tags, key grammar, decoding, unknown keys, aliases, post-load hooks and validation.
- [Sources and precedence](sources.md): source layers, file parsers, koanf providers and environment variables.
- [Secrets](secrets.md): secret references, optional values, rotations and failures.
- [Hot reload](reloading.md): lifecycle, triggers, appliers, Kubernetes volumes and admin endpoints.
- [Inspection and output](inspection.md): redaction, usage, diffs, provenance, templates and JSON Schema.
- [Reference and operations](reference.md): options, errors, observability and testing.

## Integrations and complete programs

- [Integrations and recipes](integrations.md): cookbook index, CLI flags, decoding a map and an existing koanf loader.
- [Kong provider](../kongprovider/README.md): flags as the highest-precedence source and help from the config struct.
- [Runnable cookbook](../examples/cookbook/README.md): Kubernetes, databases, AWS Secrets Manager, Vault and a complete service, each with tests.
- [Contributing](../../CONTRIBUTING.md): workspace checks, released modules, cookbook validation and releases.
