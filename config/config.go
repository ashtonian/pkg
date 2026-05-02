package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	kenv "github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	"github.com/ashtonian/pkg/secrets"
)

// Option configures GetConfig behavior.
type Option func(*options)

type options struct {
	envPrefix       string
	configFiles     []configFileEntry
	defaults        map[string]any
	secretProvider  secrets.SecretProvider
	skipValidation  bool
	customValidator *validator.Validate
	koanfInstance   *koanf.Koanf
	extraProviders  []koanf.Provider
}

type configFileEntry struct {
	path     string
	required bool
}

func buildOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// WithEnvPrefix sets the prefix for environment variable lookups.
// For example, WithEnvPrefix("APP") causes env var APP_HOST to map to koanf key "host".
func WithEnvPrefix(prefix string) Option {
	return func(o *options) {
		o.envPrefix = prefix
	}
}

// WithConfigFile adds an optional YAML config file path. No error if missing.
func WithConfigFile(path string) Option {
	return func(o *options) {
		o.configFiles = append(o.configFiles, configFileEntry{path: path, required: false})
	}
}

// WithMustConfigFile adds a required config file path. Returns ErrFileRead if missing.
func WithMustConfigFile(path string) Option {
	return func(o *options) {
		o.configFiles = append(o.configFiles, configFileEntry{path: path, required: true})
	}
}

// WithConfigFiles adds multiple optional config file paths, merged left-to-right.
func WithConfigFiles(paths ...string) Option {
	return func(o *options) {
		for _, p := range paths {
			o.configFiles = append(o.configFiles, configFileEntry{path: p, required: false})
		}
	}
}

// WithMustConfigFiles adds multiple required config file paths, merged left-to-right.
func WithMustConfigFiles(paths ...string) Option {
	return func(o *options) {
		for _, p := range paths {
			o.configFiles = append(o.configFiles, configFileEntry{path: p, required: true})
		}
	}
}

// WithDefaults sets programmatic default values as a map[string]any.
// Keys use koanf dot-path notation (e.g., "server.port").
func WithDefaults(defaults map[string]any) Option {
	return func(o *options) {
		o.defaults = defaults
	}
}

// WithProvider adds a generic koanf provider loaded at high precedence
// (after env vars). Multiple providers are loaded in the order added.
// This is the extension point for CLI flag overrides (via kongprovider),
// remote config sources, or any custom koanf.Provider.
func WithProvider(p koanf.Provider) Option {
	return func(o *options) {
		o.extraProviders = append(o.extraProviders, p)
	}
}

// WithSecretProvider configures secret injection.
// After unmarshaling, fields tagged with secret:"key" are populated by
// calling secrets.Populate() with the ctx supplied to GetConfig.
func WithSecretProvider(sp secrets.SecretProvider) Option {
	return func(o *options) {
		o.secretProvider = sp
	}
}

// WithSkipValidation disables go-playground/validator validation
// (including validate:"required" checks).
func WithSkipValidation() Option {
	return func(o *options) {
		o.skipValidation = true
	}
}

// WithValidator sets a custom *validator.Validate instance.
func WithValidator(v *validator.Validate) Option {
	return func(o *options) {
		o.customValidator = v
	}
}

// WithKoanf provides a pre-configured *koanf.Koanf instance.
// Advanced escape hatch for adding custom providers (Consul, Vault, etc.)
// before GetConfig runs. When provided, defaults and file loading steps
// are applied on top of this koanf instance.
func WithKoanf(k *koanf.Koanf) Option {
	return func(o *options) {
		o.koanfInstance = k
	}
}

// GetConfig creates a koanf instance, loads sources in precedence order,
// unmarshals into T, injects secrets, and validates.
//
// ctx is propagated into secret resolution; pass
// the caller's request/boot context so secret-fetch latency and
// cancellation flow through cleanly.
//
// Source precedence (highest wins):
//  1. Extra providers (CLI flags via kongprovider, custom providers)
//  2. Environment variables
//  3. Config files (merged left-to-right)
//  4. Programmatic defaults (WithDefaults)
//  5. Struct tag defaults (default:"value" via DefaultsFromTags)
func GetConfig[T any](ctx context.Context, opts ...Option) (*T, error) {
	o := buildOptions(opts)

	// Use caller-provided koanf or create a new one.
	k := o.koanfInstance
	if k == nil {
		k = koanf.New(".")
	}

	// Track loaded sources for structured logging.
	var loadedSources []string

	// Step 1: struct tag defaults (lowest precedence).
	tagDefaults := DefaultsFromTags[T]()
	if len(tagDefaults) > 0 {
		if err := k.Load(confmap.Provider(tagDefaults, "."), nil); err != nil {
			return nil, fmt.Errorf("loading struct tag defaults: %w", err)
		}
		loadedSources = append(loadedSources, "struct-tag-defaults")
	}

	// Step 2: programmatic defaults (override struct tag defaults).
	if len(o.defaults) > 0 {
		if err := k.Load(confmap.Provider(o.defaults, "."), nil); err != nil {
			return nil, fmt.Errorf("loading defaults: %w", err)
		}
		loadedSources = append(loadedSources, "programmatic-defaults")
	}

	// Step 3: config files (merged left-to-right).
	for _, cf := range o.configFiles {
		if err := k.Load(file.Provider(cf.path), yaml.Parser()); err != nil {
			if cf.required {
				return nil, fmt.Errorf("loading config file %s: %w", cf.path, ErrFileRead)
			}
			// Optional file missing or unreadable: continue silently.
		} else {
			loadedSources = append(loadedSources, "file:"+cf.path)
		}
	}

	// Step 4: env vars with struct-aware mapping. The naive "_" → "." transform
	// breaks koanf tags containing underscores (e.g., koanf:"max_conns"
	// nested under koanf:"db" — APP_DB_MAX_CONNS would become "db.max.conns"
	// instead of "db.max_conns"). Walking T builds an exact mapping.
	if o.envPrefix != "" {
		prefix := strings.TrimSuffix(o.envPrefix, "_")
		fullPrefix := prefix + "_"
		var zero T
		envMapping := envMappingFromType(reflect.TypeOf(zero), prefix)

		if err := k.Load(kenv.Provider(fullPrefix, ".", func(s string) string {
			if koanfPath, ok := envMapping[s]; ok {
				return koanfPath
			}
			// Fallback for dynamic keys (map fields, etc.).
			return strings.ReplaceAll(
				strings.ToLower(strings.TrimPrefix(s, fullPrefix)), "_", ".",
			)
		}), nil); err != nil {
			return nil, fmt.Errorf("loading env vars: %w", err)
		}
		loadedSources = append(loadedSources, "env:"+fullPrefix+"*")
	}

	// Step 5: extra providers (including kongprovider), loaded in order added (highest precedence).
	for i, p := range o.extraProviders {
		if err := k.Load(p, nil); err != nil {
			return nil, fmt.Errorf("loading extra provider: %w", err)
		}
		loadedSources = append(loadedSources, fmt.Sprintf("provider-%d", i))
	}

	// Log which sources were loaded for debugging configuration issues.
	if len(loadedSources) > 0 {
		slog.Debug("config sources loaded", "sources", loadedSources)
	}

	// Step 6: unmarshal with explicit decode hooks. koanf v2 defaults do NOT
	// include StringToSliceHookFunc, so comma-separated env vars / defaults
	// would not be split into []string without this.
	cfg := new(T)
	if err := k.UnmarshalWithConf("", cfg, koanf.UnmarshalConf{
		DecoderConfig: &mapstructure.DecoderConfig{
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
				mapstructure.StringToSliceHookFunc(","),
				mapstructure.TextUnmarshallerHookFunc(),
			),
			WeaklyTypedInput: true,
			Result:           cfg,
			TagName:          "koanf",
		},
	}); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	// Step 7: secret injection -- delegates to pkg/secrets. Uses the ctx
	// supplied to GetConfig, so secret-fetch latency and cancellation
	// flow through cleanly to the caller.
	if o.secretProvider != nil {
		if err := secrets.Populate(ctx, o.secretProvider, cfg); err != nil {
			// Log which secret key failed (not the value) for debuggability.
			slog.Error("secret injection failed",
				"error", err.Error(),
				"hint", "check that all secret keys referenced in 'secret' struct tags exist in the secret provider",
			)
			return nil, fmt.Errorf("populating secrets: %w", ErrSecretProvider)
		}
	}

	// Step 8: validation (includes required field checks via validate:"required").
	if !o.skipValidation {
		v := o.customValidator
		if v == nil {
			v = validator.New()
		}
		if err := v.Struct(cfg); err != nil {
			var ve validator.ValidationErrors
			if errors.As(err, &ve) {
				// Build field-level details preserving which field failed and what the constraint was.
				fieldErrors := make([]map[string]string, 0, len(ve))
				for _, fe := range ve {
					fieldErrors = append(fieldErrors, map[string]string{
						"field":      fe.Namespace(),
						"tag":        fe.Tag(),
						"constraint": fe.Param(),
						"value":      fmt.Sprintf("%v", fe.Value()),
					})
				}
				slog.Debug("config validation failed", "field_errors", fieldErrors)
				return nil, &validationError{
					msg:      fmt.Sprintf("config validation failed: fields=%v: %s", fieldErrorSummary(ve), ErrValidation),
					sentinel: ErrValidation,
					fieldErr: ve,
				}
			}
			return nil, fmt.Errorf("config validation: %w", ErrValidation)
		}
	}

	return cfg, nil
}

// fieldErrorSummary builds a compact summary of validation field errors
// for inclusion in the error message (e.g., "Name(required), Port(min=1)").
func fieldErrorSummary(ve validator.ValidationErrors) string {
	parts := make([]string, 0, len(ve))
	for _, fe := range ve {
		tag := fe.Tag()
		if fe.Param() != "" {
			tag += "=" + fe.Param()
		}
		parts = append(parts, fe.Field()+"("+tag+")")
	}
	return strings.Join(parts, ", ")
}
