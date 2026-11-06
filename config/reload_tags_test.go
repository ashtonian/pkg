package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type reloadSection struct {
	Port int    `koanf:"port" reload:"restart"`
	Mode string `koanf:"mode" reload:"rebuild"`
	Rate int    `koanf:"rate"`
}

type reloadConfig struct {
	Section reloadSection `koanf:"section" reload:"live"`
	Name    string        `koanf:"name"`
}

func stopReloader[T any](t *testing.T, r *Reloader[T]) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, r.Stop(ctx))
	})
}

func TestReloadTags_MetadataAndRouting(t *testing.T) {
	for key, class := range map[string]string{
		"section.port": ReloadRestart, "section.mode": "rebuild", "section.rate": ReloadLive, "name": ReloadRestart,
	} {
		f, err := FieldFor[reloadConfig](key)
		require.NoError(t, err)
		assert.Equal(t, class, f.Reload, key)
	}
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller policy", true: "schema policy"}[enabled], func(t *testing.T) {
			value := reloadConfig{Section: reloadSection{Port: 80, Mode: "one", Rate: 1}}
			var events []ApplyEvent[reloadConfig]
			opts := []ReloadOption{WithApplier(Applier[reloadConfig]{Name: "section", Prefixes: []string{"section"}, ApplyInitial: true,
				Apply: func(_ context.Context, ev ApplyEvent[reloadConfig]) (ApplyResult, error) {
					events = append(events, ev)
					return ApplyResult{}, nil
				}})}
			if enabled {
				opts = append(opts, WithReloadTagChecks())
			}
			r, err := NewReloaderFunc(t.Context(), func(context.Context) (*reloadConfig, error) { copy := value; return &copy, nil }, opts...)
			require.NoError(t, err)
			stopReloader(t, r)
			require.Len(t, events, 1)
			if enabled {
				assert.NotContains(t, keysOf(events[0].Changes), "section.port")
			}
			value.Section = reloadSection{Port: 81, Mode: "two", Rate: 2}
			res, err := r.Reload(t.Context())
			require.NoError(t, err)
			require.Len(t, events, 2)
			assert.Equal(t, value, *res.Snapshot.Config)
			if enabled {
				assert.Equal(t, []string{"section.mode", "section.rate"}, keysOf(events[1].Changes))
				assert.Equal(t, []string{"section.port"}, keysOf(res.Snapshot.PendingRestart))
				assert.Equal(t, []string{"section.port"}, keysOf(RestartChanges(res.Changes)))
			} else {
				assert.Equal(t, []string{"section.port", "section.mode", "section.rate"}, keysOf(events[1].Changes))
				assert.Empty(t, res.Snapshot.PendingRestart)
			}
		})
	}
}

func TestReloadTags_UntaggedSchemaKeepsPrefixRouting(t *testing.T) {
	type cfg struct {
		Count int `koanf:"count"`
	}
	value, calls := 1, 0
	r, err := NewReloaderFunc(t.Context(), func(context.Context) (*cfg, error) { return &cfg{Count: value}, nil }, WithReloadTagChecks(),
		WithApplier(Applier[cfg]{Name: "count", Prefixes: []string{"count"}, Apply: func(context.Context, ApplyEvent[cfg]) (ApplyResult, error) { calls++; return ApplyResult{}, nil }}))
	require.NoError(t, err)
	stopReloader(t, r)
	value++
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Empty(t, res.Snapshot.PendingRestart)
}

func TestReloadTags_RestartWithoutAppliers(t *testing.T) {
	type cfg struct {
		Count int `koanf:"count" reload:"restart"`
	}
	value := 1
	r, err := NewReloaderFunc(t.Context(), func(context.Context) (*cfg, error) { return &cfg{Count: value}, nil }, WithReloadTags())
	require.NoError(t, err)
	stopReloader(t, r)
	value++
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"count"}, keysOf(res.Snapshot.PendingRestart))
}

func TestReloadTags_CollectionInheritanceAndSharedType(t *testing.T) {
	type plain struct {
		Rate int `koanf:"rate"`
	}
	type cfg struct {
		Live map[string]plain `koanf:"live" reload:"live"`
		Boot []plain          `koanf:"boot"`
	}
	for key, class := range map[string]string{`live["x"].rate`: ReloadLive, "live[*].rate": ReloadLive, "boot[2].rate": ReloadRestart} {
		f, err := FieldFor[cfg](key)
		require.NoError(t, err)
		assert.Equal(t, class, f.Reload)
	}
	type shared struct {
		Rate int `koanf:"rate" reload:"live"`
	}
	type invalid struct {
		Left  shared `koanf:"left"`
		Right shared `koanf:"right"`
	}
	_, err := FieldsChecked[invalid]()
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "left")
	assert.Contains(t, err.Error(), "right")
}

func TestReloadTags_OwnershipChecks(t *testing.T) {
	type item struct {
		Rate int `koanf:"rate"`
	}
	type cfg struct {
		Items map[string]item `koanf:"items" reload:"live"`
		Boot  int             `koanf:"boot" reload:"restart"`
	}
	for _, tt := range []struct {
		name     string
		prefixes [][]string
		want     string
	}{
		{"unowned", nil, "items[*].rate"},
		{"literal only", [][]string{{`items["one"]`}}, "items[*].rate"},
		{"restart only", [][]string{{"items"}, {"boot"}}, "owns no live key"},
		{"shadowed", [][]string{{"items"}, {"items[*].rate"}}, "owns no live key"},
		{"broad", [][]string{{"items"}}, ""},
		{"wildcard and literal", [][]string{{"items[*].rate"}, {`items["one"].rate`}}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := []ReloadOption{WithReloadTagChecks()}
			for i, prefixes := range tt.prefixes {
				opts = append(opts, WithApplier(Applier[cfg]{Name: string(rune('a' + i)), Prefixes: prefixes,
					Apply: func(context.Context, ApplyEvent[cfg]) (ApplyResult, error) { return ApplyResult{}, nil }}))
			}
			loaded := false
			r, err := NewReloaderFunc(t.Context(), func(context.Context) (*cfg, error) { loaded = true; return &cfg{}, nil }, opts...)
			if tt.want != "" {
				require.ErrorIs(t, err, ErrSchema)
				assert.Contains(t, err.Error(), tt.want)
				assert.False(t, loaded)
				return
			}
			require.NoError(t, err)
			stopReloader(t, r)
		})
	}
}

func TestReloadTags_MixedStructuralChangeWaitsForRestart(t *testing.T) {
	type cfg struct {
		Section *reloadSection `koanf:"section" reload:"live"`
	}
	value := cfg{}
	calls := 0
	r, err := NewReloaderFunc(t.Context(), func(context.Context) (*cfg, error) { copy := value; return &copy, nil }, WithReloadTagChecks(),
		WithApplier(Applier[cfg]{Name: "section", Prefixes: []string{"section"}, Apply: func(context.Context, ApplyEvent[cfg]) (ApplyResult, error) { calls++; return ApplyResult{}, nil }}))
	require.NoError(t, err)
	stopReloader(t, r)
	value.Section = &reloadSection{Port: 80, Mode: "on", Rate: 2}
	result, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Zero(t, calls)
	assert.ElementsMatch(t, []string{"section", "section.port", "section.mode", "section.rate"}, keysOf(result.Snapshot.PendingRestart))
	value.Section = nil
	result, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Empty(t, result.Snapshot.PendingRestart)
}

func TestReloadTags_InlineInheritanceAndValidation(t *testing.T) {
	type inner struct {
		Live    int `koanf:"live" reload:"live"`
		Inherit int `koanf:"inherit"`
	}
	type cfg struct {
		Inline inner `koanf:",squash" reload:"restart"`
	}
	f, err := FieldFor[cfg]("live")
	require.NoError(t, err)
	assert.Equal(t, ReloadLive, f.Reload)
	f, err = FieldFor[cfg]("inherit")
	require.NoError(t, err)
	assert.Equal(t, ReloadRestart, f.Reload)
	type wrapper struct {
		Inline inner `koanf:",squash"`
	}
	type reused struct {
		Left  wrapper `koanf:"left"`
		Right wrapper `koanf:"right"`
	}
	_, err = FieldsChecked[reused]()
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "left")
	assert.Contains(t, err.Error(), "right")
	_, err = FieldsChecked[struct {
		Value int `koanf:"value" reload:""`
	}]()
	require.ErrorIs(t, err, ErrSchema)
}

func TestReloadTags_FixedArrayOwnership(t *testing.T) {
	type cfg struct {
		Values [2]int `koanf:"values" reload:"live"`
	}
	opts := []ReloadOption{WithReloadTagChecks()}
	for _, key := range []string{"values[0]", "values[1]"} {
		opts = append(opts, WithApplier(Applier[cfg]{Name: key, Prefixes: []string{key}, Apply: func(context.Context, ApplyEvent[cfg]) (ApplyResult, error) { return ApplyResult{}, nil }}))
	}
	r, err := NewReloaderFunc(t.Context(), func(context.Context) (*cfg, error) { return &cfg{}, nil }, opts...)
	require.NoError(t, err)
	stopReloader(t, r)
}

func TestReloadTags_EmptyObjectHasLivePresence(t *testing.T) {
	type cfg struct {
		Switch *struct{} `koanf:"switch" reload:"live"`
	}
	value := cfg{}
	var keys []string
	r, err := NewReloaderFunc(t.Context(), func(context.Context) (*cfg, error) { copy := value; return &copy, nil }, WithReloadTagChecks(),
		WithApplier(Applier[cfg]{Name: "switch", Prefixes: []string{"switch"}, Apply: func(_ context.Context, ev ApplyEvent[cfg]) (ApplyResult, error) {
			keys = keysOf(ev.Changes)
			return ApplyResult{}, nil
		}}))
	require.NoError(t, err)
	stopReloader(t, r)
	value.Switch = &struct{}{}
	_, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"switch"}, keys)
}

func TestReloadTags_RecursiveObjectHasLivePresence(t *testing.T) {
	type node struct {
		Next *node `koanf:"next"`
	}
	type cfg struct {
		Root *node `koanf:"root" reload:"live"`
	}
	value := cfg{Root: &node{}}
	var keys []string
	r, err := NewReloaderFunc(t.Context(), func(context.Context) (*cfg, error) { copy := value; return &copy, nil }, WithReloadTagChecks(),
		WithApplier(Applier[cfg]{Name: "root", Prefixes: []string{"root"}, Apply: func(_ context.Context, ev ApplyEvent[cfg]) (ApplyResult, error) {
			keys = keysOf(ev.Changes)
			return ApplyResult{}, nil
		}}))
	require.NoError(t, err)
	stopReloader(t, r)
	value.Root = &node{Next: &node{}}
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Contains(t, keys, "root.next")
	assert.Empty(t, res.Snapshot.PendingRestart)
}
