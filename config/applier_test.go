//go:build unit

package config

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type appLog struct {
	Level  string `koanf:"level"`
	Format string `koanf:"format"`
}

type appDB struct {
	Host     string `koanf:"host"`
	MaxConns int    `koanf:"max_conns"`
	Password string `koanf:"password"`
}

type appFeature struct {
	Enabled bool   `koanf:"enabled"`
	Gate    string `koanf:"gate"`
}

type appTLS struct {
	Cert string `koanf:"cert"`
}

type appCfg struct {
	Name     string                `koanf:"name"`
	Log      appLog                `koanf:"log"`
	DB       appDB                 `koanf:"db"`
	Features map[string]appFeature `koanf:"features"`
	TLS      *appTLS               `koanf:"tls"`
	Tokens   map[string]string     `koanf:"tokens"`
	Extra    any                   `koanf:"extra"`
}

// appSource hands out a fresh copy of a config the test edits between
// reloads, as a loader would.
type appSource struct {
	mu  sync.Mutex
	cfg appCfg
}

func newAppSource() *appSource {
	return &appSource{cfg: appCfg{
		Name:     "one",
		Log:      appLog{Level: "info", Format: "json"},
		DB:       appDB{Host: "a", MaxConns: 1, Password: "p1"},
		Features: map[string]appFeature{"audit": {Enabled: true, Gate: "open"}},
	}}
}

func (s *appSource) edit(fn func(*appCfg)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.cfg)
}

func (s *appSource) load(context.Context) (*appCfg, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	c.Features = maps.Clone(s.cfg.Features)
	if s.cfg.TLS != nil {
		tls := *s.cfg.TLS
		c.TLS = &tls
	}
	return &c, nil
}

// recorder is an applier that records its events and answers with result.
type recorder struct {
	mu     sync.Mutex
	events []ApplyEvent[appCfg]
	result func(ev ApplyEvent[appCfg]) (ApplyResult, error)
}

func (r *recorder) apply(_ context.Context, ev ApplyEvent[appCfg]) (ApplyResult, error) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	result := r.result
	r.mu.Unlock()
	if result != nil {
		return result(ev)
	}
	return ApplyResult{}, nil
}

func (r *recorder) calls() []ApplyEvent[appCfg] {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ApplyEvent[appCfg](nil), r.events...)
}

func (r *recorder) answer(fn func(ev ApplyEvent[appCfg]) (ApplyResult, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = fn
}

func (r *recorder) applier(name string, prefixes ...string) ReloadOption {
	return WithApplier(Applier[appCfg]{Name: name, Prefixes: prefixes, Apply: r.apply})
}

func keysOf(changes []Change) []string {
	out := make([]string, len(changes))
	for i, c := range changes {
		out[i] = c.Key
	}
	return out
}

func newAppReloader(t *testing.T, src *appSource, opts ...ReloadOption) *Reloader[appCfg] {
	t.Helper()
	r, err := NewReloaderFunc(t.Context(), src.load, append(opts, WithReloadLogger(discardLogger()))...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(context.Background()) })
	return r
}

// The most specific prefix owns a key: the longest, then the one naming the
// most literal entries.
func TestApplier_OwnershipIsMostSpecific(t *testing.T) {
	type entry struct {
		B int `koanf:"b"`
		C int `koanf:"c"`
	}
	type cfg struct {
		A map[string]entry `koanf:"a"`
	}
	noop := func(context.Context, ApplyEvent[cfg]) (ApplyResult, error) { return ApplyResult{}, nil }
	ro := buildReloadOptions([]ReloadOption{
		WithApplier(Applier[cfg]{Name: "top", Prefixes: []string{"a"}, Apply: noop}),
		WithApplier(Applier[cfg]{Name: "entry", Prefixes: []string{`a["x"]`}, Apply: noop}),
		WithApplier(Applier[cfg]{Name: "any-b", Prefixes: []string{"a[*].b"}, Apply: noop}),
		WithApplier(Applier[cfg]{Name: "x-c", Prefixes: []string{`a["x"].c`}, Apply: noop}),
	})
	root, err := rootSchema(reflect.TypeFor[cfg]())
	require.NoError(t, err)
	app, err := newApplication(root, ro.appliers)
	require.NoError(t, err)
	owner := func(key string) string {
		p, err := parseKeyPath(key)
		require.NoError(t, err)
		if i := app.owners.of(p); i >= 0 {
			return app.appliers[i].hook.name
		}
		return ""
	}
	assert.Equal(t, "top", owner("a"))
	assert.Equal(t, "entry", owner(`a["x"]`))
	assert.Equal(t, "top", owner(`a["y"]`))
	assert.Equal(t, "any-b", owner(`a["x"].b`), "longer beats more literal")
	assert.Equal(t, "x-c", owner(`a["x"].c`), "as long, more literal")
	assert.Equal(t, "any-b", owner(`a["y"].b`))
	assert.Equal(t, "top", owner(`a["y"].c`))
}

func TestApplier_RefusesMisdeclaredAppliers(t *testing.T) {
	noop := func(context.Context, ApplyEvent[appCfg]) (ApplyResult, error) { return ApplyResult{}, nil }
	type other struct{}
	for _, tt := range []struct {
		name string
		opts []ReloadOption
		want string
	}{
		{"no name", []ReloadOption{WithApplier(Applier[appCfg]{Prefixes: []string{"log"}, Apply: noop})}, "has no Name"},
		{"twice", []ReloadOption{
			WithApplier(Applier[appCfg]{Name: "a", Prefixes: []string{"log"}, Apply: noop}),
			WithApplier(Applier[appCfg]{Name: "a", Prefixes: []string{"db"}, Apply: noop}),
		}, "registered twice"},
		{"no apply", []ReloadOption{WithApplier(Applier[appCfg]{Name: "a", Prefixes: []string{"log"}})}, "has no Apply"},
		{"no prefix", []ReloadOption{WithApplier(Applier[appCfg]{Name: "a", Apply: noop})}, "owns no key"},
		{"unknown field", []ReloadOption{WithApplier(Applier[appCfg]{Name: "a", Prefixes: []string{"db.port"}, Apply: noop})}, `has no field "port"`},
		{"beneath a value", []ReloadOption{WithApplier(Applier[appCfg]{Name: "a", Prefixes: []string{"name.x"}, Apply: noop})}, "no keys beneath it"},
		{"beneath an interface", []ReloadOption{WithApplier(Applier[appCfg]{Name: "a", Prefixes: []string{"extra.x"}, Apply: noop})}, "reaches beneath extra"},
		{"beneath a sensitive map", []ReloadOption{WithApplier(Applier[appCfg]{Name: "a", Prefixes: []string{`tokens["a"]`}, Apply: noop})}, "sensitive and compared whole"},
		{"listed twice", []ReloadOption{WithApplier(Applier[appCfg]{Name: "a", Prefixes: []string{"log", "log"}, Apply: noop})}, "lists log twice"},
		{"equally specific", []ReloadOption{
			WithApplier(Applier[appCfg]{Name: "a", Prefixes: []string{"features[*].gate"}, Apply: noop}),
			WithApplier(Applier[appCfg]{Name: "b", Prefixes: []string{"features[*].gate"}, Apply: noop}),
		}, "equally specific"},
		{"another type", []ReloadOption{WithApplier(Applier[other]{Name: "a", Prefixes: []string{"x"}, Apply: func(context.Context, ApplyEvent[other]) (ApplyResult, error) {
			return ApplyResult{}, nil
		}})}, "WithApplier \"a\" is for"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewReloaderFunc(t.Context(), newAppSource().load, tt.opts...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// ApplyInitial applies the first load once, before the constructor returns;
// an applier without it is not called until a reload.
func TestApplier_InitialOptIn(t *testing.T) {
	initial, later := &recorder{}, &recorder{}
	newAppReloader(t, newAppSource(),
		WithApplier(Applier[appCfg]{Name: "log", Prefixes: []string{"log"}, ApplyInitial: true, Apply: initial.apply}),
		later.applier("db", "db"))

	calls := initial.calls()
	require.Len(t, calls, 1)
	ev := calls[0]
	assert.Equal(t, TriggerInitial, ev.Trigger)
	assert.Equal(t, uint64(1), ev.Generation)
	assert.Nil(t, ev.Previous)
	assert.Equal(t, []string{"log", "log.level", "log.format"}, keysOf(ev.Changes))
	assert.False(t, ev.Changes[1].Old.Present)
	assert.Equal(t, "info", ev.Changes[1].New.Text)
	assert.Empty(t, later.calls())
}

func TestApplier_InitialFailureFailsConstruction(t *testing.T) {
	for name, answer := range map[string]func(ApplyEvent[appCfg]) (ApplyResult, error){
		"error": func(ApplyEvent[appCfg]) (ApplyResult, error) { return ApplyResult{}, errors.New("sink not ready") },
		"restart": func(ApplyEvent[appCfg]) (ApplyResult, error) {
			return ApplyResult{RestartKeys: []string{"log.level"}}, nil
		},
		"panic": func(ApplyEvent[appCfg]) (ApplyResult, error) { panic("boom") },
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{result: answer}
			_, err := NewReloaderFunc(t.Context(), newAppSource().load,
				WithApplier(Applier[appCfg]{Name: "log", Prefixes: []string{"log"}, ApplyInitial: true, Apply: rec.apply}))
			require.ErrorIs(t, err, ErrApply)
			assert.Contains(t, err.Error(), "log:")
		})
	}
}

// An applier runs after its config is published, once per reload however
// many of its keys changed.
func TestApplier_RunsOnceAfterPublication(t *testing.T) {
	src := newAppSource()
	var r *Reloader[appCfg]
	var sawPublished bool
	rec := &recorder{result: func(ev ApplyEvent[appCfg]) (ApplyResult, error) {
		sawPublished = r.Config() == ev.Desired && r.Snapshot().Generation == ev.Generation
		return ApplyResult{}, nil
	}}
	r = newAppReloader(t, src, rec.applier("log", "log"))
	before := r.Config()

	src.edit(func(c *appCfg) { c.Log = appLog{Level: "debug", Format: "text"} })
	res, err := r.Reload(t.Context())
	require.NoError(t, err)

	calls := rec.calls()
	require.Len(t, calls, 1)
	assert.True(t, sawPublished, "Config and the generation are the published ones while it runs")
	assert.Equal(t, TriggerManual, calls[0].Trigger)
	assert.Same(t, before, calls[0].Previous)
	assert.False(t, calls[0].Retry)
	assert.Equal(t, []string{"log.level", "log.format"}, keysOf(calls[0].Changes))
	assert.Equal(t, []ApplyOutcome{{Applier: "log", Applied: []string{"log.level", "log.format"}}}, res.Applied)

	_, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Len(t, rec.calls(), 1, "an applier that owes nothing is not called")
}

// A key no applier owns waits for a restart until it returns to its first
// value.
func TestApplier_UnownedKeysWaitForRestartAgainstTheFirstLoad(t *testing.T) {
	src := newAppSource()
	r := newAppReloader(t, src, (&recorder{}).applier("log", "log"))

	src.edit(func(c *appCfg) { c.Name = "two" })
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"name"}, keysOf(res.Snapshot.PendingRestart))
	assert.Empty(t, res.Applied)

	src.edit(func(c *appCfg) { c.Name = "three" })
	res, err = r.Reload(t.Context())
	require.NoError(t, err)
	require.Len(t, res.Snapshot.PendingRestart, 1)
	assert.Equal(t, "one", res.Snapshot.PendingRestart[0].Old.Text, "measured from the first load")

	src.edit(func(c *appCfg) { c.Name = "one" })
	res, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Empty(t, res.Snapshot.PendingRestart, "reverted")
}

// A restart key stays pending, is offered again on every config, and
// clears when the config returns to what was acknowledged.
func TestApplier_PartialDeferral(t *testing.T) {
	src := newAppSource()
	rec := &recorder{result: func(ev ApplyEvent[appCfg]) (ApplyResult, error) {
		for _, c := range ev.Changes {
			if c.Key == "db.host" {
				return ApplyResult{RestartKeys: []string{"db.host"}}, nil
			}
		}
		return ApplyResult{}, nil
	}}
	r := newAppReloader(t, src, rec.applier("db", "db"))

	src.edit(func(c *appCfg) { c.DB.Host, c.DB.MaxConns = "b", 2 })
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []ApplyOutcome{{Applier: "db", Applied: []string{"db.max_conns"}, Deferred: []string{"db.host"}}}, res.Applied)
	assert.Equal(t, []string{"db.host"}, keysOf(res.Snapshot.PendingRestart))
	require.Len(t, res.Snapshot.Appliers, 1)
	assert.Equal(t, []string{"db.host"}, keysOf(res.Snapshot.Appliers[0].Pending))
	assert.False(t, res.Snapshot.Appliers[0].Uncertain)

	_, err = r.Reload(t.Context())
	require.NoError(t, err)
	calls := rec.calls()
	require.Len(t, calls, 2)
	assert.Equal(t, []string{"db.host"}, keysOf(calls[1].Changes), "offered again")

	src.edit(func(c *appCfg) { c.DB.Host = "a" })
	res, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Len(t, rec.calls(), 2, "the revert leaves nothing owed")
	assert.Empty(t, res.Snapshot.PendingRestart)
	assert.Empty(t, res.Snapshot.Appliers[0].Pending)
}

// A failed applier acknowledges nothing and is uncertain: it is called on
// every later config, an unchanged one and a revert included, until it
// applies everything.
func TestApplier_FailureIsRetriedUntilReconciled(t *testing.T) {
	src := newAppSource()
	rec := &recorder{result: func(ApplyEvent[appCfg]) (ApplyResult, error) { return ApplyResult{}, errors.New("sink refused") }}
	r := newAppReloader(t, src, rec.applier("log", "log"))

	src.edit(func(c *appCfg) { c.Log.Level = "debug" })
	res, err := r.Reload(t.Context())
	require.ErrorIs(t, err, ErrApply)
	require.NotErrorIs(t, err, ErrReload)
	var applyErr *ApplyError
	require.ErrorAs(t, err, &applyErr)
	assert.Equal(t, "log", applyErr.Failures[0].Applier)
	require.NotNil(t, res, "the config was published")
	assert.Equal(t, "debug", r.Config().Log.Level)
	assert.Equal(t, uint64(2), res.Snapshot.Generation)
	assert.Nil(t, res.Snapshot.Stats.LastError, "the load succeeded")
	status := res.Snapshot.Appliers[0]
	assert.True(t, status.Uncertain)
	assert.Equal(t, KindApply, status.LastError.Kind)
	assert.Equal(t, uint64(1), status.Failures)
	assert.Equal(t, []string{"log.level"}, keysOf(status.Pending))
	assert.Equal(t, KindApply, Diagnose(err).Kind)

	// Reverted to what it acknowledged: nothing differs, but it is still
	// uncertain, so it is called to reconcile — and fails again.
	src.edit(func(c *appCfg) { c.Log.Level = "info" })
	_, err = r.Reload(t.Context())
	require.ErrorIs(t, err, ErrApply)
	calls := rec.calls()
	require.Len(t, calls, 2)
	assert.True(t, calls[1].Retry)
	assert.Empty(t, calls[1].Changes)

	rec.answer(nil)
	res, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Len(t, rec.calls(), 3)
	assert.False(t, res.Snapshot.Appliers[0].Uncertain)
	assert.Nil(t, res.Snapshot.Appliers[0].LastError)
}

// An uncertain applier that answers with restart keys stays uncertain: a
// refusal cannot prove an earlier failure's effects undone.
func TestApplier_UncertainStaysUncertainWithRestartKeys(t *testing.T) {
	src := newAppSource()
	rec := &recorder{result: func(ApplyEvent[appCfg]) (ApplyResult, error) { panic("boom") }}
	r := newAppReloader(t, src, rec.applier("db", "db"))

	src.edit(func(c *appCfg) { c.DB.Host = "b" })
	_, err := r.Reload(t.Context())
	require.ErrorIs(t, err, ErrApply)
	assert.Contains(t, err.Error(), "panicked: boom")

	rec.answer(func(ApplyEvent[appCfg]) (ApplyResult, error) {
		return ApplyResult{RestartKeys: []string{"db.host"}}, nil
	})
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.True(t, res.Snapshot.Appliers[0].Uncertain)
	assert.NotNil(t, res.Snapshot.Appliers[0].LastError)
	assert.Equal(t, []string{"db.host"}, keysOf(res.Snapshot.PendingRestart))

	rec.answer(func(ApplyEvent[appCfg]) (ApplyResult, error) {
		return ApplyResult{RestartKeys: []string{"db.port"}}, nil
	})
	_, err = r.Reload(t.Context())
	require.ErrorIs(t, err, ErrApply, "a restart key that is not one of its changes is a failure")
}

// A structure that appears or disappears applies live only when one applier
// owns it and everything beneath it.
func TestApplier_StructuresNeedOneOwner(t *testing.T) {
	t.Run("one owner of the map applies an added entry", func(t *testing.T) {
		src := newAppSource()
		rec := &recorder{}
		r := newAppReloader(t, src, rec.applier("features", "features"))
		src.edit(func(c *appCfg) { c.Features["export"] = appFeature{Gate: "closed"} })
		res, err := r.Reload(t.Context())
		require.NoError(t, err)
		require.Len(t, rec.calls(), 1)
		assert.Equal(t, []string{`features["export"]`, `features["export"].enabled`, `features["export"].gate`}, keysOf(rec.calls()[0].Changes))
		assert.Empty(t, res.Snapshot.PendingRestart)
	})
	t.Run("an owner of one field cannot add an entry", func(t *testing.T) {
		src := newAppSource()
		rec := &recorder{}
		r := newAppReloader(t, src, rec.applier("gates", "features[*].gate"))
		src.edit(func(c *appCfg) { c.Features["export"] = appFeature{Gate: "closed"} })
		res, err := r.Reload(t.Context())
		require.NoError(t, err)
		assert.Empty(t, rec.calls())
		assert.Equal(t, []string{`features["export"]`, `features["export"].enabled`, `features["export"].gate`}, keysOf(res.Snapshot.PendingRestart))

		src.edit(func(c *appCfg) { c.Features["audit"] = appFeature{Enabled: true, Gate: "closed"} })
		_, err = r.Reload(t.Context())
		require.NoError(t, err)
		require.Len(t, rec.calls(), 1, "an existing entry's gate applies live")
		assert.Equal(t, []string{`features["audit"].gate`}, keysOf(rec.calls()[0].Changes))
	})
	t.Run("two owners beneath an entry block it", func(t *testing.T) {
		src := newAppSource()
		whole, gates := &recorder{}, &recorder{}
		r := newAppReloader(t, src, whole.applier("features", "features"), gates.applier("gates", "features[*].gate"))
		src.edit(func(c *appCfg) { delete(c.Features, "audit") })
		res, err := r.Reload(t.Context())
		require.NoError(t, err)
		assert.Empty(t, whole.calls())
		assert.Empty(t, gates.calls())
		assert.Equal(t, []string{`features["audit"]`, `features["audit"].enabled`, `features["audit"].gate`}, keysOf(res.Snapshot.PendingRestart))
	})
	t.Run("a nil section turned on needs its owner", func(t *testing.T) {
		src := newAppSource()
		cert, tls := &recorder{}, &recorder{}
		r := newAppReloader(t, src, cert.applier("cert", "tls.cert"))
		src.edit(func(c *appCfg) { c.TLS = &appTLS{Cert: "c"} })
		res, err := r.Reload(t.Context())
		require.NoError(t, err)
		assert.Empty(t, cert.calls())
		assert.Equal(t, []string{"tls", "tls.cert"}, keysOf(res.Snapshot.PendingRestart))

		src2 := newAppSource()
		r2 := newAppReloader(t, src2, tls.applier("tls", "tls"))
		src2.edit(func(c *appCfg) { c.TLS = &appTLS{Cert: "c"} })
		res, err = r2.Reload(t.Context())
		require.NoError(t, err)
		require.Len(t, tls.calls(), 1)
		assert.Empty(t, res.Snapshot.PendingRestart)
	})
}

// Once a config is published, cancellation cannot unpublish it: appliers not
// yet called are reported and called on the next config.
func TestApplier_CancellationAfterPublication(t *testing.T) {
	src := newAppSource()
	ctx, cancel := context.WithCancel(t.Context())
	first := &recorder{result: func(ApplyEvent[appCfg]) (ApplyResult, error) {
		cancel()
		return ApplyResult{}, nil
	}}
	second := &recorder{}
	r := newAppReloader(t, src, first.applier("log", "log"), second.applier("db", "db"))

	src.edit(func(c *appCfg) { c.Log.Level, c.DB.MaxConns = "debug", 9 })
	res, err := r.Reload(ctx)
	require.ErrorIs(t, err, ErrApply)
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, res)
	assert.Equal(t, 9, r.Config().DB.MaxConns)
	assert.Empty(t, second.calls())
	require.Len(t, res.Applied, 2)
	assert.Equal(t, "db", res.Applied[1].Applier)
	assert.Contains(t, res.Applied[1].Error.Message, "not called")
	assert.Equal(t, uint64(0), res.Snapshot.Appliers[1].Invocations)
	assert.False(t, res.Snapshot.Appliers[1].Uncertain, "never called, so nothing of it happened")

	_, err = r.Reload(t.Context())
	require.NoError(t, err)
	require.Len(t, second.calls(), 1)
	assert.Equal(t, []string{"db.max_conns"}, keysOf(second.calls()[0].Changes))
}

// Stop waits for an applier in flight; once it returns, no applier runs.
func TestApplier_StopWaitsForAnApplierInFlight(t *testing.T) {
	src := newAppSource()
	entered, release := make(chan struct{}), make(chan struct{})
	rec := &recorder{result: func(ApplyEvent[appCfg]) (ApplyResult, error) {
		close(entered)
		<-release
		return ApplyResult{}, nil
	}}
	r := newAppReloader(t, src, rec.applier("log", "log"))
	src.edit(func(c *appCfg) { c.Log.Level = "debug" })
	done := make(chan error, 1)
	go func() {
		_, err := r.Reload(context.Background())
		done <- err
	}()
	<-entered

	short, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, r.Stop(short), context.DeadlineExceeded)
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, r.Stop(t.Context()))
}

// Success callbacks run after the appliers, with what they did.
func TestApplier_SuccessCallbacksSeeTheOutcomes(t *testing.T) {
	src := newAppSource()
	var outcomes []ApplyOutcome
	r := newAppReloader(t, src, (&recorder{}).applier("log", "log"),
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[appCfg]) { outcomes = ev.Result.Applied }))
	src.edit(func(c *appCfg) { c.Log.Level = "debug" })
	_, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []ApplyOutcome{{Applier: "log", Applied: []string{"log.level"}}}, outcomes)
}

func TestApplier_SnapshotJSON(t *testing.T) {
	src := newAppSource()
	rec := &recorder{result: func(ApplyEvent[appCfg]) (ApplyResult, error) {
		return ApplyResult{RestartKeys: []string{"db.password"}}, nil
	}}
	r := newAppReloader(t, src, rec.applier("db", "db"))
	src.edit(func(c *appCfg) { c.DB.Password = "SECRET_NEW" })
	_, err := r.Reload(t.Context())
	require.NoError(t, err)

	b, err := json.Marshal(r.Snapshot())
	require.NoError(t, err)
	var got struct {
		Appliers []struct {
			Name        string `json:"name"`
			Invocations uint64 `json:"invocations"`
			Pending     []struct {
				Key string `json:"key"`
			} `json:"pending"`
		} `json:"appliers"`
		PendingRestart []struct {
			Key string `json:"key"`
			New Value  `json:"new"`
		} `json:"pending_restart"`
	}
	require.NoError(t, json.Unmarshal(b, &got))
	require.Len(t, got.Appliers, 1)
	assert.Equal(t, "db", got.Appliers[0].Name)
	assert.Equal(t, uint64(1), got.Appliers[0].Invocations)
	require.Len(t, got.PendingRestart, 1)
	assert.Equal(t, "db.password", got.PendingRestart[0].Key)
	assert.True(t, got.PendingRestart[0].New.Redacted)
	assert.NotContains(t, string(b), "SECRET_NEW")
}

// A config that cannot be compared is refused before it is published.
func TestApplier_IncomparableConfigIsRefused(t *testing.T) {
	src := newAppSource()
	r := newAppReloader(t, src, (&recorder{}).applier("log", "log"))
	src.edit(func(c *appCfg) { c.Extra = func() {} })
	_, err := r.Reload(t.Context())
	require.ErrorIs(t, err, ErrReload)
	require.ErrorIs(t, err, ErrSchema)
	assert.Equal(t, uint64(1), r.Snapshot().Generation)

	_, err = NewReloaderFunc(t.Context(), src.load)
	require.ErrorIs(t, err, ErrSchema)
}

// Without appliers nothing is accounted: a key read from Config on every
// use is not waiting for a restart.
func TestApplier_NoAppliersNoAccounting(t *testing.T) {
	src := newAppSource()
	r := newAppReloader(t, src)
	src.edit(func(c *appCfg) { c.Name = "two" })
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Nil(t, res.Snapshot.PendingRestart)
	assert.Nil(t, res.Snapshot.Appliers)
}

// Readers see whole snapshots while reloads run their appliers.
func TestApplier_ConcurrentReaders(t *testing.T) {
	src := newAppSource()
	rec := &recorder{result: func(ev ApplyEvent[appCfg]) (ApplyResult, error) {
		if ev.Desired.DB.MaxConns%3 == 0 {
			return ApplyResult{}, errors.New("every third fails")
		}
		return ApplyResult{RestartKeys: keysOf(ev.Changes)[:len(ev.Changes)/2]}, nil
	}}
	r := newAppReloader(t, src, rec.applier("db", "db"))

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			snap := r.Snapshot()
			_, err := json.Marshal(snap)
			assert.NoError(t, err)
			if len(snap.Appliers) > 0 {
				snap.Appliers[0].Pending = nil // a reader's copy is its own
			}
			_ = r.Stats()
			_ = r.Config().DB.MaxConns
		}
	})
	for i := range 50 {
		src.edit(func(c *appCfg) { c.DB.MaxConns, c.DB.Host = i, string(rune('a'+i%5)) })
		_, err := r.Reload(t.Context())
		if err != nil {
			require.ErrorIs(t, err, ErrApply)
		}
	}
	close(done)
	wg.Wait()
}
