//go:build unit

package config

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// safeBuffer is a buffer a reloader's goroutine may write while a test reads.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureDefault installs a slog default writing to a buffer for the test.
func captureDefault(t *testing.T) *safeBuffer {
	t.Helper()
	var buf safeBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// Without a logger, lines go to slog.Default() as it is when they are
// written, so a reloader built before the process logger is configured logs
// through the configured one.
func TestLogger_FollowsSetDefault(t *testing.T) {
	r, err := NewReloader[portCfg](t.Context(), []Option{WithDefaults(map[string]any{"port": 1}), WithSkipValidation()},
		WithPollInterval(time.Hour))
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(context.Background()) })

	buf := captureDefault(t)
	_, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "config sources loaded", "the load's lines")
	runReloader(t, r)
	eventually(t, func() bool { return strings.Contains(buf.String(), "config reloader started") }, "the reloader's start line")
}

// An explicit logger stays fixed; WithReloadLogger(nil) sends the reloader's
// lines to the default even when the load options name a logger.
func TestLogger_ExplicitAndExplicitNil(t *testing.T) {
	var fixed safeBuffer
	logger := slog.New(slog.NewTextHandler(&fixed, &slog.HandlerOptions{Level: slog.LevelDebug}))
	buf := captureDefault(t)

	r, err := NewReloader[portCfg](t.Context(), []Option{WithDefaults(map[string]any{"port": 1}), WithSkipValidation(), WithLogger(logger)},
		WithPollInterval(time.Hour))
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(context.Background()) })
	runReloader(t, r)
	eventually(t, func() bool { return strings.Contains(fixed.String(), "config reloader started") }, "the inherited logger")
	assert.NotContains(t, buf.String(), "config reloader started")

	r2, err := NewReloader[portCfg](t.Context(), []Option{WithDefaults(map[string]any{"port": 1}), WithSkipValidation(), WithLogger(logger)},
		WithPollInterval(time.Hour), WithReloadLogger(nil))
	require.NoError(t, err)
	t.Cleanup(func() { _ = r2.Stop(context.Background()) })
	runReloader(t, r2)
	eventually(t, func() bool { return strings.Contains(buf.String(), "config reloader started") }, "the default logger")
}

type rangeCfg struct {
	Min  int    `koanf:"min" validate:"min=0"`
	Max  int    `koanf:"max"`
	Mode string `koanf:"mode"`
}

var errMinMax = errors.New("min must not exceed max")

func (c *rangeCfg) Validate() error {
	if c.Min > c.Max {
		return errMinMax
	}
	return nil
}

// A Validatable's Validate runs after the validator, on the config the
// post-load hooks produced; its error is ErrValidation with it as the cause.
func TestValidation_RootValidate(t *testing.T) {
	load := func(opts ...Option) error {
		_, err := Load[rangeCfg](t.Context(), opts...)
		return err
	}
	err := load(WithDefaults(map[string]any{"min": 5, "max": 1}))
	require.ErrorIs(t, err, ErrValidation)
	require.ErrorIs(t, err, errMinMax)
	assert.Contains(t, err.Error(), "min must not exceed max")

	// The tag validator reports first.
	err = load(WithDefaults(map[string]any{"min": -1, "max": -5}))
	require.ErrorIs(t, err, ErrValidation)
	assert.NotErrorIs(t, err, errMinMax)

	// Post-load normalization happens before Validate.
	err = load(WithDefaults(map[string]any{"min": 5, "max": 1}), WithPostLoad(func(_ context.Context, c *rangeCfg) error {
		c.Max = c.Min
		return nil
	}))
	require.NoError(t, err)

	// A custom validator replaces the tag rules, not Validate; skipping
	// validation skips both.
	err = load(WithDefaults(map[string]any{"min": 5, "max": 1}), WithValidator(nopValidator{}))
	require.ErrorIs(t, err, errMinMax)
	require.NoError(t, load(WithDefaults(map[string]any{"min": 5, "max": 1}), WithSkipValidation()))
}

type nopValidator struct{}

func (nopValidator) Struct(any) error { return nil }

// A failed Validate under a reloader keeps the previous config.
func TestValidation_RootValidateGatesTheSwap(t *testing.T) {
	values := map[string]any{"min": 1, "max": 2}
	src, err := NewMapProvider(func(context.Context) (map[string]any, error) { return values, nil }, "")
	require.NoError(t, err)
	r, err := NewReloader[rangeCfg](t.Context(), []Option{WithBaseProvider(src, nil)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(context.Background()) })

	values = map[string]any{"min": 9, "max": 2}
	_, err = r.Reload(t.Context())
	require.ErrorIs(t, err, ErrReload)
	require.ErrorIs(t, err, errMinMax)
	assert.Equal(t, 1, r.Config().Min)
}

func TestNewMapProvider(t *testing.T) {
	type cfg struct {
		DB struct {
			Host string `koanf:"host"`
			Port int    `koanf:"port"`
		} `koanf:"db"`
	}
	rows := map[string]any{"db.host": "a", "db.port": 1}
	var seen context.Context
	src, err := NewMapProvider(func(ctx context.Context) (map[string]any, error) {
		seen = ctx
		return rows, nil
	}, ".")
	require.NoError(t, err)

	type key struct{}
	ctx := context.WithValue(t.Context(), key{}, "load")
	got, err := Load[cfg](ctx, WithBaseProvider(src, nil))
	require.NoError(t, err)
	assert.Equal(t, "a", got.DB.Host)
	assert.Equal(t, "load", seen.Value(key{}), "read sees the load's context")

	rows["db.host"] = "b" // read afresh on every load
	got, err = Load[cfg](t.Context(), WithBaseProvider(src, nil))
	require.NoError(t, err)
	assert.Equal(t, "b", got.DB.Host)

	for _, bad := range []map[string]any{
		{"db": "x", "db.host": "y"},
		{"db.": "x"},
		{"db..host": "x"},
		{"db": "x", "db-x": "y", "db.host": "z"},
	} {
		bad := bad
		p, err := NewMapProvider(func(context.Context) (map[string]any, error) { return bad, nil }, ".")
		require.NoError(t, err)
		_, err = Load[cfg](t.Context(), WithBaseProvider(p, nil), WithAllowUnknownKeys())
		assert.ErrorIs(t, err, ErrSource, "%v", bad)
	}

	nested, err := NewMapProvider(func(context.Context) (map[string]any, error) {
		return map[string]any{"db": map[string]any{"port": 5}}, nil
	}, "")
	require.NoError(t, err)
	got, err = Load[cfg](t.Context(), WithBaseProvider(nested, nil))
	require.NoError(t, err)
	assert.Equal(t, 5, got.DB.Port)

	empty, err := NewMapProvider(func(context.Context) (map[string]any, error) { return nil, nil }, ".")
	require.NoError(t, err)
	_, err = Load[cfg](t.Context(), WithBaseProvider(empty, nil))
	require.NoError(t, err)

	failing, err := NewMapProvider(func(context.Context) (map[string]any, error) { return nil, errors.New("store down") }, ".")
	require.NoError(t, err)
	_, err = Load[cfg](t.Context(), WithBaseProvider(failing, nil))
	require.ErrorIs(t, err, ErrSource)
	assert.Contains(t, err.Error(), "store down")

	_, err = NewMapProvider(nil, ".")
	require.Error(t, err)
	_, err = NewMapProvider(func(context.Context) (map[string]any, error) { return nil, nil }, "/")
	require.Error(t, err)
}

// What read returns is copied: a load cannot change the caller's map.
func TestNewMapProvider_CopiesWhatReadReturns(t *testing.T) {
	type cfg struct {
		Tags []string `koanf:"tags"`
	}
	tags := []any{"a"}
	src, err := NewMapProvider(func(context.Context) (map[string]any, error) {
		return map[string]any{"tags": tags}, nil
	}, "")
	require.NoError(t, err)
	m, err := src.ReadContext(t.Context())
	require.NoError(t, err)
	m["tags"].([]any)[0] = "changed"
	assert.Equal(t, "a", tags[0])
	_, err = src.ReadBytes()
	require.Error(t, err)
}
