# config

Type-safe configuration loading using [koanf](https://github.com/knadh/koanf) with support for multiple sources (YAML files, environment variables, CLI flags), struct tag defaults, validation, sensitive field redaction, and secret injection.

## Installation

```bash
go get github.com/ashtonian/pkg/config
```

## Source Precedence (highest wins)

1. Extra providers (CLI flags via `kongprovider`, custom providers)
2. Environment variables (`WithEnvPrefix`)
3. Config files (merged left-to-right)
4. Programmatic defaults (`WithDefaults`)
5. Struct tag defaults (`default:"value"`)

## Example

One struct hierarchy showing every supported feature: nested structs, slices, maps, pointers, durations, struct-tag defaults, programmatic defaults, env var overrides (with underscored koanf tags), validation, secret injection, CLI flag bridging, and sensitive-field redaction.

### Struct Definition

```go
type AppConfig struct {
    // --- Scalars with defaults ---
    Host    string        `koanf:"host" default:"0.0.0.0" desc:"Bind address"`
    Port    int           `koanf:"port" default:"8080" validate:"min=1,max=65535" flag:"port"`
    Debug   bool          `koanf:"debug" default:"false" flag:"debug"`

    // --- Slices (comma-split from env vars and default tags) ---
    Tags    []string      `koanf:"tags" default:"web,api"`
    Regions []string      `koanf:"regions"`

    // --- Maps (from YAML or programmatic defaults) ---
    Labels  map[string]string `koanf:"labels"`

    // --- Pointer (nil = not provided, distinguishes from zero value) ---
    MaxBody *int          `koanf:"max_body"`

    // --- Nested structs (3 levels deep) ---
    Server  ServerConfig  `koanf:"server"`
    DB      DBConfig      `koanf:"db"`
    Auth    AuthConfig    `koanf:"auth"`

    // --- Slice of structs (from YAML) ---
    Pools   []PoolConfig  `koanf:"pools"`
}

type ServerConfig struct {
    ReadTimeout  time.Duration `koanf:"read_timeout" default:"30s"`
    WriteTimeout time.Duration `koanf:"write_timeout" default:"60s"`
}

type DBConfig struct {
    Host     string `koanf:"host" default:"localhost"`
    Port     int    `koanf:"port" default:"5432"`
    Name     string `koanf:"name" validate:"required"`
    MaxConns int    `koanf:"max_conns" default:"25"`        // underscore in koanf tag
    Password string `koanf:"password" sensitive:"true" secret:"db/password"`
}

type AuthConfig struct {
    JWT JWTConfig `koanf:"jwt"`
}

type JWTConfig struct {
    SignKey        string        `koanf:"sign_key" sensitive:"true" validate:"required"`
    ExpiresIn      time.Duration `koanf:"expires_in" default:"24h"`
    AllowedIssuers []string      `koanf:"allowed_issuers" default:"auth.myapp.io"`
}

type PoolConfig struct {
    Name string `koanf:"name"`
    Size int    `koanf:"size"`
}
```

### YAML Config File

```yaml
# /etc/myapp/config.yaml
host: 0.0.0.0
regions:
  - us-east-1
  - eu-west-1

labels:
  environment: production
  team: platform

server:
  read_timeout: 15s
  write_timeout: 30s

db:
  host: db.prod.internal
  name: myapp_prod
  max_conns: 50
  password: hunter2

auth:
  jwt:
    sign_key: hmac-secret-256
    expires_in: 12h
    allowed_issuers:
      - auth.prod.io
      - auth.staging.io

pools:
  - name: primary
    size: 10
  - name: readonly
    size: 5
```

### Environment Variables

Env var names are built by uppercasing each koanf tag and joining with `_`. The package walks the struct at load time so **underscores inside koanf tags are never misinterpreted** as nesting separators:

```
APP_DB_MAX_CONNS  →  "db.max_conns"  (correct — not "db.max.conns")
```

| Struct Path | koanf Tags | Env Var |
|-------------|-----------|---------|
| `AppConfig.Host` | `host` | `APP_HOST` |
| `AppConfig.Tags` | `tags` | `APP_TAGS` (comma-separated) |
| `AppConfig.DB.MaxConns` | `db` → `max_conns` | `APP_DB_MAX_CONNS` |
| `AppConfig.DB.Password` | `db` → `password` | `APP_DB_PASSWORD` |
| `AppConfig.Auth.JWT.SignKey` | `auth` → `jwt` → `sign_key` | `APP_AUTH_JWT_SIGN_KEY` |
| `AppConfig.Auth.JWT.AllowedIssuers` | `auth` → `jwt` → `allowed_issuers` | `APP_AUTH_JWT_ALLOWED_ISSUERS` (comma-separated) |
| `AppConfig.Server.ReadTimeout` | `server` → `read_timeout` | `APP_SERVER_READ_TIMEOUT` |

```bash
export APP_PORT="9090"
export APP_TAGS="web,api,grpc"                         # → []string{"web","api","grpc"}
export APP_DB_MAX_CONNS="100"                          # underscore preserved
export APP_DB_PASSWORD="s3cret"
export APP_AUTH_JWT_SIGN_KEY="prod-key"
export APP_AUTH_JWT_ALLOWED_ISSUERS="a.io,b.io"        # → []string{"a.io","b.io"}
export APP_SERVER_READ_TIMEOUT="10s"                   # → time.Duration
```

### Loading

```go
import (
    "github.com/ashtonian/pkg/config"
    "github.com/ashtonian/pkg/config/kongprovider"
)

cfg, err := config.GetConfig[AppConfig](
    // Env var prefix — APP_HOST, APP_DB_MAX_CONNS, etc.
    config.WithEnvPrefix("APP"),

    // YAML files merged left-to-right (second overrides first)
    config.WithConfigFile("/etc/myapp/config.yaml"),
    config.WithConfigFile("./config.local.yaml"),

    // Programmatic defaults (override struct-tag defaults, overridden by files/env)
    config.WithDefaults(map[string]any{
        "debug":   true,
        "db.name": "devdb",
        "labels":  map[string]any{"env": "dev"},
    }),

    // Secret injection — fields tagged secret:"key" fetched from provider
    config.WithSecretProvider(vaultProvider, ctx),

    // CLI flag overrides (highest precedence) — reads flag:"name" tags
    kongprovider.WithOverridesFromTags[AppConfig](kongCtx),
)
if err != nil {
    log.Fatal(err)
}

// Pointer nil-check: *int distinguishes "unset" from 0
if cfg.MaxBody != nil {
    fmt.Printf("max body: %d bytes\n", *cfg.MaxBody)
} else {
    fmt.Println("max body: unlimited")
}
```

### PrintRedacted

```go
config.PrintRedacted(cfg)
```

```
FIELD                        VALUE                TYPE
---------------------------------------------------------------
host                         0.0.0.0              string
port                         9090                 int
debug                        true                 bool
tags                         [web api grpc]       []string
regions                      [us-east-1 eu-west-1][]string
labels                       map[env:dev]         map[string]string
max_body                     <nil>                *int
server.read_timeout          10s                  time.Duration
server.write_timeout         30s                  time.Duration
db.host                      db.prod.internal     string
db.port                      5432                 int
db.name                      myapp_prod           string
db.max_conns                 100                  int
db.password                  [REDACTED]           string
auth.jwt.sign_key            [REDACTED]           string
auth.jwt.expires_in          12h0m0s              time.Duration
auth.jwt.allowed_issuers     [a.io b.io]          []string
```

Fields are auto-redacted when tagged `sensitive:"true"` or when the field name contains password, secret, token, key, credential, auth, private, or apikey.

Other output formats:

```go
config.PrintRedacted(cfg, config.WithFormat("json"))            // JSON object
config.PrintRedacted(cfg, config.WithFormat("env"))             // export KEY=VALUE lines
config.PrintRedacted(cfg, config.WithShowSensitive(true))       // reveal sensitive values
config.PrintRedacted(cfg, config.WithWriter(&buf))              // write to buffer

m := config.ToMap(cfg, true)  // map[string]any with redacted sensitive fields
```

## API Reference

### Functions

| Function | Description |
|----------|-------------|
| `GetConfig[T](opts...)` | Load config from all sources, unmarshal into T, inject secrets, validate |
| `DefaultsFromTags[T]()` | Extract `default:"value"` struct tags into a koanf-compatible map |
| `PrintRedacted(cfg, opts...)` | Print config with sensitive fields redacted |
| `ToMap(cfg, redact)` | Export config as `map[string]any` with optional redaction |

### Options

| Option | Description |
|--------|-------------|
| `WithEnvPrefix(prefix)` | Set prefix for env var lookups (e.g., `"APP"` maps `APP_HOST` → `host`) |
| `WithConfigFile(path)` | Add an optional YAML config file |
| `WithMustConfigFile(path)` | Add a required YAML config file (error if missing) |
| `WithConfigFiles(paths...)` | Add multiple optional config files, merged left-to-right |
| `WithMustConfigFiles(paths...)` | Add multiple required config files |
| `WithDefaults(map)` | Set programmatic default values (koanf dot-path keys) |
| `WithProvider(p)` | Add a generic koanf provider at high precedence |
| `WithSecretProvider(sp, ctx)` | Configure secret injection via `pkg/secrets` |
| `WithSkipValidation()` | Disable go-playground/validator validation |
| `WithValidator(v)` | Use a custom validator instance |
| `WithKoanf(k)` | Provide a pre-configured `*koanf.Koanf` instance |

### Struct Tags

| Tag | Description | Example |
|-----|-------------|---------|
| `koanf` | Key name for koanf (nesting via `.` delimiter) | `koanf:"host"` |
| `default` | Default value (lowest precedence, comma-split for slices) | `default:"localhost"` |
| `validate` | Validation rules ([go-playground/validator](https://pkg.go.dev/github.com/go-playground/validator/v10)) | `validate:"required,min=1"` |
| `flag` | Kong CLI flag name (used by `kongprovider`) | `flag:"port"` |
| `sensitive` | Redacted in PrintRedacted/ToMap output | `sensitive:"true"` |
| `desc` | Human-readable description | `desc:"Server hostname"` |
| `secret` | Key for secret provider lookup via `pkg/secrets` | `secret:"db/password"` |

### Error Codes

| Code | Sentinel | Description |
|------|----------|-------------|
| E7002 | `ErrValidation` | Validation rule failed (includes `validate:"required"` checks) |
| E7003 | `ErrFileRead` | Required config file could not be read |
| E7005 | `ErrSecretProvider` | Secret provider returned an error |

