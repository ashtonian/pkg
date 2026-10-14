package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/config"
)

// fakeStore answers GetSecretValue from a map, by secret name.
type fakeStore struct {
	secrets map[string]string
	calls   []string
}

func (f *fakeStore) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	name := aws.ToString(in.SecretId)
	f.calls = append(f.calls, name)
	value, ok := f.secrets[name]
	if !ok {
		return nil, &types.ResourceNotFoundException{Message: aws.String("not found")}
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: aws.String(value)}, nil
}

func TestSecretsFromSecretsManager(t *testing.T) {
	store := &fakeStore{secrets: map[string]string{
		"test/app/db": `{"username":"app","password":"SECRET_ONE"}`,
	}}
	boot := &BootConfig{SecretPrefix: "test/app/", RotationCheck: 0}
	r, err := newReloader(t.Context(), store, boot)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(t.Context()) })

	cfg := r.Config()
	assert.Equal(t, "app", cfg.DB.User)
	assert.Equal(t, "SECRET_ONE", cfg.DB.Password)
	assert.Empty(t, cfg.WebhookKey, "an optional secret the store lacks is left to the sources")
	assert.Equal(t, []string{"test/app/db", "test/app/webhook-key"}, store.calls, "one fetch per secret, both fields included")

	var out bytes.Buffer
	require.NoError(t, config.PrintRedacted(cfg, config.WithWriter(&out)))
	assert.NotContains(t, out.String(), "SECRET_ONE", "a secret-tagged field is redacted")

	store.secrets["test/app/db"] = `{"username":"app","password":"SECRET_TWO"}`
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "SECRET_TWO", r.Config().DB.Password, "a rotation reaches the next load")
	require.Len(t, res.Changes, 1)
	assert.True(t, res.Changes[0].New.Redacted)

	delete(store.secrets, "test/app/db")
	_, err = r.Reload(t.Context())
	require.ErrorIs(t, err, config.ErrSecretProvider)
	assert.Contains(t, err.Error(), "db.user")
	assert.Equal(t, "SECRET_TWO", r.Config().DB.Password, "a required secret gone keeps the last valid config")
}
