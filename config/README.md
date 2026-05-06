# config

Type-safe configuration loading using [koanf](https://github.com/knadh/koanf) with support for multiple sources (YAML files, environment variables, CLI flags), struct-tag defaults, validation, sensitive field redaction, and secret injection.

## Quick Start

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
cfg, err := config.GetConfig[AppConfig](ctx,
    config.WithEnvPrefix("APP"),                           // APP_HOST, APP_DB_MAX_CONNS, etc.
    config.WithConfigFile("/etc/myapp/config.yaml"),       // YAML files merged left-to-right
    config.WithConfigFile("./config.local.yaml"),
    config.WithDefaults(map[string]any{"db.name": "dev"}), // Programmatic defaults
    config.WithSecretPopulator(func(ctx context.Context, target any) error {
        return secrets.Populate(ctx, vaultProvider, target) // your secret backend
    }),
    kongprovider.WithOverridesFromTags[AppConfig](kongCtx), // CLI flag overrides (highest)
)

// Print config with sensitive fields auto-redacted.
config.PrintRedacted(cfg)
// db.password  [REDACTED]  string
// Other formats: "json", "env"; options: WithShowSensitive, WithWriter
```

Env var names are built by uppercasing each koanf tag and joining with `_`. Underscores inside koanf tags are preserved: `APP_DB_MAX_CONNS` maps to `db.max_conns`, not `db.max.conns`.

## Configuration

### Source precedence (highest wins)

1. Extra providers (CLI flags via `kongprovider`, custom providers)
2. Environment variables (`WithEnvPrefix`)
3. Config files (merged left-to-right)
4. Programmatic defaults (`WithDefaults`)
5. Struct tag defaults (`default:"value"`)

### Struct Tags

| Tag | Description | Example |
|-----|-------------|---------|
| `koanf` | Key name for koanf (nesting via `.` delimiter) | `koanf:"host"` |
| `default` | Default value (lowest precedence, comma-split for slices) | `default:"localhost"` |
| `validate` | Validation rules ([go-playground/validator](https://pkg.go.dev/github.com/go-playground/validator/v10)) | `validate:"required,min=1"` |
| `flag` | Kong CLI flag name (used by `kongprovider`) | `flag:"port"` |
| `sensitive` | Redacted in PrintRedacted/ToMap output | `sensitive:"true"` |
| `desc` | Human-readable description | `desc:"Server hostname"` |
| `secret` | Key resolved by the [SecretPopulator] passed to `WithSecretPopulator` | `secret:"db/password"` |

### Hot-Reload

`Reloader[T]` watches config sources and atomically swaps the config when changes are detected. Reads via `Config()` are lock-free (atomic pointer). Failed reloads preserve the previous config.

```go
reloader, err := config.NewReloader[AppConfig](
    []config.ReloadOption[AppConfig]{
        config.WithFileWatch[AppConfig](500 * time.Millisecond), // fsnotify with debounce
        config.WithPollInterval[AppConfig](30 * time.Second),    // fallback polling
        config.WithSignalReload[AppConfig](syscall.SIGHUP),      // SIGHUP reload
        config.WithOnChange(func(old, new *AppConfig) {
            slog.Info("config changed", "old_port", old.Port, "new_port", new.Port)
            rateLimiter.UpdateLimit(new.RateLimit)
        }),
    },
    config.WithEnvPrefix("APP"),
    config.WithConfigFile("./config.yaml"),
)

// Read config on hot path (zero contention).
cfg := reloader.Config()

// Start watching in background.
go reloader.Start(ctx)

// Expose reload endpoint for operators (POST /admin/config/reload).
mux.Handle("POST /admin/config/reload", reloader.ReloadHandler())
```

## API Reference

| Function | Description |
|----------|-------------|
| `GetConfig[T](opts...)` | Load config from all sources, unmarshal into T, inject secrets, validate |
| `NewReloader[T](reloadOpts, configOpts...)` | Create a reloader with initial load and optional watch |
| `DefaultsFromTags[T]()` | Extract `default:"value"` struct tags into a koanf-compatible map |
| `PrintRedacted(cfg, opts...)` | Print config with sensitive fields redacted |
| `ToMap(cfg, redact)` | Export config as `map[string]any` with optional redaction |

| Config Option | Description |
|--------|-------------|
| `WithEnvPrefix(prefix)` | Set prefix for env var lookups |
| `WithConfigFile(path)` | Add an optional YAML config file |
| `WithMustConfigFile(path)` | Add a required YAML config file (error if missing) |
| `WithConfigFiles(paths...)` | Add multiple optional config files, merged left-to-right |
| `WithDefaults(map)` | Set programmatic default values (koanf dot-path keys) |
| `WithProvider(p)` | Add a generic koanf provider at high precedence |
| `WithSecretPopulator(fn)` | Configure secret injection via a consumer-supplied `SecretPopulator` closure (typically wraps a secret store client) |
| `WithSkipValidation()` | Disable go-playground/validator validation |
| `WithValidator(v)` | Use a custom validator instance |

| Reload Option | Description |
|--------|-------------|
| `WithFileWatch[T](debounce)` | Watch config files via fsnotify (0 = 500ms default) |
| `WithPollInterval[T](interval)` | Periodic reload at interval (fallback for unreliable fsnotify) |
| `WithSignalReload[T](signals...)` | Reload on OS signals (e.g., `syscall.SIGHUP`) |
| `WithOnChange[T](func(old, new *T))` | Callback after successful reload |
| `WithReloadLogger[T](logger)` | Custom logger for reload events |

| Reloader Method | Description |
|--------|-------------|
| `Config() *T` | Current config (atomic load, zero contention) |
| `Reload(ctx) (*T, error)` | Trigger immediate reload (validate-before-swap) |
| `Start(ctx) error` | Block and watch for changes until Stop/cancel |
| `Stop()` | Signal the watcher to stop |
| `ReloadHandler() http.Handler` | HTTP handler: POST returns redacted config JSON |

## Error Codes

| Code | Sentinel | Description |
|------|----------|-------------|
| E7002 | `ErrValidation` | Validation rule failed |
| E7003 | `ErrFileRead` | Required config file could not be read |
| E7005 | `ErrSecretProvider` | Secret provider returned an error |
| E7006 | `ErrReload` | Config reload failed (wraps underlying error) |

## References

- [knadh/koanf](https://github.com/knadh/koanf) — wrapped library
