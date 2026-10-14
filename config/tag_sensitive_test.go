package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type optOutCfg struct {
	MaxTokens   int               `koanf:"max_tokens" sensitive:"false"`
	AuthEnabled bool              `koanf:"auth_enabled" sensitive:"false"`
	TokenTTL    string            `koanf:"token_ttl"`
	Headers     map[string]string `koanf:"headers" sensitive:"false"`
	Vault       struct {
		KeyPath string `koanf:"key_path" sensitive:"false"`
	} `koanf:"vault" sensitive:"true"`
}

// sensitive:"false" takes a field out of the credential-name rule, in every
// output; a name without the tag, a key named like a credential beneath it,
// and an enclosing sensitive:"true" still redact.
func TestSensitiveFalse_OptsOutOfTheNameRule(t *testing.T) {
	cfg := &optOutCfg{MaxTokens: 4096, AuthEnabled: true, TokenTTL: "1h",
		Headers: map[string]string{"accept": "json", "authorization": "Bearer x"}}
	cfg.Vault.KeyPath = "transit/app"

	assert.Equal(t, map[string]string{
		"max_tokens":               "4096",
		"auth_enabled":             "true",
		"token_ttl":                redactedValue,
		`headers["accept"]`:        "json",
		`headers["authorization"]`: redactedValue,
		"vault.key_path":           redactedValue,
	}, RedactedMap(cfg))

	next := *cfg
	next.MaxTokens, next.TokenTTL = 8192, "2h"
	changes, err := Diff(cfg, &next)
	require.NoError(t, err)
	require.Len(t, changes, 2)
	assert.Equal(t, "max_tokens", changes[0].Key)
	assert.Equal(t, "4096", changes[0].Old.Text)
	assert.Equal(t, "8192", changes[0].New.Text)
	assert.Equal(t, "token_ttl", changes[1].Key)
	assert.True(t, changes[1].New.Redacted)

	sensitive := map[string]bool{}
	for _, f := range Fields[optOutCfg]() {
		sensitive[f.Path] = f.Sensitive
	}
	assert.Equal(t, map[string]bool{"max_tokens": false, "auth_enabled": false, "token_ttl": true, "headers": false, "vault.key_path": true}, sensitive)
}

// A sensitive tag must say true or false, and a field that names a secret
// cannot opt out; a misspelled value still redacts where nothing loads.
func TestSensitiveTag_Refused(t *testing.T) {
	type yes struct {
		Note string `koanf:"note" sensitive:"yes"`
	}
	_, err := Load[yes](t.Context(), WithSkipValidation())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), `sensitive:"yes"`)
	assert.Equal(t, map[string]string{"note": redactedValue}, RedactedMap(&yes{Note: "x"}))

	type secretOptOut struct {
		Password string `koanf:"password" secret:"db#password" sensitive:"false"`
	}
	_, err = Load[secretOptOut](t.Context(), WithSkipValidation())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "names a secret")
}
