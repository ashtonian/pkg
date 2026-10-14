//go:build unit

package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/config"
	"github.com/ashtonian/pkg/config/kongprovider"
)

func TestService(t *testing.T) {
	file := filepath.Join(t.TempDir(), "service.yaml")
	write := func(content string) { require.NoError(t, os.WriteFile(file, []byte(content), 0o600)) }
	write("greeting: hi\nadmin_token: t0ken\nlog:\n  level: warn\n")

	var cli CLI
	parser, err := kong.New(&cli, kongprovider.HelpFromConfig(&cli.AppConfig, "APP"))
	require.NoError(t, err)
	kctx, err := parser.Parse([]string{"--config", file, "--log-level", "debug"})
	require.NoError(t, err)

	level := new(slog.LevelVar)
	r, err := newReloader(t.Context(), &cli, kctx, slog.New(slog.DiscardHandler), level)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(t.Context()) })
	assert.Equal(t, slog.LevelDebug, level.Level(), "the flag beats the file, applied before serving")

	srv := httptest.NewServer(routes(r))
	t.Cleanup(srv.Close)
	get := func(path, token string) (int, string) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(b)
	}
	code, body := get("/", "")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "hi\n", body)
	code, _ = get("/admin/config", "wrong")
	assert.Equal(t, http.StatusUnauthorized, code)

	write("greeting: hello again\nadmin_token: t0ken\nlog:\n  level: error\nhttp:\n  addr: :9090\n")
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	_, body = get("/", "")
	assert.Equal(t, "hello again\n", body, "read from Config on every request")
	assert.Equal(t, slog.LevelDebug, level.Level(), "the flag still wins")
	var pending []string
	for _, c := range res.Snapshot.PendingRestart {
		pending = append(pending, c.Key)
	}
	assert.Equal(t, []string{"http.addr"}, pending, "the listener's address waits for a restart")

	code, body = get("/admin/config", "t0ken")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, `"pending_restart"`)
	assert.NotContains(t, body, "t0ken")

	write("greeting: \"  \"\nadmin_token: t0ken\n")
	_, err = r.Reload(t.Context())
	require.ErrorIs(t, err, config.ErrValidation, "Validate refuses a blank greeting")
	_, body = get("/", "")
	assert.Equal(t, "hello again\n", body)
}
