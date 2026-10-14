//go:build unit

package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The config, its generation and its stats are published together: a failed
// reload records a failure against the config still live, and a success
// moves all three at once.
func TestSnapshot_ConfigGenerationAndStatsMoveTogether(t *testing.T) {
	path := writeYAML(t, "name: one\n")
	r, err := NewReloader[validatedCfg](t.Context(), []Option{WithRequiredFile(path)})
	require.NoError(t, err)

	initial := r.Snapshot()
	assert.Equal(t, uint64(1), initial.Generation)
	assert.Same(t, r.Config(), initial.Config)
	assert.Zero(t, initial.Stats.Attempts)

	require.NoError(t, os.WriteFile(path, []byte("port: 1\n"), 0o644))
	_, err = r.Reload(t.Context())
	require.ErrorIs(t, err, ErrValidation)
	failed := r.Snapshot()
	assert.Equal(t, uint64(1), failed.Generation, "a failed reload publishes no config")
	assert.Same(t, initial.Config, failed.Config)
	assert.Equal(t, uint64(1), failed.Stats.Failures)
	require.NotNil(t, failed.Stats.LastError)
	assert.Equal(t, KindValidation, failed.Stats.LastError.Kind)

	require.NoError(t, os.WriteFile(path, []byte("name: two\n"), 0o644))
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, r.Snapshot(), res.Snapshot)
	assert.Equal(t, uint64(2), res.Snapshot.Generation)
	assert.Equal(t, "two", res.Snapshot.Config.Name)
	assert.Nil(t, res.Snapshot.Stats.LastError)
	assert.Equal(t, uint64(2), res.Snapshot.Stats.Attempts)
	require.Len(t, res.Changes, 1)
	assert.Equal(t, "name", res.Changes[0].Key)
	assert.Equal(t, "one", res.Changes[0].Old.Text)
	assert.Equal(t, "two", res.Changes[0].New.Text)
}

// A reload whose sources did not change publishes a new generation with an
// empty, not nil, change list, so it encodes as [].
func TestReload_UnchangedSourcesReportNoChanges(t *testing.T) {
	r, err := NewReloader[reloaderCfg](t.Context(), []Option{WithSkipValidation()})
	require.NoError(t, err)
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.NotNil(t, res.Changes)
	assert.Empty(t, res.Changes)
	b, err := json.Marshal(res)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"changes":[]`)
}

func TestReloadStats_JSON(t *testing.T) {
	b, err := json.Marshal(ReloadStats{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"attempts":0,"successes":0,"failures":0}`, string(b), "unset times and errors are omitted")

	path := writeYAML(t, "name: one\n")
	r, err := NewReloader[validatedCfg](t.Context(), []Option{WithRequiredFile(path)})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("port: 1\n"), 0o644))
	_, err = r.Reload(t.Context())
	require.Error(t, err)

	b, err = json.Marshal(r.Snapshot())
	require.NoError(t, err)
	var got struct {
		Generation uint64 `json:"generation"`
		Stats      struct {
			Failures    uint64            `json:"failures"`
			LastTrigger string            `json:"last_trigger"`
			LastFailure string            `json:"last_failure"`
			LastSuccess *string           `json:"last_success"`
			LastError   map[string]string `json:"last_error"`
		} `json:"stats"`
	}
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, uint64(1), got.Generation)
	assert.Equal(t, uint64(1), got.Stats.Failures)
	assert.Equal(t, TriggerManual, got.Stats.LastTrigger)
	assert.NotEmpty(t, got.Stats.LastFailure)
	assert.Nil(t, got.Stats.LastSuccess)
	assert.Equal(t, KindValidation, got.Stats.LastError["kind"])
	assert.NotEmpty(t, got.Stats.LastError["message"])
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &top))
	assert.ElementsMatch(t, []string{"generation", "stats"}, slices.Collect(maps.Keys(top)), "the config itself is not encoded")
}

// What Stats and Snapshot return is the caller's own: changing it changes no
// other reader's view.
func TestReloadStats_ReturnsCopies(t *testing.T) {
	path := writeYAML(t, "name: one\n")
	r, err := NewReloader[validatedCfg](t.Context(), []Option{WithRequiredFile(path)})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("port: 1\n"), 0o644))
	_, err = r.Reload(t.Context())
	require.Error(t, err)

	r.Stats().LastError.Message = "changed"
	r.Snapshot().Stats.LastError.Kind = "changed"
	s := r.Stats()
	assert.Equal(t, KindValidation, s.LastError.Kind)
	assert.NotEqual(t, "changed", s.LastError.Message)
}

func TestDiagnose(t *testing.T) {
	assert.Nil(t, Diagnose(nil))
	tests := []struct {
		err  error
		kind string
	}{
		{ErrValidation, KindValidation},
		{ErrFileRead, KindFileRead},
		{ErrFileParse, KindFileParse},
		{ErrSource, KindSource},
		{ErrSecretProvider, KindSecretProvider},
		{ErrPostLoad, KindPostLoad},
		{ErrSchema, KindSchema},
		{errors.New("other"), KindUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			err := fmt.Errorf("%w: %w", ErrReload, fmt.Errorf("loading: %w", tt.err))
			p := Diagnose(err)
			require.NotNil(t, p)
			assert.Equal(t, tt.kind, p.Kind)
			assert.Equal(t, err.Error(), p.Message)
		})
	}
}

// A success callback receives the trigger, both configs and the same result
// Reload returns.
func TestWithOnReloadSuccess_Event(t *testing.T) {
	path := writeYAML(t, "name: one\n")
	var got []ReloadEvent[validatedCfg]
	r, err := NewReloader[validatedCfg](t.Context(), []Option{WithRequiredFile(path)},
		WithOnReloadSuccess(func(ctx context.Context, ev ReloadEvent[validatedCfg]) {
			assert.NoError(t, ctx.Err())
			got = append(got, ev)
		}))
	require.NoError(t, err)
	before := r.Config()

	require.NoError(t, os.WriteFile(path, []byte("name: two\n"), 0o644))
	res, err := r.Reload(t.Context())
	require.NoError(t, err)

	require.Len(t, got, 1)
	ev := got[0]
	assert.Equal(t, TriggerManual, ev.Trigger)
	assert.Same(t, before, ev.Old)
	assert.Same(t, r.Config(), ev.New)
	assert.Equal(t, *res, ev.Result)
}

// Each callback gets its own copy of the changes: one that rewrites them
// changes neither the next callback's view nor Reload's result.
func TestWithOnReloadSuccess_CallbacksGetTheirOwnChanges(t *testing.T) {
	path := writeYAML(t, "name: one\n")
	var second []Change
	r, err := NewReloader[validatedCfg](t.Context(), []Option{WithRequiredFile(path)},
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[validatedCfg]) {
			for i := range ev.Result.Changes {
				ev.Result.Changes[i].Key = "rewritten"
			}
		}),
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[validatedCfg]) { second = ev.Result.Changes }))
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(path, []byte("name: two\n"), 0o644))
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	require.Len(t, res.Changes, 1)
	assert.Equal(t, "name", res.Changes[0].Key)
	require.Len(t, second, 1)
	assert.Equal(t, "name", second[0].Key)
}

// An automatic reload logs the generation it published and the keys it
// changed; a poll that changed nothing logs at debug, one that changed
// something at info.
func TestReloadLog_NamesGenerationAndChangedKeys(t *testing.T) {
	var port atomic.Int64
	port.Store(1)
	source, err := NewMapProvider(func(context.Context) (map[string]any, error) {
		return map[string]any{"port": port.Load()}, nil
	}, ".")
	require.NoError(t, err)
	var logs safeBuffer
	r, err := NewReloader[portCfg](t.Context(), []Option{WithBaseProvider(source, nil), WithSkipValidation()},
		WithReloadLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	require.NoError(t, err)

	poll := func() string {
		require.NoError(t, r.acquire(t.Context(), TriggerPoll, true))
		defer r.release()
		before := len(logs.String())
		r.reloadAndLog(t.Context(), TriggerPoll)
		return logs.String()[before:]
	}
	unchanged := poll()
	assert.Contains(t, unchanged, "level=DEBUG")
	assert.Contains(t, unchanged, "generation=2 changed_keys=[]")

	port.Store(2)
	changed := poll()
	assert.Contains(t, changed, "level=INFO")
	assert.Contains(t, changed, "generation=3 changed_keys=[port]")
}
