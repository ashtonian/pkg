package config

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type shadowCfg struct {
	LogLevel string            `koanf:"log_level" default:"info"`
	Workers  int               `koanf:"workers" default:"4"`
	Password string            `koanf:"password" sensitive:"true"`
	Tokens   map[string]string `koanf:"tokens" sensitive:"true"`
	DB       struct {
		Host string `koanf:"host"`
		Port int    `koanf:"port"`
	} `koanf:"db"`
}

// environment is a WithEnviron source a test changes between reloads.
type environment struct {
	mu   sync.Mutex
	vars []string
}

func (e *environment) set(vars ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.vars = vars
}

func (e *environment) environ() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.vars...)
}

// jsonLog captures a logger's records as decoded JSON objects.
type jsonLog struct{ buf bytes.Buffer }

func (l *jsonLog) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&l.buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (l *jsonLog) records(t *testing.T, level string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(l.buf.String()) {
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		if rec["level"] == level {
			out = append(out, rec)
		}
	}
	return out
}

func fileInput(path string) Source { return Source{Kind: SourceFile, Name: path} }

func envSource(name, prefix string) Source {
	return Source{Kind: SourceEnv, Name: name, Prefix: prefix}
}

func changedKeys(changes []Change) []string {
	keys := make([]string, len(changes))
	for i, ch := range changes {
		keys[i] = ch.Key
	}
	return keys
}

var withAndWithoutProvenance = []struct {
	name string
	opts []ReloadOption
}{
	{"without provenance", nil},
	{"with provenance", []ReloadOption{WithProvenance()}},
}

// The issue's repro: a deployment moved log_level into a file but left
// APP_LOG_LEVEL behind, so editing the file changes nothing. The reload
// reports it, the Warn log names both sources and no value, and an
// unshadowed edit stays a change.
func TestShadow_ReloadReportsAChangeAHigherSourceKeeps(t *testing.T) {
	for _, tt := range withAndWithoutProvenance {
		t.Run(tt.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.yaml")
			writeFile(t, file, "log_level: info\nworkers: 4\n")
			env := &environment{vars: []string{"APP_LOG_LEVEL=info"}}
			var logs jsonLog
			r, err := NewReloader[shadowCfg](t.Context(),
				[]Option{WithFile(file), WithEnvPrefix("APP"), WithEnviron(env.environ), WithLogger(logs.logger())}, tt.opts...)
			require.NoError(t, err)
			want := []Shadow{{Key: "log_level", Source: fileInput(file), By: envSource("APP_LOG_LEVEL", "APP")}}
			assert.Equal(t, want, r.Snapshot().Shadowed, "the initial load records what is shadowed")

			writeFile(t, file, "log_level: FAKE_FILE_LEVEL\nworkers: 8\n")
			res, err := r.Reload(t.Context())
			require.NoError(t, err)
			assert.Equal(t, uint64(2), res.Snapshot.Generation)
			assert.Equal(t, []string{"workers"}, changedKeys(res.Changes))
			assert.Equal(t, want, res.Shadowed)
			assert.Equal(t, want, res.Snapshot.Shadowed)
			assert.Equal(t, "info", r.Config().LogLevel)

			warns := logs.records(t, "WARN")
			require.Len(t, warns, 1)
			assert.Equal(t, "log_level", warns[0]["key"])
			assert.Equal(t, "file "+file, warns[0]["source"])
			assert.Equal(t, "environment APP_LOG_LEVEL", warns[0]["by"])
			assert.Equal(t, TriggerManual, warns[0]["trigger"])
			assert.NotContains(t, logs.buf.String(), "FAKE_FILE_LEVEL", "no log line quotes a value")

			res, err = r.Reload(t.Context())
			require.NoError(t, err)
			assert.Empty(t, res.Shadowed, "an input reported once is not reported again until it changes")
			assert.Equal(t, want, res.Snapshot.Shadowed, "the snapshot still lists it")
		})
	}
}

// A typed flag outranks a file's key for the life of the process: the
// flag's annotation names it, with and without provenance.
func TestShadow_FlagOutranksAFileKey(t *testing.T) {
	for _, tt := range withAndWithoutProvenance {
		t.Run(tt.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.yaml")
			writeFile(t, file, "log_level: info\n")
			flags := &originProvider{
				data:        map[string]any{"log_level": "warn"},
				annotations: map[string]Source{"log_level": {Kind: SourceFlag, Name: "--log-level"}},
			}
			r, err := NewReloader[shadowCfg](t.Context(), []Option{WithFile(file), WithOverrideProvider(flags, nil)}, tt.opts...)
			require.NoError(t, err)

			writeFile(t, file, "log_level: debug\n")
			res, err := r.Reload(t.Context())
			require.NoError(t, err)
			assert.Empty(t, res.Changes)
			assert.Equal(t, []Shadow{{Key: "log_level", Source: fileInput(file), By: Source{Kind: SourceFlag, Name: "--log-level"}}}, res.Shadowed)
		})
	}
}

// Of two files, the later wins: an edit to the earlier one's shared key is
// reported naming both.
func TestShadow_EarlierFileEditKeptByALaterFile(t *testing.T) {
	dir := t.TempDir()
	base, over := filepath.Join(dir, "base.yaml"), filepath.Join(dir, "over.yaml")
	writeFile(t, base, "workers: 4\n")
	writeFile(t, over, "workers: 5\n")
	r, err := NewReloader[shadowCfg](t.Context(), []Option{WithFiles(base, over)})
	require.NoError(t, err)

	writeFile(t, base, "workers: 40\n")
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Empty(t, res.Changes)
	assert.Equal(t, []Shadow{{Key: "workers", Source: fileInput(base), By: fileInput(over)}}, res.Shadowed)
}

// A file that appears after the first load shifts every later source's
// merge position; sources are told apart by name, so the unchanged ones
// report nothing and the new one reports what a variable outranks.
func TestShadow_SourceNewToAReload(t *testing.T) {
	dir := t.TempDir()
	optional, main := filepath.Join(dir, "optional.yaml"), filepath.Join(dir, "main.yaml")
	writeFile(t, main, "log_level: warn\n")
	env := &environment{vars: []string{"APP_WORKERS=2", "APP_LOG_LEVEL=error"}}
	r, err := NewReloader[shadowCfg](t.Context(), []Option{WithFiles(optional, main), WithEnvPrefix("APP"), WithEnviron(env.environ)})
	require.NoError(t, err)

	writeFile(t, optional, "workers: 9\n")
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []Shadow{{Key: "workers", Source: fileInput(optional), By: envSource("APP_WORKERS", "APP")}}, res.Shadowed)
	assert.Len(t, res.Snapshot.Shadowed, 2, "main's log_level is still shadowed, but did not change")
}

// A key removed from a lower source while a higher one holds it is
// reported; so is a lower source's edit in a reload that also changes the
// higher source, whose change is in Changes.
func TestShadow_RemovedKeysAndChangesAboveThem(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "log_level: info\nworkers: 4\n")
	env := &environment{vars: []string{"APP_LOG_LEVEL=info"}}
	r, err := NewReloader[shadowCfg](t.Context(), []Option{WithFile(file), WithEnvPrefix("APP"), WithEnviron(env.environ)})
	require.NoError(t, err)
	want := []Shadow{{Key: "log_level", Source: fileInput(file), By: envSource("APP_LOG_LEVEL", "APP")}}

	writeFile(t, file, "workers: 4\n")
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Empty(t, res.Changes)
	assert.Equal(t, want, res.Shadowed, "the removal does not take effect")
	assert.Empty(t, res.Snapshot.Shadowed, "nothing the file sets is shadowed now")

	writeFile(t, file, "log_level: debug\nworkers: 4\n")
	env.set("APP_LOG_LEVEL=warn")
	res, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"log_level"}, changedKeys(res.Changes), "the variable's change takes effect")
	assert.Equal(t, want, res.Shadowed, "the file's does not")
}

// A higher source that replaces a whole section with a value that is no
// map shadows every key a lower source set beneath it.
func TestShadow_SectionReplacedWhole(t *testing.T) {
	dir := t.TempDir()
	base, over := filepath.Join(dir, "base.yaml"), filepath.Join(dir, "over.yaml")
	writeFile(t, base, "db:\n  host: a\n  port: 5432\n")
	writeFile(t, over, "db: null\n")
	snap, err := LoadSnapshot[shadowCfg](t.Context(), WithFiles(base, over))
	require.NoError(t, err)
	assert.Equal(t, []Shadow{
		{Key: "db.host", Source: fileInput(base), By: fileInput(over)},
		{Key: "db.port", Source: fileInput(base), By: fileInput(over)},
	}, snap.Shadowed)
}

// A list or a typed map is merged whole, so one a higher source sets
// shadows every input a lower source set beneath its key — with or without
// provenance, which alone records the value's elements.
func TestShadow_ListAndTypedMapReplaceWhole(t *testing.T) {
	type cfg struct {
		Limits map[string]int `koanf:"limits"`
		Extra  any            `koanf:"extra"`
	}
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "limits:\n  a: 1\n  b: 2\nextra:\n  x: 1\n  y: 2\n")
	over := &originProvider{data: map[string]any{"limits": map[string]int{"a": 5}, "extra": []any{1, 2}}}
	opts := []Option{WithFile(file), WithOverrideProvider(over, nil)}
	provider := Source{Kind: SourceOverride, Name: "0"}
	want := []Shadow{
		{Key: "extra", Source: fileInput(file), By: provider},
		{Key: `limits["a"]`, Source: fileInput(file), By: provider},
		{Key: `limits["b"]`, Source: fileInput(file), By: provider},
	}

	snap, err := LoadSnapshot[cfg](t.Context(), opts...)
	require.NoError(t, err)
	assert.Equal(t, want, snap.Shadowed, "with provenance")
	assert.Equal(t, map[string]int{"a": 5}, snap.Config.Limits)
	for _, tt := range withAndWithoutProvenance {
		t.Run(tt.name, func(t *testing.T) {
			r, err := NewReloader[cfg](t.Context(), opts, tt.opts...)
			require.NoError(t, err)
			assert.Equal(t, want, r.Snapshot().Shadowed)
			writeFile(t, file, "limits:\n  a: 10\n  b: 20\nextra:\n  x: 10\n  y: 2\n")
			res, err := r.Reload(t.Context())
			require.NoError(t, err)
			assert.Empty(t, res.Changes)
			assert.Equal(t, want, res.Shadowed)
			writeFile(t, file, "limits:\n  a: 1\n  b: 2\nextra:\n  x: 1\n  y: 2\n")
		})
	}
}

// Defaults beneath the sources change no answer, with or without
// provenance, which alone records them: a section tag defaults fill that a
// file and a variable set keys of, and a default map a file replaced with a
// value that a higher source made a map again.
func TestShadow_DefaultsBeneathDoNotChangeTheAnswer(t *testing.T) {
	type cfg struct {
		DB struct {
			Host string `koanf:"host" default:"localhost"`
			Port int    `koanf:"port" default:"5432"`
			Name string `koanf:"name" default:"app"`
		} `koanf:"db"`
		Extra any `koanf:"extra"`
	}
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "db:\n  host: file-host\n  port: 1\nextra: text\n")
	over := &originProvider{data: map[string]any{"extra": map[string]any{"b": 2}}}
	opts := []Option{
		WithDefaults(map[string]any{"extra": map[string]any{"a": 1}}),
		WithFile(file),
		WithEnvPrefix("APP"),
		WithEnviron(func() []string { return []string{"APP_DB_HOST=env-host"} }),
		WithOverrideProvider(over, nil),
	}
	want := []Shadow{
		{Key: "db.host", Source: fileInput(file), By: envSource("APP_DB_HOST", "APP")},
		{Key: "extra", Source: fileInput(file), By: Source{Kind: SourceOverride, Name: "0"}},
	}

	snap, err := LoadSnapshot[cfg](t.Context(), opts...)
	require.NoError(t, err)
	assert.Equal(t, want, snap.Shadowed, "with provenance")
	for _, tt := range withAndWithoutProvenance {
		t.Run(tt.name, func(t *testing.T) {
			r, err := NewReloader[cfg](t.Context(), opts, tt.opts...)
			require.NoError(t, err)
			assert.Equal(t, want, r.Snapshot().Shadowed)
			writeFile(t, file, "db:\n  host: file-host-2\n  port: 1\nextra: other\n")
			res, err := r.Reload(t.Context())
			require.NoError(t, err)
			assert.Equal(t, want, res.Shadowed)
			writeFile(t, file, "db:\n  host: file-host\n  port: 1\nextra: text\n")
		})
	}
}

// Designed overrides are never reported: defaults beneath any source, the
// layering inside the environment — narrower prefixes over broader ones —
// and input no field reads, which unknown-key handling owns.
func TestShadow_DesignedOverridesAreNotReported(t *testing.T) {
	dir := t.TempDir()
	base, over := filepath.Join(dir, "base.yaml"), filepath.Join(dir, "over.yaml")
	writeFile(t, base, "log_level: warn\nlegacy: 1\n")
	writeFile(t, over, "legacy: 2\n")
	env := &environment{vars: []string{"APP_WORKER_WORKERS=2", "APP_WORKERS=3"}}
	r, err := NewReloader[shadowCfg](t.Context(), []Option{
		WithDefaults(map[string]any{"log_level": "debug", "workers": 1}),
		WithFiles(base, over),
		WithEnvPrefixes("APP_WORKER", "APP"),
		WithEnviron(env.environ),
		WithAllowUnknownKeys(),
	})
	require.NoError(t, err)
	assert.Empty(t, r.Snapshot().Shadowed)
	assert.Equal(t, 2, r.Config().Workers)

	env.set("APP_WORKER_WORKERS=2", "APP_WORKERS=30")
	writeFile(t, base, "log_level: warn\nlegacy: 10\n")
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Empty(t, res.Changes)
	assert.Empty(t, res.Shadowed)
}

// Sensitive keys are reported by key and source, never value; inside a
// sensitive map, the entry and the variable naming it are withheld.
func TestShadow_SensitiveKeys(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "password: FAKE_FILE_SECRET\ntokens:\n  fake_entry: FAKE_FILE_TOKEN\n")
	env := &environment{vars: []string{"APP_PASSWORD=FAKE_ENV_SECRET", "APP_TOKENS_FAKE_ENTRY=FAKE_ENV_TOKEN"}}
	want := []Shadow{
		{Key: "password", Source: fileInput(file), By: envSource("APP_PASSWORD", "APP")},
		{Key: "tokens", Source: fileInput(file), By: Source{Kind: SourceEnv, Prefix: "APP"}},
	}
	opts := []Option{WithFile(file), WithEnvPrefix("APP"), WithEnviron(env.environ)}

	snap, err := LoadSnapshot[shadowCfg](t.Context(), opts...)
	require.NoError(t, err)
	assert.Equal(t, want, snap.Shadowed)

	var logs jsonLog
	r, err := NewReloader[shadowCfg](t.Context(), append(opts, WithLogger(logs.logger())))
	require.NoError(t, err)
	writeFile(t, file, "password: FAKE_FILE_SECRET_2\ntokens:\n  fake_entry: FAKE_FILE_TOKEN_2\n")
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, want, res.Shadowed)
	encoded, err := json.Marshal(res)
	require.NoError(t, err)
	for _, text := range []string{string(encoded), logs.buf.String()} {
		for _, secret := range []string{"FAKE_FILE_SECRET", "FAKE_ENV_SECRET", "FAKE_FILE_TOKEN", "FAKE_ENV_TOKEN", "fake_entry", "FAKE_ENTRY"} {
			assert.NotContains(t, text, secret)
		}
	}
}

// A WithShadowedInputs handler sees the shadowed inputs at every load and
// reload: nil accepts them, an error fails the load with ErrValidation.
func TestWithShadowedInputs(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "workers: 4\n")
	env := func() []string { return []string{"APP_WORKERS=8"} }
	want := []Shadow{{Key: "workers", Source: fileInput(file), By: envSource("APP_WORKERS", "APP")}}

	var seen []*ShadowedInputsError
	accept := func(_ context.Context, shadowed *ShadowedInputsError) error {
		seen = append(seen, shadowed)
		return nil
	}
	cfg, err := Load[shadowCfg](t.Context(), WithFile(file), WithEnvPrefix("APP"), WithEnviron(env), WithShadowedInputs(accept))
	require.NoError(t, err)
	assert.Equal(t, 8, cfg.Workers)
	require.Len(t, seen, 1)
	assert.Equal(t, want, seen[0].Inputs)

	_, err = Load[shadowCfg](t.Context(), WithFile(file), WithEnvPrefix("APP"), WithEnviron(env),
		WithShadowedInputs(func(_ context.Context, shadowed *ShadowedInputsError) error { return shadowed }))
	require.ErrorIs(t, err, ErrValidation)
	var shadowed *ShadowedInputsError
	require.ErrorAs(t, err, &shadowed)
	assert.Equal(t, want, shadowed.Inputs)
	assert.Contains(t, err.Error(), "workers (file "+file+", kept by environment APP_WORKERS)")

	seen = nil
	_, err = Load[shadowCfg](t.Context(), WithFile(file), WithShadowedInputs(accept))
	require.NoError(t, err)
	assert.Empty(t, seen, "not called when nothing is shadowed")

	seen = nil
	r, err := NewReloader[shadowCfg](t.Context(), []Option{WithFile(file), WithEnvPrefix("APP"), WithEnviron(env), WithShadowedInputs(accept)})
	require.NoError(t, err)
	_, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Len(t, seen, 2, "called at the initial load and at the reload")
}

// A handler that refuses keeps the previous config live.
func TestWithShadowedInputs_RefusedReload(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "log_level: info\n")
	env := &environment{}
	r, err := NewReloader[shadowCfg](t.Context(), []Option{WithFile(file), WithEnvPrefix("APP"), WithEnviron(env.environ),
		WithShadowedInputs(func(_ context.Context, shadowed *ShadowedInputsError) error { return shadowed })})
	require.NoError(t, err)

	env.set("APP_LOG_LEVEL=debug")
	_, err = r.Reload(t.Context())
	require.ErrorIs(t, err, ErrReload)
	require.ErrorIs(t, err, ErrValidation)
	assert.Equal(t, "info", r.Config().LogLevel)
	assert.Empty(t, r.Snapshot().Shadowed)
}

// A caller's own loader gives the reloader no view of its sources.
func TestShadow_NewReloaderFuncReportsNothing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "log_level: info\n")
	env := []string{"APP_LOG_LEVEL=info"}
	r, err := NewReloaderFunc(t.Context(), func(ctx context.Context) (*shadowCfg, error) {
		return Load[shadowCfg](ctx, WithFile(file), WithEnvPrefix("APP"), WithEnviron(func() []string { return env }))
	})
	require.NoError(t, err)
	writeFile(t, file, "log_level: debug\n")
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Nil(t, res.Shadowed)
	assert.Nil(t, res.Snapshot.Shadowed)
}

// A failed reload keeps the published snapshot's list and the inputs it is
// measured from, so the change is reported once the reload succeeds.
func TestShadow_FailedReloadKeepsTheComparison(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "log_level: info\n")
	env := func() []string { return []string{"APP_LOG_LEVEL=info"} }
	r, err := NewReloader[shadowCfg](t.Context(), []Option{WithFile(file), WithEnvPrefix("APP"), WithEnviron(env)})
	require.NoError(t, err)
	before := r.Snapshot().Shadowed
	require.Len(t, before, 1)

	writeFile(t, file, "log_level: debug\nworkers: many\n")
	_, err = r.Reload(t.Context())
	require.ErrorIs(t, err, ErrReload)
	assert.Equal(t, before, r.Snapshot().Shadowed)

	writeFile(t, file, "log_level: debug\n")
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, before, res.Shadowed)
}

func TestShadow_JSON(t *testing.T) {
	s := Shadow{Key: "log_level", Source: fileInput("/etc/app.yaml"), By: envSource("APP_LOG_LEVEL", "APP")}
	encoded, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{"key":"log_level","source":{"kind":"file","name":"/etc/app.yaml"},"by":{"kind":"environment","name":"APP_LOG_LEVEL","prefix":"APP"}}`, string(encoded))
	assert.Equal(t, "log_level (file /etc/app.yaml, kept by environment APP_LOG_LEVEL)", s.String())
}

// Equal values digest equally however they were built, through reflection
// or not; values that differ in type or content do not; the key keys the
// digest.
func TestDigest(t *testing.T) {
	key := []byte("digest key")
	digest := func(v any) [32]byte { return newEncoder(key).digest(v) }
	n, m := 5, 5
	at := func(text string) time.Time {
		v, err := time.Parse(time.RFC3339, text)
		require.NoError(t, err)
		return v
	}
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	nested := map[string]any{"a": []any{"x", map[string]any{"b": 1}}}
	list := []any{"x", nil, 1.5}
	byInt := map[int]string{}
	byInt[2], byInt[1] = "b", "a"
	for _, tt := range []struct {
		name  string
		a, b  any
		equal bool
	}{
		{"fresh pointers", &n, &m, true},
		{"a pointer and its value", &n, 5, true},
		{"a map through a pointer", &nested, nested, true},
		{"a list through a pointer", &list, list, true},
		{"map order", map[string]any{"a": 1, "b": []any{"x"}}, map[string]any{"b": []any{"x"}, "a": 1}, true},
		{"non-string keys in any order", byInt, map[int]string{1: "a", 2: "b"}, true},
		{"lists of maps", []any{map[string]any{"a": 1}}, []any{map[string]any{"a": 1}}, true},
		{"instants", at("2026-01-02T03:04:05Z"), at("2026-01-02T03:04:05Z"), true},
		{"cycles", cyclic, cyclic, true},
		{"int and string", 1, "1", false},
		{"int and int64", 1, int64(1), false},
		{"a typed map and an untyped one", map[string]string{"a": "x"}, map[string]any{"a": "x"}, false},
		{"list boundaries", []any{"ab", "c"}, []any{"a", "bc"}, false},
		{"nil and empty string", nil, "", false},
		{"instants apart", at("2026-01-02T03:04:05Z"), at("2026-01-02T03:04:06Z"), false},
		{"nested values", map[string]any{"a": map[string]any{"b": 1}}, map[string]any{"a": map[string]any{"b": 2}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.equal, digest(tt.a) == digest(tt.b))
		})
	}
	assert.NotEqual(t, digest("v"), newEncoder([]byte("another key")).digest("v"))
}

// One encoder digests every input of a load: reusing its hash and buffers
// gives each value the digest a fresh encoder gives it, whatever it
// digested before.
func TestEncoder_ReuseMatchesFresh(t *testing.T) {
	key := []byte("digest key")
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	values := []any{
		"a",
		map[string]any{"b": map[string]any{"c": []any{1, "x"}}, "a": 2},
		cyclic,
		nil,
		3.5,
		[]any{map[string]any{"k": true}, map[string]any{"j": false}},
		map[int]string{2: "b", 1: "a"},
		&struct{ N int }{1},
		"a",
	}
	shared := newEncoder(key)
	for round := range 2 {
		for i, v := range values {
			assert.Equal(t, newEncoder(key).digest(v), shared.digest(v), "value %d, round %d", i, round)
		}
	}
	assert.Empty(t, shared.visiting)
	assert.Empty(t, shared.keys)
}
