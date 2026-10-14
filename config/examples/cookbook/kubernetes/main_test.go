//go:build unit

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeVolume updates dir as the kubelet updates a ConfigMap or Secret
// volume: the files land in a new timestamped directory, ..data is swapped to
// it in one rename, and each key is a symlink through ..data.
func writeVolume(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	ts := filepath.Join(dir, fmt.Sprintf("..%d", time.Now().UnixNano()))
	require.NoError(t, os.MkdirAll(ts, 0o755))
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(ts, name), []byte(content), 0o644))
	}
	tmp := filepath.Join(dir, "..data_tmp")
	require.NoError(t, os.Symlink(filepath.Base(ts), tmp))
	require.NoError(t, os.Rename(tmp, filepath.Join(dir, "..data")))
	for name := range files {
		link := filepath.Join(dir, name)
		if _, err := os.Lstat(link); os.IsNotExist(err) {
			require.NoError(t, os.Symlink(filepath.Join("..data", name), link))
		}
	}
}

func TestNewReloader_FollowsVolumeUpdates(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	configDir, secretDir := filepath.Join(root, "config"), filepath.Join(root, "secrets")
	writeVolume(t, configDir, map[string]string{"config.yaml": "db:\n  host: db.internal\n  max_conns: 10\n"})
	writeVolume(t, secretDir, map[string]string{"db.password": "first\n"})

	secrets := secretVolume(secretDir)
	r, err := newReloader(t.Context(), configDir, secrets)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(context.Background()) })
	assert.Equal(t, "db.internal", r.Config().DB.Host)
	assert.Equal(t, "first", r.Config().DB.Password, "the mounted value, trimmed")
	require.NoError(t, r.Arm(t.Context()))
	require.NoError(t, watchSecrets(secrets, r, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { _ = secrets.Unwatch() })
	go func() { _ = r.Run(t.Context()) }()

	writeVolume(t, secretDir, map[string]string{"db.password": "second\n"})
	assert.Eventually(t, func() bool { return r.Config().DB.Password == "second" }, 10*time.Second, 20*time.Millisecond,
		"a rotated Secret reloads")

	writeVolume(t, configDir, map[string]string{"config.yaml": "db:\n  host: db.internal\n  max_conns: 0\n"})
	assert.Eventually(t, func() bool { return r.Stats().Failures > 0 }, 10*time.Second, 20*time.Millisecond)
	assert.Equal(t, 10, r.Config().DB.MaxConns, "an invalid ConfigMap keeps the last valid config")
}
