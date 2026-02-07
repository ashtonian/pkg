package config

import (
	"context"
	"fmt"
	"os"
)

// SecretProvider retrieves secret values from an external store.
type SecretProvider interface {
	GetSecret(ctx context.Context, key string) (string, error)
}

// EnvSecretProvider retrieves secrets from environment variables with a prefix.
type EnvSecretProvider struct {
	prefix string
}

// NewEnvSecretProvider creates a provider that looks up secrets as
// environment variables with the given prefix. For example, with prefix
// "SECRET_", the key "db/password" maps to SECRET_DB_PASSWORD.
func NewEnvSecretProvider(prefix string) *EnvSecretProvider {
	return &EnvSecretProvider{prefix: prefix}
}

// GetSecret looks up the secret key as an environment variable.
func (p *EnvSecretProvider) GetSecret(_ context.Context, key string) (string, error) {
	envKey := p.prefix + envKeyFromSecret(key)
	val, ok := os.LookupEnv(envKey)
	if !ok {
		return "", fmt.Errorf("secret env var %s not set for key %q", envKey, key)
	}
	return val, nil
}

// MapSecretProvider retrieves secrets from an in-memory map. Useful for testing.
type MapSecretProvider struct {
	secrets map[string]string
}

// NewMapSecretProvider creates a provider backed by the given map.
func NewMapSecretProvider(secrets map[string]string) *MapSecretProvider {
	return &MapSecretProvider{secrets: secrets}
}

// GetSecret looks up the key in the map.
func (p *MapSecretProvider) GetSecret(_ context.Context, key string) (string, error) {
	val, ok := p.secrets[key]
	if !ok {
		return "", fmt.Errorf("secret %q not found in map provider", key)
	}
	return val, nil
}

// envKeyFromSecret converts a secret path like "db/password" to "DB_PASSWORD".
func envKeyFromSecret(key string) string {
	result := make([]byte, 0, len(key))
	for i := range len(key) {
		ch := key[i]
		switch {
		case ch == '/' || ch == '-' || ch == '.':
			result = append(result, '_')
		case ch >= 'a' && ch <= 'z':
			result = append(result, ch-32) // to upper
		default:
			result = append(result, ch)
		}
	}
	return string(result)
}
