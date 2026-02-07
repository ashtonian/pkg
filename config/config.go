package config

import (
	"context"
	"fmt"

	"github.com/go-playground/validator/v10"
)

// Option configures a Loader.
type Option func(*loaderOptions)

// WithEnvPrefix sets the prefix for environment variable lookups.
// For example, WithEnvPrefix("APP") causes field Host to resolve from APP_HOST.
func WithEnvPrefix(prefix string) Option {
	return func(o *loaderOptions) {
		o.envPrefix = prefix
	}
}

// WithConfigFile adds a config file path. Missing files are allowed by default.
func WithConfigFile(path string) Option {
	return func(o *loaderOptions) {
		o.configFiles = append(o.configFiles, configFileEntry{path: path, required: false})
	}
}

// WithMustConfigFile adds a config file path that must exist.
func WithMustConfigFile(path string) Option {
	return func(o *loaderOptions) {
		o.configFiles = append(o.configFiles, configFileEntry{path: path, required: true})
	}
}

// WithConfigFiles adds multiple optional config file paths, merged left-to-right.
func WithConfigFiles(paths ...string) Option {
	return func(o *loaderOptions) {
		for _, p := range paths {
			o.configFiles = append(o.configFiles, configFileEntry{path: p, required: false})
		}
	}
}

// WithMustConfigFiles adds multiple required config file paths, merged left-to-right.
func WithMustConfigFiles(paths ...string) Option {
	return func(o *loaderOptions) {
		for _, p := range paths {
			o.configFiles = append(o.configFiles, configFileEntry{path: p, required: true})
		}
	}
}

// WithDefaults sets programmatic default values. Keys should match env var names.
func WithDefaults(defaults map[string]any) Option {
	return func(o *loaderOptions) {
		o.defaults = defaults
	}
}

// WithSecretProvider configures a secret provider for fields with secret tags.
func WithSecretProvider(sp SecretProvider, ctx context.Context) Option {
	return func(o *loaderOptions) {
		o.secretProvider = sp
		o.secretCtx = ctx
	}
}

// WithSkipValidation disables go-playground/validator validation.
func WithSkipValidation() Option {
	return func(o *loaderOptions) {
		o.skipValidation = true
	}
}

// WithValidator sets a custom validator instance.
func WithValidator(v *validator.Validate) Option {
	return func(o *loaderOptions) {
		o.customValidator = v
	}
}

// GetConfig is a shorthand that creates a Loader, loads, and returns the config.
func GetConfig[T any](opts ...Option) (*T, error) {
	loader, err := NewLoader[T](opts...)
	if err != nil {
		return nil, fmt.Errorf("creating config loader: %w", err)
	}
	return loader.Load()
}
