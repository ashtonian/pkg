// Package config provides type-safe configuration loading via struct tags with
// support for multiple sources (YAML files, environment variables), validation,
// sensitive field handling, and secret providers.
//
// The loader resolves configuration from all sources using a clear precedence
// order: env vars > config files > programmatic defaults > struct tag defaults.
//
// # Quick Start
//
//	type AppConfig struct {
//	    Host    string        `config:"HOST,localhost" desc:"Server hostname"`
//	    Port    int           `config:"PORT,8080" validate:"min=1,max=65535"`
//	    Debug   bool          `config:"DEBUG,false"`
//	    Timeout time.Duration `config:"TIMEOUT,30s"`
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
//   - config:"NAME,default" - Config key name and optional default value
//   - required:"true"       - Field must be set (fails if zero value after loading)
//   - sensitive:"true"      - Field is redacted in Print/ToMap output
//   - validate:"min=1"      - Validation rules (go-playground/validator)
//   - desc:"description"    - Human-readable description
//   - secret:"key"          - Key for secret provider lookup
//
// # Source Precedence
//
// Values are resolved in this order (highest wins):
//
//  1. Environment variables
//  2. Config files (merged left-to-right)
//  3. Programmatic defaults (WithDefaults)
//  4. Struct tag defaults
//
// # Secret Providers
//
//	provider := config.NewMapSecretProvider(map[string]string{
//	    "db/password": "secret123",
//	})
//	cfg, err := config.GetConfig[MyConfig](
//	    config.WithSecretProvider(provider, ctx),
//	)
package config
