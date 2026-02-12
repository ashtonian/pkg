# config

Type-safe configuration loading using [koanf](https://github.com/knadh/koanf) with support for multiple sources (YAML files, environment variables, CLI flags), struct tag defaults, validation, sensitive field redaction, and secret injection.

## Source Precedence (highest wins)

1. Extra providers (CLI flags via `kongprovider`, custom providers)
2. Environment variables (`WithEnvPrefix`)
3. Config files (merged left-to-right)
4. Programmatic defaults (`WithDefaults`)
5. Struct tag defaults (`default:"value"`)

## Example

```go
// Struct with tags for defaults, validation, secrets, and redaction.
type AppConfig struct {
    Host   string        `koanf:"host" default:"0.0.0.0"`
    Port   int           `koanf:"port" default:"8080" validate:"min=1,max=65535" flag:"port"`
    Debug  bool          `koanf:"debug" default:"false"`
    Tags   []string      `koanf:"tags" default:"web,api"`
    Server ServerConfig  `koanf:"server"`
    DB     DBConfig      `koanf:"db"`
}

type ServerConfig struct {
    ReadTimeout  time.Duration `koanf:"read_timeout" default:"30s"`
    WriteTimeout time.Duration `koanf:"write_timeout" default:"60s"`
}

type DBConfig struct {
    Host     string `koanf:"host" default:"localhost"`
    Port     int    `koanf:"port" default:"5432"`
    Name     string `koanf:"name" validate:"required"`
    MaxConns int    `koanf:"max_conns" default:"25"`
    Password string `koanf:"password" sensitive:"true" secret:"db/password"`
}

// Load from files, env vars, secrets, and CLI flags.
cfg, err := config.GetConfig[AppConfig](
    config.WithEnvPrefix("APP"),                           // APP_HOST, APP_DB_MAX_CONNS, etc.
    config.WithConfigFile("/etc/myapp/config.yaml"),       // YAML files merged left-to-right
    config.WithConfigFile("./config.local.yaml"),
    config.WithDefaults(map[string]any{"db.name": "dev"}), // Programmatic defaults
    config.WithSecretProvider(vaultProvider, ctx),          // secret:"key" injection
    kongprovider.WithOverridesFromTags[AppConfig](kongCtx), // CLI flag overrides (highest)
)

// Print config with sensitive fields auto-redacted.
config.PrintRedacted(cfg)
// db.password  [REDACTED]  string
// Other formats: "json", "env"; options: WithShowSensitive, WithWriter
```

Env var names are built by uppercasing each koanf tag and joining with `_`. Underscores inside koanf tags are preserved: `APP_DB_MAX_CONNS` maps to `db.max_conns`, not `db.max.conns`.

## Struct Tags

| Tag | Description | Example |
|-----|-------------|---------|
| `koanf` | Key name for koanf (nesting via `.` delimiter) | `koanf:"host"` |
| `default` | Default value (lowest precedence, comma-split for slices) | `default:"localhost"` |
| `validate` | Validation rules ([go-playground/validator](https://pkg.go.dev/github.com/go-playground/validator/v10)) | `validate:"required,min=1"` |
| `flag` | Kong CLI flag name (used by `kongprovider`) | `flag:"port"` |
| `sensitive` | Redacted in PrintRedacted/ToMap output | `sensitive:"true"` |
| `desc` | Human-readable description | `desc:"Server hostname"` |
| `secret` | Key for secret provider lookup via `pkg/secrets` | `secret:"db/password"` |

## API Reference

| Function | Description |
|----------|-------------|
| `GetConfig[T](opts...)` | Load config from all sources, unmarshal into T, inject secrets, validate |
| `DefaultsFromTags[T]()` | Extract `default:"value"` struct tags into a koanf-compatible map |
| `PrintRedacted(cfg, opts...)` | Print config with sensitive fields redacted |
| `ToMap(cfg, redact)` | Export config as `map[string]any` with optional redaction |

| Option | Description |
|--------|-------------|
| `WithEnvPrefix(prefix)` | Set prefix for env var lookups |
| `WithConfigFile(path)` | Add an optional YAML config file |
| `WithMustConfigFile(path)` | Add a required YAML config file (error if missing) |
| `WithConfigFiles(paths...)` | Add multiple optional config files, merged left-to-right |
| `WithDefaults(map)` | Set programmatic default values (koanf dot-path keys) |
| `WithProvider(p)` | Add a generic koanf provider at high precedence |
| `WithSecretProvider(sp, ctx)` | Configure secret injection via `pkg/secrets` |
| `WithSkipValidation()` | Disable go-playground/validator validation |
| `WithValidator(v)` | Use a custom validator instance |

## Error Codes

| Code | Sentinel | Description |
|------|----------|-------------|
| E7002 | `ErrValidation` | Validation rule failed |
| E7003 | `ErrFileRead` | Required config file could not be read |
| E7005 | `ErrSecretProvider` | Secret provider returned an error |

