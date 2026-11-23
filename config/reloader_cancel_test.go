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

// cancelDefaultedCfg records in the config it loads that its Defaulter ran.
type cancelDefaultedCfg struct {
	Port      int  `koanf:"port"`
	Defaulted bool `koanf:"defaulted"`
}

func (c *cancelDefaultedCfg) SetDefaults() { c.Defaulted = true }

// cancelUncalledDefaulterCfg has a Defaulter a load must not call.
type cancelUncalledDefaulterCfg struct {
	Port int `koanf:"port"`
}

func (c *cancelUncalledDefaulterCfg) SetDefaults() { panic("SetDefaults ran on an ended context") }

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

	var n stageCounts
	var err error
	require.NotPanics(t, func() {
		_, err = Load[cancelUncalledDefaulterCfg](ended, stagedOptions[cancelUncalledDefaulterCfg](t, &n, nil)...)
	}, "the Defaulter is not called")
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, errShutdown)
	assert.Equal(t, stageCounts{}, n)

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
	assert.True(t, got.Defaulted, "the Defaulter ran")
	assert.Equal(t, 1, n.base)
	assert.Equal(t, 1, n.environ)
	assert.Equal(t, 1, n.override)
	assert.Positive(t, n.decodeHook)
	assert.Equal(t, 1, n.secrets)
	assert.Equal(t, 1, n.postLoad)
	assert.Equal(t, 1, n.validator)
}

// cancellingParser parses every file to port 1, counting the files, and
// ends the load's context on the first.
type cancellingParser struct {
	calls  *int
	cancel func()
}

func (p cancellingParser) Unmarshal([]byte) (map[string]any, error) {
	*p.calls++
	p.cancel()
	return map[string]any{"port": 1}, nil
}

func (p cancellingParser) Marshal(map[string]any) ([]byte, error) { return nil, nil }

// A load whose context ends while one config file parses parses no other:
// a parser is the caller's code.
func TestLoad_EndedContextParsesNoFurtherFile(t *testing.T) {
	dir := t.TempDir()
	var files []string
	for _, name := range []string{"a.cfg", "b.cfg"} {
		f := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(f, []byte("port"), 0o600))
		files = append(files, f)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	calls := 0
	parser := cancellingParser{calls: &calls, cancel: func() { cancel(errShutdown) }}
	_, err := Load[cancelCfg](ctx, WithFileParser(".cfg", parser), WithFiles(files...), WithSkipValidation())
	require.ErrorIs(t, err, errShutdown)
	assert.Equal(t, 1, calls)
}

// A load whose context ends while a decode hook runs calls no variable
// filter, which is the caller's code too.
func TestLoad_EndedContextCallsNoVariableFilter(t *testing.T) {
	filtered := 0
	opts := func(hook func()) []Option {
		return []Option{
			WithEnvPrefix("APP"),
			WithEnviron(func() []string { return []string{"APP_PORT=2", "APP_UNCLAIMED=1"} }),
			WithDecodeHook(func(_, _ reflect.Type, data any) (any, error) { hook(); return data, nil }),
			WithVariableFilter(func(string) bool { filtered++; return true }),
			WithSkipValidation(),
		}
	}
	got, err := Load[cancelCfg](t.Context(), opts(func() {})...)
	require.NoError(t, err)
	assert.Equal(t, 2, got.Port)
	require.Equal(t, 1, filtered, "a live load asks the filter about the unmatched variable")

	filtered = 0
	ctx, cancel := context.WithCancelCause(context.Background())
	_, err = Load[cancelCfg](ctx, opts(func() { cancel(errShutdown) })...)
	require.ErrorIs(t, err, errShutdown)
	assert.Zero(t, filtered)
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
	var abandoned *AbandonedApplyError
	require.ErrorAs(t, err, &abandoned)
	assert.Equal(t, []string{"first"}, abandoned.Applied, "the applier that applied it is named for rollback")
	assert.ErrorContains(t, err, "after appliers first applied it")

	type twoKeys struct {
		Port int    `koanf:"port"`
		Host string `koanf:"host"`
	}
	midway, cancel = context.WithCancelCause(context.Background())
	later := 0
	r2, err := NewReloaderFunc(midway, func(context.Context) (*twoKeys, error) { return &twoKeys{Port: 1, Host: "h"}, nil },
		WithApplier(Applier[twoKeys]{Name: "ports", Prefixes: []string{"port"}, ApplyInitial: true,
			Apply: func(context.Context, ApplyEvent[twoKeys]) (ApplyResult, error) {
				cancel(errShutdown)
				return ApplyResult{}, nil
			}}),
		WithApplier(Applier[twoKeys]{Name: "hosts", Prefixes: []string{"host"}, ApplyInitial: true,
			Apply: func(context.Context, ApplyEvent[twoKeys]) (ApplyResult, error) {
				later++
				return ApplyResult{}, nil
			}}))
	require.ErrorIs(t, err, errShutdown)
	assert.Nil(t, r2)
	assert.Zero(t, later, "no applier starts once the context ends")
	require.ErrorAs(t, err, &abandoned)
	assert.Equal(t, []string{"ports"}, abandoned.Applied)

	r, err = NewReloaderFunc(t.Context(), loader, applier)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(context.Background()) })
	assert.Equal(t, uint64(1), r.Snapshot().Generation)
	assert.Equal(t, 1, r.Config().Port)
	assert.Equal(t, 1, applied)
	assert.Zero(t, r.Stats().Attempts, "the first load is not a reload")
}

// An ApplyInitial applier that fails as the context ends leaves the first
// load abandoned, not merely unapplied: the error wraps why the context
// ended and names the appliers that did apply it for rollback, beside the
// failures. Nothing was published, and neither part says otherwise.
func TestNewReloader_ApplierFailingAsTheContextEndsIsAbandoned(t *testing.T) {
	type keys struct {
		Port int    `koanf:"port"`
		Host string `koanf:"host"`
	}
	loader := func(context.Context) (*keys, error) { return &keys{Port: 1, Host: "h"}, nil }
	applier := func(name, prefix string, apply func() error) ReloadOption {
		return WithApplier(Applier[keys]{Name: name, Prefixes: []string{prefix}, ApplyInitial: true,
			Apply: func(context.Context, ApplyEvent[keys]) (ApplyResult, error) { return ApplyResult{}, apply() }})
	}
	for _, tc := range []struct {
		name    string
		applied string
		failed  string
		build   func(cancel context.CancelCauseFunc) []ReloadOption
	}{
		{"the last fails as it ends the context", "ports", "hosts", func(cancel context.CancelCauseFunc) []ReloadOption {
			return []ReloadOption{
				applier("ports", "port", func() error { return nil }),
				applier("hosts", "host", func() error { cancel(errShutdown); return errors.New("host sink closed") }),
			}
		}},
		{"an earlier one failed, the last ends the context", "hosts", "ports", func(cancel context.CancelCauseFunc) []ReloadOption {
			return []ReloadOption{
				applier("ports", "port", func() error { return errors.New("port sink closed") }),
				applier("hosts", "host", func() error { cancel(errShutdown); return nil }),
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			r, err := NewReloaderFunc(ctx, loader, tc.build(cancel)...)
			assert.Nil(t, r)
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorIs(t, err, errShutdown)
			assert.NotErrorIs(t, err, ErrReload)
			var abandoned *AbandonedApplyError
			require.ErrorAs(t, err, &abandoned)
			assert.Equal(t, []string{tc.applied}, abandoned.Applied)
			require.ErrorIs(t, err, ErrApply)
			var failed *ApplyError
			require.ErrorAs(t, err, &failed)
			require.Len(t, failed.Failures, 1)
			assert.Equal(t, tc.failed, failed.Failures[0].Applier)
			assert.True(t, failed.Initial)
			assert.Contains(t, err.Error(), "sink closed")
			assert.NotContains(t, err.Error(), "configuration published")
		})
	}
}
