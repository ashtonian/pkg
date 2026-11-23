package config

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type withheldItem struct {
	Port int `koanf:"port"`
}

type withheldVault struct {
	Addr string                    `koanf:"addr"`
	Rest map[string]map[int]string `koanf:",remain" sensitive:"true"`
}

type withheldCfg struct {
	Port    int                       `koanf:"port"`
	Secrets map[int]string            `koanf:"secrets" sensitive:"true"`
	Nested  map[string]map[int]string `koanf:"nested" sensitive:"true"`
	Secret  any                       `koanf:"secret" sensitive:"true" validate:"omitempty,dive,min=3"`
	Opaque  any                       `koanf:"opaque" sensitive:"true"`
	Items   map[string]withheldItem   `koanf:"items" sensitive:"true"`
	Plain   map[string]withheldItem   `koanf:"plain"`
	Vault   withheldVault             `koanf:"vault"`
}

// ownTexts is the text of err and of every error beneath it that this
// package words, as Error and %+v write them: all but a validator's own
// errors, the cause a *KeyError keeps for trusted diagnostics.
func ownTexts(err error) []string {
	var out []string
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if _, ok := e.(validator.ValidationErrors); ok {
			return
		}
		out = append(out, e.Error(), fmt.Sprintf("%+v", e))
		switch u := e.(type) {
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		case interface{ Unwrap() []error }:
			for _, x := range u.Unwrap() {
				walk(x)
			}
		}
	}
	walk(err)
	return out
}

// assertWithholds checks that no text err, or ke within it, words names
// withheld.
func assertWithholds(t *testing.T, err error, withheld string) {
	t.Helper()
	for _, text := range ownTexts(err) {
		assert.NotContains(t, text, withheld)
	}
}

// A *KeyError names its key as diagnostics may show it, and so does all a
// key check, a batch of edits or a load words about it: Err's text, which
// Unwrap returns, Error and %+v, and every error this package wraps beneath
// it. A reason naming the part of a key Key withholds is not shown.
func TestKeyError_ErrWithholdsWhatKeyWithholds(t *testing.T) {
	ctx := t.Context()
	typedEntry := map[string]any{`opaque.prod_db`: map[int]string{1: "abc"}, `opaque.prod_db["x"]`: "abc"}
	entries := func(m map[string]any) func(context.Context) (map[string]any, error) {
		return func(context.Context) (map[string]any, error) { return m, nil }
	}
	tests := []struct {
		name     string
		err      func() error
		withheld string // what no text may name
		key      string
		unknown  bool
		says     string // what Err does say
	}{
		{"CheckKey: an entry a sensitive map's key type cannot read", func() error {
			return CheckKey[withheldCfg](ctx, `secrets["prod_db_key"]`, "x")
		}, "prod", "secrets", true, "invalid configuration key secrets: secrets is keyed by int"},
		{"CanonicalKey: a key beneath that entry", func() error {
			_, err := CanonicalKey[withheldCfg](`secrets["prod_db_key"].x`)
			return err
		}, "prod", "secrets", true, "invalid configuration key secrets: secrets is keyed by int"},
		{"CheckKey: an entry of a map within a sensitive map", func() error {
			return CheckKey[withheldCfg](ctx, `nested["prod_db"]["x"]`, "x")
		}, "prod", "nested", true, "invalid configuration key nested: the reason names sensitive contents, which are not shown"},
		{"DecodeKey: a field no element of a sensitive map has", func() error {
			_, err := DecodeKey[withheldCfg](ctx, `items["prod_db"].prot`, 1)
			return err
		}, "prod", "items", true, "invalid configuration key items: the reason names sensitive contents, which are not shown"},
		{"CanonicalKey: beneath a key a sensitive remain field collects", func() error {
			_, err := CanonicalKey[withheldCfg](`vault.prod_db["x"]`)
			return err
		}, "prod", "vault", true, "invalid configuration key vault: the reason names sensitive contents, which are not shown"},
		{"DecodeEdits: an entry of a typed map within a sensitive interface", func() error {
			_, err := DecodeEdits[withheldCfg](ctx, nil, typedEntry)
			return err
		}, "prod", "opaque", false, "the reason names sensitive contents, which are not shown"},
		{"DecodeKey: a key that does not parse, holding a credential", func() error {
			_, err := DecodeKey[withheldCfg](ctx, "secret.postgres://u:prodpass@h[", 1)
			return err
		}, "prodpass", RedactText("secret.postgres://u:prodpass@h["), true, "does not parse"},
		{"DecodeKey: a value a sensitive interface's rule refuses", func() error {
			_, err := DecodeKey[withheldCfg](ctx, "secret.prod_password", "ab")
			return err
		}, "prod", "secret", false, "secret(min)"},
		{"Load: an entry a source without WithInvalidEntries cannot set", func() error {
			_, err := Load[withheldCfg](ctx, WithBaseEntries(entries(typedEntry)))
			return err
		}, "prod", "opaque", false, "the reason names sensitive contents, which are not shown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.err()
			require.Error(t, err)
			assertWithholds(t, err, tt.withheld)
			var ke *KeyError
			require.ErrorAs(t, err, &ke)
			assert.Equal(t, tt.key, ke.Key)
			assert.Equal(t, tt.unknown, ke.Unknown)
			assert.Equal(t, tt.unknown, errors.Is(ke, ErrKey))
			assert.Equal(t, !tt.unknown, errors.Is(ke, ErrValidation))
			cause := errors.Unwrap(ke)
			require.Error(t, cause)
			assert.Contains(t, cause.Error(), tt.says)
			for _, text := range []string{ke.Error(), fmt.Sprintf("%+v", ke), cause.Error(), fmt.Sprintf("%+v", cause)} {
				assert.NotContains(t, text, tt.withheld)
			}
		})
	}

	t.Run("WithInvalidEntries: the entries it withholds", func(t *testing.T) {
		var rejected []KeyError
		_, err := Load[withheldCfg](ctx, WithBaseEntries(entries(typedEntry), WithInvalidEntries(func(_ context.Context, ke []KeyError) error {
			rejected = ke
			return nil
		})))
		require.NoError(t, err)
		require.Len(t, rejected, 1)
		assert.Equal(t, "opaque", rejected[0].Key)
		assert.Contains(t, rejected[0].Error(), "the reason names sensitive contents, which are not shown")
		assertWithholds(t, &rejected[0], "prod")
	})

	t.Run("an entries source's unknown key", func(t *testing.T) {
		_, err := Load[withheldCfg](ctx, WithBaseEntries(entries(map[string]any{`items["prod_db"].prot`: 1})))
		var unknown *UnknownKeysError
		require.ErrorAs(t, err, &unknown)
		assert.Equal(t, []UnknownKey{{Key: "items"}}, unknown.Keys)
		assertWithholds(t, err, "prod")
	})

	t.Run("a validator's own errors stay the trusted cause", func(t *testing.T) {
		_, err := DecodeKey[withheldCfg](ctx, "secret.prod_password", "ab")
		var ve validator.ValidationErrors
		assert.ErrorAs(t, err, &ve)
	})
}

// A key no field reads is named whole in Err, beside where the walk along
// it stopped.
func TestKeyError_ErrNamesTheWholeKey(t *testing.T) {
	err := CheckKey[withheldCfg](t.Context(), "port.x", 1)
	var ke *KeyError
	require.ErrorAs(t, err, &ke)
	assert.Equal(t, "port.x", ke.Key)
	assert.EqualError(t, ke.Err, "invalid configuration key port.x: port is a int, which has no keys beneath it")

	_, err = DecodeKey[withheldCfg](t.Context(), `plain["a"].prot`, 1)
	require.ErrorAs(t, err, &ke)
	assert.EqualError(t, ke.Err, `invalid configuration key plain["a"].prot: plain["a"] has no field "prot"`)
}
