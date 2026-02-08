// Package config provides type-safe configuration loading using koanf with
// support for multiple sources (YAML files, environment variables, CLI flags),
// struct tag defaults, validation, sensitive field redaction, and secret injection.
//
// The package is a thin wrapper around knadh/koanf/v2, adding DefaultsFromTags,
// PrintRedacted/ToMap, and GetConfig[T]() as a convenience orchestrator over
// koanf, pkg/secrets, and go-playground/validator.
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
//   - secret:"key"         - Key for secret provider lookup via pkg/secrets
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
// # Secret Providers
//
//	cfg, err := config.GetConfig[MyConfig](
//	    config.WithSecretProvider(provider, ctx),
//	)
package config
