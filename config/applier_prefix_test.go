package config

import (
	"context"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/config/internal/deepcopy"
)

type prefixServer struct {
	Port int `koanf:"port"`
}

type prefixCfg struct {
	ByInt   map[int]string           `koanf:"by_int"`
	ByDur   map[time.Duration]string `koanf:"by_dur"`
	ByLevel map[slog.Level]string    `koanf:"by_level"`
	ByFloat map[float64]string       `koanf:"by_float"`
	Primary prefixServer             `koanf:"primary" alias:"main"`
}

// prefixSource hands a reloader fresh copies of a config the test edits.
type prefixSource struct {
	mu  sync.Mutex
	cfg prefixCfg
}

func (s *prefixSource) edit(fn func(*prefixCfg)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.cfg)
}

func (s *prefixSource) load(context.Context) (*prefixCfg, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := deepcopy.Value(reflect.ValueOf(s.cfg)).Interface().(prefixCfg)
	return &c, nil
}

// An applier's prefix is spelled as CanonicalKey spells a key, so it owns
// the changes Diff names however it was written: a map entry by the text of
// the key it decodes to, a field by its key, not a former name or another
// letter case.
func TestApplier_PrefixesAreSpelledAsDiffSpellsKeys(t *testing.T) {
	initial := prefixCfg{
		ByInt:   map[int]string{8: "a"},
		ByDur:   map[time.Duration]string{time.Minute: "a"},
		ByLevel: map[slog.Level]string{slog.LevelWarn: "a"},
		Primary: prefixServer{Port: 1},
	}
	for _, tt := range []struct {
		prefix string
		edit   func(*prefixCfg)
		key    string
	}{
		{`by_int["010"]`, func(c *prefixCfg) { c.ByInt[8] = "b" }, `by_int["8"]`},
		{`by_int["0x8"]`, func(c *prefixCfg) { c.ByInt[8] = "b" }, `by_int["8"]`},
		{`by_dur["60s"]`, func(c *prefixCfg) { c.ByDur[time.Minute] = "b" }, `by_dur["1m0s"]`},
		{`by_level["warn"]`, func(c *prefixCfg) { c.ByLevel[slog.LevelWarn] = "b" }, `by_level["WARN"]`},
		{"main.port", func(c *prefixCfg) { c.Primary.Port = 2 }, "primary.port"},
		{"PRIMARY.Port", func(c *prefixCfg) { c.Primary.Port = 2 }, "primary.port"},
		{"by_int[*]", func(c *prefixCfg) { c.ByInt[8] = "b" }, `by_int["8"]`},
	} {
		t.Run(tt.prefix, func(t *testing.T) {
			src := &prefixSource{cfg: deepcopy.Value(reflect.ValueOf(initial)).Interface().(prefixCfg)}
			var (
				mu     sync.Mutex
				events []ApplyEvent[prefixCfg]
			)
			apply := func(_ context.Context, ev ApplyEvent[prefixCfg]) (ApplyResult, error) {
				mu.Lock()
				defer mu.Unlock()
				events = append(events, ev)
				return ApplyResult{}, nil
			}
			r, err := NewReloaderFunc(t.Context(), src.load, WithReloadLogger(discardLogger()),
				WithApplier(Applier[prefixCfg]{Name: "a", Prefixes: []string{tt.prefix}, Apply: apply}))
			require.NoError(t, err)
			t.Cleanup(func() { _ = r.Stop(context.Background()) })
			mu.Lock()
			events = nil // the initial apply
			mu.Unlock()

			src.edit(tt.edit)
			_, err = r.Reload(t.Context())
			require.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			require.Len(t, events, 1, "the applier owns the change")
			assert.Equal(t, []string{tt.key}, changeKeys(events[0].Changes))
		})
	}
}

// A prefix naming a map entry by text its key type does not read back is
// refused, and two spellings of one key are one prefix.
func TestApplier_RefusesAPrefixWhoseKeyDoesNotReadBack(t *testing.T) {
	load := (&prefixSource{}).load
	apply := func(context.Context, ApplyEvent[prefixCfg]) (ApplyResult, error) { return ApplyResult{}, nil }
	build := func(appliers ...Applier[prefixCfg]) error {
		opts := []ReloadOption{WithReloadLogger(discardLogger())}
		for _, a := range appliers {
			a.Apply = apply
			opts = append(opts, WithApplier(a))
		}
		r, err := NewReloaderFunc(t.Context(), load, opts...)
		if err == nil {
			require.NoError(t, r.Stop(t.Context()))
		}
		return err
	}
	for _, tt := range []struct{ prefix, msg string }{
		{`by_int["x"]`, "by_int is keyed by int, which the entry's key does not decode into"},
		{`by_float["NaN"]`, "by_float is keyed by float64, which the entry's key does not decode into"},
		{`by_dur["soon"]`, "by_dur is keyed by time.Duration, which the entry's key does not decode into"},
	} {
		err := build(Applier[prefixCfg]{Name: "a", Prefixes: []string{tt.prefix}})
		require.ErrorIs(t, err, ErrKey, tt.prefix)
		assert.Contains(t, err.Error(), tt.msg, tt.prefix)
	}

	err := build(Applier[prefixCfg]{Name: "a", Prefixes: []string{`by_int["010"]`, `by_int["8"]`}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `lists by_int["8"] twice`)

	err = build(Applier[prefixCfg]{Name: "a", Prefixes: []string{"main"}}, Applier[prefixCfg]{Name: "b", Prefixes: []string{"primary"}})
	require.Error(t, err, "two appliers own one key, however each spells it")
}
