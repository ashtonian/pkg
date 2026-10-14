//go:build unit

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/config"
)

// fakeVault serves one KV v2 secret as Vault's read endpoint does.
type fakeVault struct {
	mu   sync.Mutex
	data map[string]any
}

func (f *fakeVault) set(data map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data = data
}

func (f *fakeVault) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/secret/data/app" || r.Header.Get("X-Vault-Token") != "t0ken" {
		http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": map[string]any{"data": f.data, "metadata": map[string]any{"version": 1}},
	})
}

func TestVaultSecret(t *testing.T) {
	store := &fakeVault{data: map[string]any{"db.password": "SECRET_ONE", "signing_key": "k1"}}
	server := httptest.NewServer(store)
	t.Cleanup(server.Close)
	var boot BootConfig
	boot.Vault.Address, boot.Vault.Token, boot.Vault.Path, boot.Vault.Timeout = server.URL, "t0ken", "secret/data/app", time.Second
	secrets, err := vaultSource(&boot)
	require.NoError(t, err)

	r, err := newReloader(t.Context(), secrets, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(t.Context()) })
	assert.Equal(t, "SECRET_ONE", r.Config().DB.Password)
	assert.Equal(t, "k1", r.Config().SigningKey)

	store.set(map[string]any{"db.password": "SECRET_TWO", "signing_key": "k1"})
	_, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "SECRET_TWO", r.Config().DB.Password)

	store.set(map[string]any{"db.password": "SECRET_TWO", "signing_kye": "k2"})
	_, err = r.Reload(t.Context())
	require.ErrorIs(t, err, config.ErrValidation, "a key naming no field fails the load")
	assert.Contains(t, err.Error(), "signing_kye")
	assert.NotContains(t, err.Error(), "SECRET_TWO")
	assert.Equal(t, "k1", r.Config().SigningKey)
}
