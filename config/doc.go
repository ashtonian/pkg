// Package config provides type-safe configuration loading using koanf with
// support for multiple sources (YAML files, environment variables, CLI flags),
// struct tag defaults, validation, sensitive field redaction, and secret injection.
//
// The package is a thin wrapper around knadh/koanf/v2, adding DefaultsFromTags,
// PrintRedacted/ToMap, and GetConfig[T]() as a convenience orchestrator over
// koanf, a consumer-supplied [SecretPopulator], and go-playground/validator.
// Secret backends are the caller's, wired in via WithSecretPopulator; the
// package depends on none.
//
// # Quick Start
//
//	type AppConfig struct {
//	    Host    string        `koanf:"host" default:"localhost" desc:"Server hostname"`
//	    Port    int           `koanf:"port" default:"8080" validate:"min=1,max=65535"`
//	    Debug   bool          `koanf:"debug" default:"false"`
//	    Timeout time.Duration `koanf:"timeout" default:"30s"`
//	}
//
//	cfg, err := config.GetConfig[AppConfig](
//	    config.WithEnvPrefix("APP"),
//	    config.WithConfigFile("./config.yaml"),
//	)
//
// # Struct Tags
//
// The following struct tags control configuration behavior:
//
//   - koanf:"name"         - koanf key name. Nesting uses "." delimiter.
//   - koanf:"-"            - Ignore this field
//   - default:"value"      - Default value (read by DefaultsFromTags, lowest precedence)
//   - validate:"required"  - Field must be set (go-playground/validator)
//   - sensitive:"true"     - Redacted in PrintRedacted/ToMap output
//   - desc:"description"   - Human-readable description
//   - secret:"key"         - Key resolved by the SecretPopulator passed to WithSecretPopulator
//   - flag:"flagname"      - Maps to Kong CLI flag (used by kongprovider sub-package)
//
// # Source Precedence
//
// Values are resolved in this order (highest wins):
//
//  1. Extra providers (CLI flags via kongprovider, custom providers)
//  2. Environment variables
//  3. Config files (merged left-to-right)
//  4. Programmatic defaults (WithDefaults)
//  5. Struct tag defaults (default:"value")
//
// # CLI Flag Overrides
//
//	import "github.com/ashtonian/pkg/config/kongprovider"
//
//	cfg, err := config.GetConfig[AppConfig](
//	    config.WithEnvPrefix("APP"),
//	    kongprovider.WithOverridesFromTags[AppConfig](kongCtx),
//	)
//
// # Secret Injection
//
// The package does not import any secret backend. The consumer wires
// in a [SecretPopulator] closure that captures the chosen backend (e.g.
// Vault, AWS Secrets Manager):
//
//	import "github.com/ashtonian/pkg/secrets"
//
//	cfg, err := config.GetConfig[MyConfig](ctx,
//	    config.WithSecretPopulator(func(ctx context.Context, target any) error {
//	        return secrets.Populate(ctx, provider, target)
//	    }),
//	)
//
// # Hot-Reload
//
// Reloader[T] watches config sources and atomically swaps the config
// struct when changes are detected. Readers access the current config via
// Config() with zero contention (atomic pointer load). If a reload fails
// (parse error, validation failure), the previous config is preserved.
//
//	reloader, err := config.NewReloader[AppConfig](
//	    []config.ReloadOption[AppConfig]{
//	        config.WithFileWatch[AppConfig](500 * time.Millisecond),
//	        config.WithOnChange(func(old, new *AppConfig) {
//	            slog.Info("config reloaded", "old_port", old.Port, "new_port", new.Port)
//	        }),
//	    },
//	    config.WithEnvPrefix("APP"),
//	    config.WithConfigFile("./config.yaml"),
//	)
//
//	// Read config on the hot path (atomic, zero contention).
//	cfg := reloader.Config()
//
//	// Start watching in a goroutine.
//	go reloader.Start(ctx)
//
//	// Expose reload endpoint for operators.
//	mux.Handle("POST /admin/config/reload", reloader.ReloadHandler())
package config
