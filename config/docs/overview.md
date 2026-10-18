# Overview and design

[Documentation](README.md) · [Quick start](../README.md#quick-start)

Typed configuration for long-running Go services, declared as one tagged Go struct and built on [koanf](https://github.com/knadh/koanf). The struct is the schema: its tags give each field its key, former names, variables, default, list separator, text format, example, rules, description and secrecy, and every feature reads them. Every config, at boot and on every reload, goes through the same pipeline: merge the sources, decode, resolve secrets, normalize, validate.

```go
type DBConfig struct {
	Host     string `koanf:"host" default:"localhost" desc:"Database host"`
	MaxConns int    `koanf:"max_conns" default:"25" validate:"min=1,max=500" desc:"Connection pool size"`
	Password string `koanf:"password" validate:"required" secret:"prod/db#password" desc:"Database password"`
}

type AppConfig struct {
	DB DBConfig `koanf:"db"`
}
```

With `WithEnvPrefix("APP")`, `APP_DB_MAX_CONNS` sets the pool size; a file setting `max_conns: 0` fails validation naming `db.max_conns`; a `SecretsFrom` fetch for your store fills the password from `prod/db#password`, and no output ever shows it; and usage tables, a starter config file, a JSON Schema for editors and kongprovider's flag help all describe the three fields, with no other declaration to write.

- **The struct is the schema.** No second definition to keep in step: environment variable names, flag help, usage tables, starter files and the JSON Schema are derived from the tags, so none of them drifts from the code.
- **Any source.** Struct-tag defaults, files, environment variables, CLI flags, and any koanf provider: Kubernetes volumes, Vault, AWS, Consul, etcd, a database table.
- **Strict and validated.** A key no field takes fails the load, with the key it most likely misspells, or goes to a handler that warns; `validate` tags and a `Validate` method run on every load, and errors name the config key, never a secret's value.
- **Hot reload that cannot break production.** A reload publishes a new config only if it passed the whole pipeline; triggers are a file watch that understands Kubernetes volumes, a poll, a signal, or a provider's own watch.
- **Changes pushed, not just published.** Appliers push the changes they own into the running process — a log level, pool limits, feature gates — are retried after a failure, and the reloader reports what still waits for a restart.
- **Output that never leaks a credential.** Redacted printing, config diffs, where each value came from, usage tables, starter config files and a JSON Schema for editors.

```bash
go get github.com/ashtonian/pkg/config
```

## Shared reflection metadata

Config uses [gowalker](https://pkg.go.dev/github.com/ashtonian/pkg/gowalker)
for struct discovery, naming collisions, inline field routes and cached field
access. Config retains its own decoding, redaction, validation and source
precedence rules. Structural inspection and load-time tag validation are
separate: diagnostics can print live values without decoding their examples.
Neither metadata cache retains configuration values.
