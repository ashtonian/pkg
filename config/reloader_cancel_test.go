package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A reload whose context ends — before it starts, or inside a post-load hook
// that then returns nil — publishes nothing, counts as no attempt and fires no
// callback.
func TestReload_CancelledContextPublishesNothing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	write := func(port string) {
		if err := os.WriteFile(file, []byte("port: "+port+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("9000")
	var cancelInHook context.CancelFunc
	callbacks := 0
	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]Option{WithFile(file), WithSkipValidation(), WithPostLoad(func(context.Context, *reloaderCfg) error {
			if cancelInHook != nil {
				cancelInHook()
			}
			return nil
		})},
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[reloaderCfg]) { callbacks++ }),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop(context.Background())
	write("9001")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Reload(cancelled); !errors.Is(err, context.Canceled) || errors.Is(err, ErrReload) {
		t.Fatalf("Reload with a cancelled context = %v, want context.Canceled and not ErrReload", err)
	}

	hookCtx, cancel := context.WithCancel(context.Background())
	cancelInHook = cancel
	if _, err := r.Reload(hookCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Reload cancelled inside the post-load hook = %v, want context.Canceled", err)
	}
	cancelInHook = nil

	if got := r.Config().Port; got != 9000 {
		t.Errorf("an abandoned reload published port %d", got)
	}
	if s := r.Stats(); s.Attempts != 0 || callbacks != 0 {
		t.Errorf("abandoned reloads counted %d attempts and fired %d callbacks", s.Attempts, callbacks)
	}
}

// stageCounts counts the calls a load makes into the caller's code.
type stageCounts struct {
	base, environ, override, decodeHook, secrets, postLoad, validator int
}

type cancelCfg struct {
	Port int `koanf:"port"`
}

var defaulterCalls int

type cancelDefaultedCfg struct {
	Port int `koanf:"port"`
}

func (c *cancelDefaultedCfg) SetDefaults() { defaulterCalls++ }

type countingValidator struct{ calls *int }

func (v countingValidator) Struct(any) error { *v.calls++; return nil }

// stagedOptions builds a load that touches every caller-supplied stage,
// counting each call, and runs midway when the base provider reads.
func stagedOptions[T any](t *testing.T, n *stageCounts, midway func()) []Option {
	t.Helper()
	provider := func(count *int, data map[string]any, then func()) ContextProvider {
		p, err := NewMapProvider(func(context.Context) (map[string]any, error) {
			*count++
			if then != nil {
				then()
			}
			return data, nil
		}, "")
		require.NoError(t, err)
		return p
	}
	return []Option{
		WithBaseProvider(provider(&n.base, map[string]any{"port": 1}, midway), nil),
		WithEnvPrefix("APP"),
		WithEnviron(func() []string { n.environ++; return []string{"APP_PORT=2"} }),
		WithOverrideProvider(provider(&n.override, map[string]any{"port": 3}, nil), nil),
		WithDecodeHook(func(from, to reflect.Type, data any) (any, error) { n.decodeHook++; return data, nil }),
		WithSecretPopulator(func(context.Context, any) error { n.secrets++; return nil }),
		WithPostLoad(func(context.Context, *T) error { n.postLoad++; return nil }),
		WithValidator(countingValidator{&n.validator}),
	}
}

var errShutdown = errors.New("shutting down")

// A load whose context has ended runs nothing of the caller's and reports
// the cancellation with its cause; one that ends while a source is read
// runs no later stage. A live context runs every stage.
func TestLoad_EndedContextRunsNoFurtherStage(t *testing.T) {
	ended, cancel := context.WithCancelCause(context.Background())
	cancel(errShutdown)

	defaulterCalls = 0
	var n stageCounts
	_, err := Load[cancelDefaultedCfg](ended, stagedOptions[cancelDefaultedCfg](t, &n, nil)...)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, errShutdown)
	assert.Equal(t, stageCounts{}, n)
	assert.Zero(t, defaulterCalls)

	_, err = Decode[cancelCfg](ended, map[string]any{"port": 1}, stagedOptions[cancelCfg](t, &n, nil)...)
	require.ErrorIs(t, err, errShutdown)
	assert.Equal(t, stageCounts{}, n)
	_, err = LoadSnapshot[cancelCfg](ended, stagedOptions[cancelCfg](t, &n, nil)...)
	require.ErrorIs(t, err, errShutdown)
	assert.Equal(t, stageCounts{}, n)

	midway, cancel := context.WithCancelCause(context.Background())
	_, err = Load[cancelCfg](midway, stagedOptions[cancelCfg](t, &n, func() { cancel(errShutdown) })...)
	require.ErrorIs(t, err, errShutdown)
	assert.Equal(t, stageCounts{base: 1}, n, "the provider already running finishes; nothing after it starts")

	n = stageCounts{}
	got, err := Load[cancelDefaultedCfg](t.Context(), stagedOptions[cancelDefaultedCfg](t, &n, nil)...)
	require.NoError(t, err)
	assert.Equal(t, 3, got.Port)
	assert.Equal(t, 1, defaulterCalls)
	assert.Equal(t, 1, n.base)
	assert.Equal(t, 1, n.environ)
	assert.Equal(t, 1, n.override)
	assert.Positive(t, n.decodeHook)
	assert.Equal(t, 1, n.secrets)
	assert.Equal(t, 1, n.postLoad)
	assert.Equal(t, 1, n.validator)
}

// A reloader whose construction context has ended, or ends while the first
// config loads, is not built: its loader does not run, or what it returns
// is neither applied nor published. A live context publishes generation 1.
func TestNewReloader_EndedContextPublishesNothing(t *testing.T) {
	applied := 0
	applier := WithApplier(Applier[cancelCfg]{Name: "port", Prefixes: []string{"port"}, ApplyInitial: true,
		Apply: func(context.Context, ApplyEvent[cancelCfg]) (ApplyResult, error) {
			applied++
			return ApplyResult{}, nil
		}})
	ended, cancel := context.WithCancelCause(context.Background())
	cancel(errShutdown)

	loads := 0
	loader := func(context.Context) (*cancelCfg, error) { loads++; return &cancelCfg{Port: 1}, nil }
	r, err := NewReloaderFunc(ended, loader, applier)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, errShutdown)
	assert.Nil(t, r)
	assert.Zero(t, loads)

	var n stageCounts
	built, err := NewReloader[cancelCfg](ended, stagedOptions[cancelCfg](t, &n, nil), applier)
	require.ErrorIs(t, err, errShutdown)
	assert.Nil(t, built)
	assert.Equal(t, stageCounts{}, n)

	midway, cancel := context.WithCancelCause(context.Background())
	r, err = NewReloaderFunc(midway, func(context.Context) (*cancelCfg, error) {
		cancel(errShutdown)
		return &cancelCfg{Port: 1}, nil
	}, applier)
	require.ErrorIs(t, err, errShutdown)
	assert.NotErrorIs(t, err, ErrReload)
	assert.Nil(t, r)
	assert.Zero(t, applied, "a candidate loaded as the context ended is not applied")

	midway, cancel = context.WithCancelCause(context.Background())
	stopping := WithApplier(Applier[cancelCfg]{Name: "first", Prefixes: []string{"port"}, ApplyInitial: true,
		Apply: func(context.Context, ApplyEvent[cancelCfg]) (ApplyResult, error) {
			cancel(errShutdown)
			return ApplyResult{}, nil
		}})
	r, err = NewReloaderFunc(midway, loader, stopping)
	require.ErrorIs(t, err, errShutdown)
	assert.Nil(t, r, "applied as the context ended, still not published")

	r, err = NewReloaderFunc(t.Context(), loader, applier)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(context.Background()) })
	assert.Equal(t, uint64(1), r.Snapshot().Generation)
	assert.Equal(t, 1, r.Config().Port)
	assert.Equal(t, 1, applied)
	assert.Zero(t, r.Stats().Attempts, "the first load is not a reload")
}
