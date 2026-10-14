//go:build unit

package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A caller's loader gets the whole lifecycle: an initial load, a swap only on
// success, the previous config kept on failure, callbacks and stats.
func TestNewReloaderFunc_SwapsOnlyOnSuccess(t *testing.T) {
	var next atomic.Int32
	next.Store(1)
	failing := errors.New("source unavailable")
	var fail atomic.Bool
	var swaps atomic.Int32
	r, err := NewReloaderFunc(t.Context(),
		func(context.Context) (*portCfg, error) {
			if fail.Load() {
				return nil, failing
			}
			return &portCfg{Port: int(next.Add(1))}, nil
		},
		WithOnReloadSuccess(func(old, new *portCfg) { swaps.Add(1) }),
		WithReloadLogger(discardLogger()),
	)
	require.NoError(t, err)
	assert.Equal(t, 2, r.Config().Port)

	got, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 3, got.Port)
	assert.Same(t, got, r.Config())

	fail.Store(true)
	_, err = r.Reload(t.Context())
	require.ErrorIs(t, err, ErrReload)
	require.ErrorIs(t, err, failing)
	assert.Equal(t, 3, r.Config().Port, "a failed reload keeps the previous config")
	assert.Equal(t, int32(1), swaps.Load())
	s := r.Stats()
	assert.Equal(t, uint64(2), s.Attempts)
	assert.Equal(t, uint64(1), s.Failures)
	require.NoError(t, r.Stop(t.Context()))
}

func TestNewReloaderFunc_InitialFailure(t *testing.T) {
	_, err := NewReloaderFunc(t.Context(), func(context.Context) (*portCfg, error) { return nil, os.ErrNotExist })
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = NewReloaderFunc(t.Context(), func(context.Context) (*portCfg, error) { return nil, nil })
	require.Error(t, err)
}

// The files a caller's loader reads are watched through WithWatchedFiles,
// and one changed after the load but before the watch armed is caught up.
func TestNewReloaderFunc_WatchesNamedFiles(t *testing.T) {
	file := filepath.Join(realTempDir(t), "port")
	writeFile(t, file, "1")
	readPort := func(context.Context) (*portCfg, error) {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		return &portCfg{Port: len(strings.TrimSpace(string(b)))}, nil
	}
	r, err := NewReloaderFunc(t.Context(), readPort,
		WithWatchedFiles(file), WithFileWatch(10*time.Millisecond), WithReloadLogger(discardLogger()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(context.Background()) })

	writeFile(t, file, "22") // before the watch exists
	armReloader(t, r)
	runReloader(t, r)
	eventually(t, func() bool { return r.Config().Port == 2 }, "the catch-up reload")

	writeFile(t, file, "333")
	eventually(t, func() bool { return r.Config().Port == 3 }, "the file-change reload")
}

// WithWatchedFiles adds files to a NewReloader's watch beyond its config
// files — here one a secret populator reads.
func TestNewReloader_WatchedFilesBeyondConfigFiles(t *testing.T) {
	dir := realTempDir(t)
	file, secret := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "secret")
	writeFile(t, file, "host: h\n")
	writeFile(t, secret, "one")
	type cfg struct {
		Host   string `koanf:"host"`
		Secret string `koanf:"-"`
	}
	r, err := NewReloader[cfg](t.Context(),
		[]Option{
			WithFile(file), WithSkipValidation(), WithLogger(discardLogger()),
			WithSecretPopulator(func(_ context.Context, target any) error {
				b, err := os.ReadFile(secret)
				target.(*cfg).Secret = string(b)
				return err
			}),
		},
		WithWatchedFiles(secret), WithFileWatch(10*time.Millisecond),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(context.Background()) })
	armReloader(t, r)
	runReloader(t, r)

	writeFile(t, secret, "two")
	eventually(t, func() bool { return r.Config().Secret == "two" }, "a reload after the secret file changed")
}

func TestNewReloader_RefusesMisconfiguration(t *testing.T) {
	type other struct{}
	_, err := NewReloader[portCfg](t.Context(), []Option{WithSkipValidation()},
		WithOnReloadSuccess(func(old, new *other) {}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithOnReloadSuccess")

	_, err = NewReloader[portCfg](t.Context(), []Option{WithSkipValidation()}, WithFileWatch(0))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no files to watch")
}
