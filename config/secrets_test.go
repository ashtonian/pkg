//go:build unit

package config

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type secretsDB struct {
	Host     string `koanf:"host" default:"localhost"`
	Password string `koanf:"password" secret:"db/password"`
	Replica  string `koanf:"replica" secret:"db/password"`
}

type secretsOpt struct {
	Key string `koanf:"key" secret:"opt/key"`
}

type secretsCfg struct {
	DB      secretsDB     `koanf:"db"`
	Workers int           `koanf:"workers" secret:"app/workers"`
	TTL     time.Duration `koanf:"ttl" secret:"app/ttl"`
	Opt     *secretsOpt   `koanf:"opt"`
}

func fakeStore(values map[string]string, calls map[string]int) func(context.Context, string) (string, error) {
	return func(_ context.Context, ref string) (string, error) {
		calls[ref]++
		v, ok := values[ref]
		if !ok {
			return "", fmt.Errorf("no secret %s", ref)
		}
		return v, nil
	}
}

func TestSecretsFrom(t *testing.T) {
	calls := map[string]int{}
	store := fakeStore(map[string]string{"db/password": "SECRET_DB", "app/workers": "8", "app/ttl": "90s"}, calls)
	cfg, err := Load[secretsCfg](t.Context(),
		WithDefaults(map[string]any{"db.password": "from-a-file"}),
		WithSecretPopulator(SecretsFrom(store)))
	require.NoError(t, err)
	assert.Equal(t, "SECRET_DB", cfg.DB.Password, "the secret replaces what the sources set")
	assert.Equal(t, "SECRET_DB", cfg.DB.Replica)
	assert.Equal(t, 8, cfg.Workers, "decoded into the field's type")
	assert.Equal(t, 90*time.Second, cfg.TTL)
	assert.Nil(t, cfg.Opt, "a section no source configured is left alone")
	assert.Zero(t, calls["opt/key"])
	assert.Equal(t, 1, calls["db/password"], "a ref on two fields is fetched once")

	m := RedactedMap(cfg)
	assert.Equal(t, redactedValue, m["db.password"], "a secret-tagged field is sensitive")
	assert.Equal(t, redactedValue, m["workers"])
}

func TestSecretsFrom_Failures(t *testing.T) {
	missing := errors.New("access denied")
	_, err := Load[secretsCfg](t.Context(), WithSecretPopulator(SecretsFrom(func(context.Context, string) (string, error) {
		return "", missing
	})))
	require.ErrorIs(t, err, ErrSecretProvider)
	require.ErrorIs(t, err, missing)
	assert.Contains(t, err.Error(), `resolving secret "db/password" for db.password`)

	_, err = Load[secretsCfg](t.Context(), WithSecretPopulator(SecretsFrom(fakeStore(
		map[string]string{"db/password": "p", "app/workers": "SECRET_NOT_A_NUMBER", "app/ttl": "1s"}, map[string]int{}))))
	require.ErrorIs(t, err, ErrSecretProvider)
	assert.NotContains(t, err.Error(), "SECRET_NOT_A_NUMBER")
	assert.Contains(t, err.Error(), "the secret does not decode into int")

	type listed struct {
		Items []secretsDB `koanf:"items"`
	}
	_, err = Load[listed](t.Context(), WithSecretPopulator(SecretsFrom(fakeStore(nil, map[string]int{}))))
	require.ErrorIs(t, err, ErrSecretProvider)
	assert.Contains(t, err.Error(), `items[*].password has secret:"db/password" inside a map or list`)
}

// The populator runs with the load's context.
func TestSecretsFrom_UsesTheLoadContext(t *testing.T) {
	type key struct{}
	var seen any
	type cfg struct {
		Token string `koanf:"token" secret:"t"`
	}
	ctx := context.WithValue(t.Context(), key{}, "load")
	_, err := Load[cfg](ctx, WithSecretPopulator(SecretsFrom(func(ctx context.Context, _ string) (string, error) {
		seen = ctx.Value(key{})
		return "x", nil
	})))
	require.NoError(t, err)
	assert.Equal(t, "load", seen)
}

// A #field takes one member of a JSON secret, fetched once for every field
// that reads it; ,optional leaves a field as the sources set it when the
// store has no such secret or member.
func TestSecretsFrom_MembersAndOptional(t *testing.T) {
	type cfg struct {
		User     string `koanf:"user" secret:"prod/db#username"`
		Password string `koanf:"password" secret:"prod/db#password"`
		Port     int    `koanf:"port" secret:"prod/db#port"`
		Region   string `koanf:"region" secret:"prod/db#region,optional"`
		APIKey   string `koanf:"api_key" secret:"prod/api,optional"`
		Plain    string `koanf:"plain" secret:"-"`
	}
	calls := map[string]int{}
	fetch := func(_ context.Context, ref string) (string, error) {
		calls[ref]++
		switch ref {
		case "prod/db":
			return `{"username": "app", "password": "SECRET_PW", "port": 5432}`, nil
		case "prod/api":
			return "", fmt.Errorf("%w: %s", ErrSecretNotFound, ref)
		}
		return "", errors.New("unexpected ref " + ref)
	}
	got, err := Load[cfg](t.Context(),
		WithDefaults(map[string]any{"region": "us-east-1", "api_key": "dev-key", "plain": "p"}),
		WithSecretPopulator(SecretsFrom(fetch)))
	require.NoError(t, err)
	assert.Equal(t, "app", got.User)
	assert.Equal(t, "SECRET_PW", got.Password)
	assert.Equal(t, 5432, got.Port)
	assert.Equal(t, "us-east-1", got.Region, "an optional member the secret lacks keeps the source value")
	assert.Equal(t, "dev-key", got.APIKey, "an optional secret the store lacks keeps the source value")
	assert.Equal(t, "p", got.Plain)
	assert.Equal(t, 1, calls["prod/db"])
	assert.Equal(t, "p", RedactedMap(got)["plain"], `secret:"-" is not a secret`)

	type required struct {
		Key string `koanf:"key" secret:"prod/api"`
	}
	_, err = Load[required](t.Context(), WithSecretPopulator(SecretsFrom(fetch)))
	require.ErrorIs(t, err, ErrSecretProvider)
	require.ErrorIs(t, err, ErrSecretNotFound)

	type notJSON struct {
		Key string `koanf:"key" secret:"plain#field"`
	}
	_, err = Load[notJSON](t.Context(), WithSecretPopulator(SecretsFrom(func(context.Context, string) (string, error) {
		return "SECRET_RAW", nil
	})))
	require.ErrorIs(t, err, ErrSecretProvider)
	assert.Contains(t, err.Error(), "not a JSON object")
	assert.NotContains(t, err.Error(), "SECRET_RAW")
}
