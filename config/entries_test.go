package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readEntries(entries map[string]any) func(context.Context) (map[string]any, error) {
	return func(context.Context) (map[string]any, error) { return entries, nil }
}

// What CheckKey accepts, persisted verbatim, loads back as the config
// DecodeEdits makes of the same entries: a field, a map key holding a dot,
// a field of a map entry, a list index, a key beneath an interface.
func TestEntries_LoadWhatCheckKeyAccepts(t *testing.T) {
	defaults := WithDefaults(editDefaults(true))
	entries := map[string]any{}
	for key, value := range map[string]any{
		"primary.port":              6,
		`addresses["edge.example"]`: map[string]any{"host": "e", "port": 7},
		`addresses["first"].port`:   9,
		"pool[0].host":              "y",
		"pool[1]":                   map[string]any{"host": "z", "port": 3},
		"route":                     "192.0.2.0/24",
		"extra.x":                   1,
	} {
		require.NoError(t, CheckKey[editConfig](t.Context(), key, value, defaults), key)
		entries[key] = value
	}
	got, err := Load[editConfig](t.Context(), defaults, WithBaseEntries(readEntries(entries)))
	require.NoError(t, err)
	want, err := DecodeEdits[editConfig](t.Context(), nil, entries, defaults)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, &editConfig{
		Addresses: map[string]editServer{"first": {"a", 9}, "second": {"b", 2}, "edge.example": {"e", 7}},
		Primary:   editServer{"p", 6},
		Pool:      []editServer{{"y", 1}, {"z", 3}},
		Route:     netip.MustParsePrefix("192.0.2.0/24"),
		Extra:     map[string]any{"x": 1, "y": 2},
	}, got)

	dotted, err := NewMapProvider(func(context.Context) (map[string]any, error) {
		return map[string]any{"addresses.first.port": 9, "primary.port": 6}, nil
	}, ".")
	require.NoError(t, err)
	viaDots, err := Load[editConfig](t.Context(), defaults, WithBaseProvider(dotted, nil))
	require.NoError(t, err)
	viaEntries, err := Load[editConfig](t.Context(), defaults, WithBaseEntries(readEntries(map[string]any{`addresses["first"].port`: 9, "primary.port": 6})))
	require.NoError(t, err)
	assert.Equal(t, viaDots, viaEntries, "a dotted provider keeps its own grammar")
}

// Entries apply over the sources beneath them, below the ones above, in
// the order added among the providers of their tier: an index edits the
// list a lower source set, element by element, and the other elements stay
// that source's.
func TestEntries_Precedence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "primary: {host: f, port: 1}\npool: [{host: a, port: 1}, {host: b, port: 2}]\n")
	entries := map[string]any{"primary.port": 7, "pool[1].port": 9, "pool[2].host": "c"}

	snap, err := LoadSnapshot[editConfig](t.Context(), WithFile(file), WithOverrideEntries(readEntries(entries)))
	require.NoError(t, err)
	assert.Equal(t, editServer{"f", 7}, snap.Config.Primary)
	assert.Equal(t, []editServer{{"a", 1}, {"b", 9}, {"c", 0}}, snap.Config.Pool)
	override := Source{Kind: SourceOverride, Name: "0"}
	assert.Equal(t, override, snap.Provenance["pool[1].port"].Input)
	assert.Equal(t, override, snap.Provenance["pool[2].host"].Input)
	assert.Equal(t, fileInput(file), snap.Provenance["pool[1].host"].Input)
	assert.Equal(t, fileInput(file), snap.Provenance["pool[0].port"].Input)
	assert.Equal(t, []Shadow{{Key: "primary.port", Source: fileInput(file), By: override}}, snap.Shadowed,
		"an entry setting one element shadows none of the file's others")

	entries = map[string]any{"primary.port": 7, "pool[0].port": 9}
	below, err := Load[editConfig](t.Context(), WithBaseEntries(readEntries(entries)), WithFile(file))
	require.NoError(t, err)
	assert.Equal(t, editServer{"f", 1}, below.Primary, "a file above the entries wins")
	assert.Equal(t, []editServer{{"a", 1}, {"b", 2}}, below.Pool, "and replaces the list they edited")

	later, err := NewMapProvider(func(context.Context) (map[string]any, error) {
		return map[string]any{"primary": map[string]any{"port": 8}}, nil
	}, "")
	require.NoError(t, err)
	ordered, err := Load[editConfig](t.Context(), WithOverrideEntries(readEntries(entries)), WithOverrideProvider(later, nil))
	require.NoError(t, err)
	assert.Equal(t, 8, ordered.Primary.Port, "an override provider added after the entries wins")
}

// A reload rebuilds an index entry over the list beneath it as it is now,
// so the lower source's other elements take effect, and nothing is
// reported shadowed.
func TestEntries_ReloadSeesTheListBeneath(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "pool: [{host: a, port: 1}, {host: b, port: 2}]\n")
	for _, tt := range withAndWithoutProvenance {
		t.Run(tt.name, func(t *testing.T) {
			r, err := NewReloader[editConfig](t.Context(), []Option{WithFile(file), WithOverrideEntries(readEntries(map[string]any{"pool[1].port": 9}))}, tt.opts...)
			require.NoError(t, err)
			writeFile(t, file, "pool: [{host: a, port: 5}, {host: b, port: 2}]\n")
			res, err := r.Reload(t.Context())
			require.NoError(t, err)
			assert.Equal(t, []editServer{{"a", 5}, {"b", 9}}, r.Config().Pool)
			assert.Empty(t, res.Shadowed)
			writeFile(t, file, "pool: [{host: a, port: 1}, {host: b, port: 2}]\n")
		})
	}
}

// Entries that cannot all apply fail the load, each named, with no value;
// under WithInvalidEntries they are withheld and reported, and what lies
// beneath them stays.
func TestEntries_ThatCannotAllApply(t *testing.T) {
	type secretsCfg struct {
		Tokens     map[string]string `koanf:"tokens" sensitive:"true"`
		editConfig `koanf:",squash"`
	}
	defaults := WithDefaults(map[string]any{"primary": map[string]any{"host": "p", "port": 5}, "pool": []any{map[string]any{"host": "x", "port": 1}}})
	for _, tc := range []struct {
		name     string
		entries  map[string]any
		key      string
		reason   string
		withheld []string
	}{
		{"one value under two spellings", map[string]any{"primary.port": 6, "MAIN.Port": 7}, "primary.port", "set more than once", []string{"primary.port"}},
		{"an index past the next element", map[string]any{"pool[2].host": "z"}, "pool[2].host", "beyond the next element", []string{"pool[2].host"}},
		{"a key beneath a value holding no keys", map[string]any{"primary": 5, "primary.port": 6}, "primary.port", "primary is set to a value with no keys beneath it", []string{"primary", "primary.port"}},
		{"elements and keys of one value", map[string]any{"extra.x": 1, "extra[0]": 2}, "extra[0]", "select both its elements and its keys", []string{"extra[0]"}},
		{"beneath a sensitive container", map[string]any{`tokens["prod"]`: "FAKE_SECRET_VALUE", `TOKENS["prod"]`: "FAKE_SECRET_VALUE"}, "tokens", "set more than once", []string{"tokens"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load[secretsCfg](t.Context(), defaults, WithBaseEntries(readEntries(tc.entries)))
			require.ErrorIs(t, err, ErrValidation)
			var ke *KeyError
			require.ErrorAs(t, err, &ke)
			assert.Equal(t, tc.key, ke.Key)
			assert.Contains(t, err.Error(), tc.reason)
			assert.NotContains(t, err.Error(), "prod")
			assert.NotContains(t, err.Error(), "FAKE_SECRET_VALUE")

			var rejected []KeyError
			got, err := Load[secretsCfg](t.Context(), defaults, WithBaseEntries(readEntries(tc.entries),
				WithInvalidEntries(func(_ context.Context, entries []KeyError) error { rejected = entries; return nil })))
			require.NoError(t, err)
			var keys []string
			for _, r := range rejected {
				keys = append(keys, r.Key)
			}
			assert.Equal(t, tc.withheld, keys)
			assert.Equal(t, editServer{"p", 5}, got.Primary)
			assert.Equal(t, []editServer{{"x", 1}}, got.Pool)
			assert.Empty(t, got.Tokens)
		})
	}
}

type judgedUpstream struct {
	URL     string        `koanf:"url" validate:"required"`
	Timeout time.Duration `koanf:"timeout" default:"1s" validate:"min=1ms"`
}

type judgedConfig struct {
	Port      int                       `koanf:"port" default:"8" validate:"min=1,max=100"`
	Upstreams map[string]judgedUpstream `koanf:"upstreams" validate:"dive"`
	Pool      []editServer              `koanf:"pool"`
	Min       int                       `koanf:"min" default:"1"`
	Max       int                       `koanf:"max" default:"10"`
}

func (c *judgedConfig) Validate() error {
	if c.Min > c.Max {
		return errors.New("min exceeds max")
	}
	return nil
}

// WithInvalidEntries judges an entry over the layers beneath the source
// with the source's other entries applied: an entry that needs another — a
// field its map entry requires, the element before it, a limit moved with
// it — is kept, as is one that needs a lower layer, and an entry invalid
// however it is applied is withheld. Entries each valid on their own that
// break a rule together are kept, and the load fails.
func TestEntries_WithInvalidEntriesJudgesEntriesInContext(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "upstreams: {b: {url: https://b.example}}\n")
	var rejected []KeyError
	report := WithInvalidEntries(func(_ context.Context, entries []KeyError) error { rejected = entries; return nil })
	got, err := Load[judgedConfig](t.Context(), WithFile(file), WithOverrideEntries(readEntries(map[string]any{
		`upstreams["a"].timeout`: "2s",
		`upstreams["a"].url`:     "https://a.example",
		`upstreams["b"].timeout`: "3s",
		"pool[0].host":           "x",
		"pool[1].host":           "y",
		"min":                    15,
		"max":                    20,
		"port":                   500,
	}), report))
	require.NoError(t, err)
	assert.Equal(t, map[string]judgedUpstream{"a": {"https://a.example", 2 * time.Second}, "b": {"https://b.example", 3 * time.Second}}, got.Upstreams)
	assert.Equal(t, []editServer{{"x", 0}, {"y", 0}}, got.Pool)
	assert.Equal(t, 15, got.Min)
	assert.Equal(t, 8, got.Port)
	require.Len(t, rejected, 1)
	assert.Equal(t, "port", rejected[0].Key)

	_, err = Load[judgedConfig](t.Context(), WithOverrideEntries(readEntries(map[string]any{"min": 9, "max": 5}), report))
	require.ErrorIs(t, err, ErrValidation)
	assert.Empty(t, rejected, "each passes on its own, so neither is chosen for discarding")
}

// A provider under WithInvalidEntries is judged as entries are: field by
// field over the layers beneath, with its other fields applied.
func TestWithInvalidEntries_ProviderFieldsJudgedInContext(t *testing.T) {
	type section struct {
		Min int `koanf:"min" default:"1"`
		Max int `koanf:"max" default:"10" validate:"gtefield=Min"`
	}
	type cfg struct {
		Limits section `koanf:"limits"`
		Port   int     `koanf:"port" default:"8" validate:"min=1"`
	}
	provider, err := NewMapProvider(func(context.Context) (map[string]any, error) {
		return map[string]any{"limits": map[string]any{"min": 15, "max": 20}, "port": 0}, nil
	}, "")
	require.NoError(t, err)
	var rejected []KeyError
	got, err := Load[cfg](t.Context(), WithBaseProvider(provider, nil, WithInvalidEntries(func(_ context.Context, entries []KeyError) error { rejected = entries; return nil })))
	require.NoError(t, err)
	assert.Equal(t, section{15, 20}, got.Limits)
	assert.Equal(t, 8, got.Port)
	require.Len(t, rejected, 1)
	assert.Equal(t, "port", rejected[0].Key)
}

// An entry naming nothing config reads is an unknown key like any source's,
// with the key it most likely misspells, as is a key inside an entry's
// value; a key under a former name loads and warns.
func TestEntries_UnknownAndAliasedKeys(t *testing.T) {
	entries := map[string]any{"primary.prot": 1, "pool[x]": 2, "MAIN.port": 6, "bogus": map[string]any{"a": 1}, `addresses["a"]`: map[string]any{"hots": "h"}}
	_, err := Load[editConfig](t.Context(), WithBaseEntries(readEntries(entries)))
	var unknown *UnknownKeysError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, []UnknownKey{{Key: `addresses["a"].hots`, Suggestion: `addresses["a"].host`}, {Key: "bogus"}, {Key: "pool[x]"}, {Key: "primary.prot", Suggestion: "primary.port"}}, unknown.Keys)

	var logs bytes.Buffer
	snap, err := LoadSnapshot[editConfig](t.Context(), WithBaseEntries(readEntries(entries)), WithAllowUnknownKeys(),
		WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))))
	require.NoError(t, err)
	assert.Equal(t, 6, snap.Config.Primary.Port)
	assert.Len(t, snap.UnknownKeys.Keys, 4)
	assert.Contains(t, logs.String(), "MAIN (now primary) in base provider 0")
}

func TestEntries_Boundaries(t *testing.T) {
	_, err := Load[keyConfig](t.Context(), WithBaseEntries(nil))
	require.ErrorIs(t, err, ErrSource)
	failed := context.DeadlineExceeded
	_, err = Load[keyConfig](t.Context(), WithBaseEntries(func(context.Context) (map[string]any, error) { return nil, failed }))
	require.ErrorIs(t, err, ErrSource)
	require.ErrorIs(t, err, failed)
	got, err := Load[keyConfig](t.Context(), WithBaseEntries(readEntries(nil)))
	require.NoError(t, err)
	assert.Equal(t, 8, got.Port)
	entries := map[string]any{"nested": map[string]any{"count": 4}}
	_, err = Load[keyConfig](t.Context(), WithBaseEntries(readEntries(entries)), WithDefaults(map[string]any{"nested": map[string]any{"count": 3}}))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"nested": map[string]any{"count": 4}}, entries, "what read returns is the caller's")
}

// jointConfig's a and b are valid only equal, which only its Validate
// method, naming no field, says.
type jointConfig struct {
	A      int           `koanf:"a"`
	B      int           `koanf:"b"`
	Bad    int           `koanf:"bad" validate:"max=5"`
	Pool   []editServer  `koanf:"pool"`
	Limits limitsSection `koanf:"limits"`
}

type limitsSection struct {
	Min int `koanf:"min" validate:"ltefield=Max"`
	Max int `koanf:"max" default:"10"`
}

func (c *jointConfig) Validate() error {
	if c.A != c.B {
		return errors.New("a and b differ")
	}
	return nil
}

// judged loads entries under WithInvalidEntries over below and beneath
// above, returning the config, the keys withheld and the load's error.
func judged[T any](t *testing.T, entries map[string]any, below []Option, above ...Option) (*T, []string, error) {
	t.Helper()
	var rejected []string
	report := WithInvalidEntries(func(_ context.Context, entries []KeyError) error {
		rejected = nil
		for _, ke := range entries {
			rejected = append(rejected, ke.Key)
		}
		return nil
	})
	opts := slices.Concat(below, []Option{WithOverrideEntries(readEntries(entries), report)}, above)
	got, err := Load[T](t.Context(), opts...)
	return got, rejected, err
}

// An entry that fails is withheld without the entries it is not to blame
// for: a chain of list elements each needing the one before it is kept
// whole, and so are two entries valid only together, beside an entry
// breaking a rule of its own, one that does not decode, or one breaking a
// rule relating it to a field beside it.
func TestEntries_WithInvalidEntriesWithholdsOnlyWhatFails(t *testing.T) {
	got, rejected, err := judged[jointConfig](t, map[string]any{"pool[0].host": "x", "pool[1].host": "y", "pool[2].host": "z", "bad": 9}, nil)
	require.NoError(t, err)
	assert.Equal(t, []editServer{{"x", 0}, {"y", 0}, {"z", 0}}, got.Pool)
	assert.Equal(t, []string{"bad"}, rejected)

	for _, bad := range []map[string]any{{"bad": 9}, {"bad": "nine"}, {"limits.min": 20}} {
		entries := map[string]any{"a": 3, "b": 3}
		maps.Copy(entries, bad)
		got, rejected, err = judged[jointConfig](t, entries, nil)
		require.NoError(t, err, bad)
		assert.Equal(t, [2]int{3, 3}, [2]int{got.A, got.B}, bad)
		assert.Equal(t, jointConfig{A: 3, B: 3, Limits: limitsSection{Max: 10}}, *got, bad)
		assert.Equal(t, slices.Collect(maps.Keys(bad)), rejected, bad)
	}
}

// chainConfig's rules name no field: a needs b, b needs c, and z is never
// valid set.
type chainConfig struct {
	A int `koanf:"a"`
	B int `koanf:"b"`
	C int `koanf:"c"`
	Z int `koanf:"z"`
}

func (c *chainConfig) Validate() error {
	switch {
	case c.A > 0 && c.B == 0:
		return errors.New("a needs b")
	case c.B > 0 && c.C == 0:
		return errors.New("b needs c")
	case c.Z > 0:
		return errors.New("z is set")
	}
	return nil
}

// Entries a failure names no field of are judged one by one, retried in
// key order until none more can be kept: a needs b, which comes after it,
// and is kept with it.
func TestEntries_WithInvalidEntriesKeepsAChainAgainstKeyOrder(t *testing.T) {
	got, rejected, err := judged[chainConfig](t, map[string]any{"a": 1, "b": 1, "c": 1, "z": 1}, nil)
	require.NoError(t, err)
	assert.Equal(t, chainConfig{A: 1, B: 1, C: 1}, *got)
	assert.Equal(t, []string{"z"}, rejected)
}

type orderedConfig struct {
	Min int `koanf:"min" validate:"ltefield=Max"`
	Max int `koanf:"max" default:"10"`
	Bad int `koanf:"bad" validate:"max=5"`
}

// Entries each valid with the other sources that break a rule together are
// kept, and the load fails on that rule: neither is chosen for discarding.
// No set of the other entries can pass beside them, so only an entry a
// failure names is withheld.
func TestEntries_WithInvalidEntriesKeepsEntriesValidAlone(t *testing.T) {
	_, rejected, err := judged[orderedConfig](t, map[string]any{"min": 9, "max": 5}, nil)
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "min")
	assert.Empty(t, rejected)

	_, rejected, err = judged[orderedConfig](t, map[string]any{"min": 9, "max": 5, "bad": 9}, nil)
	require.ErrorIs(t, err, ErrValidation)
	assert.Equal(t, []string{"bad"}, rejected)
}

type layeredConfig struct {
	Port int    `koanf:"port" default:"1" validate:"min=1"`
	Max  int    `koanf:"max" default:"1" validate:"max=10"`
	Name string `koanf:"name"`
	Bad  int    `koanf:"bad" validate:"max=5"`
}

// Entries are judged against every source, not the layers beneath alone:
// over a file that fails on its own, an entry that repairs it and one
// unrelated to it are kept, beside a source above that repairs the rest.
func TestEntries_WithInvalidEntriesJudgesOverAFailingLayer(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "port: 0\nmax: 50\n")
	got, rejected, err := judged[layeredConfig](t, map[string]any{"port": 8, "name": "x", "bad": 9},
		[]Option{WithFile(file)}, WithOverrideProvider(mapSource{"max": 5}, nil))
	require.NoError(t, err)
	assert.Equal(t, layeredConfig{Port: 8, Max: 5, Name: "x"}, *got)
	assert.Equal(t, []string{"bad"}, rejected)
}

// firstFailureConfig's Validate returns its first failure, a's before b's.
type firstFailureConfig struct {
	A    int    `koanf:"a"`
	B    int    `koanf:"b"`
	Name string `koanf:"name"`
}

func (c *firstFailureConfig) Validate() error {
	switch {
	case c.A < 0:
		return errors.New("a is negative")
	case c.B < 0:
		return errors.New("b is negative")
	}
	return nil
}

// minMaxConfig's Validate names the values it compares.
type minMaxConfig struct {
	Min  int    `koanf:"min"`
	Max  int    `koanf:"max" default:"10"`
	Name string `koanf:"name"`
}

func (c *minMaxConfig) Validate() error {
	if c.Min > c.Max {
		return fmt.Errorf("min %d exceeds max %d", c.Min, c.Max)
	}
	return nil
}

// A failure of a layer beneath, which a source above repairs, hides none of
// an entry's and blames none of the entries on it: each check stops at its
// first failure, so judging an entry over the layers beneath alone sees the
// layer's failure in place of the entry's.
func TestEntries_WithInvalidEntriesSeesPastAFailureBeneath(t *testing.T) {
	dir := t.TempDir()
	file := func(name, text string) Option {
		path := filepath.Join(dir, name)
		writeFile(t, path, text)
		return WithFile(path)
	}
	t.Run("a Validate method returning its first failure", func(t *testing.T) {
		got, rejected, err := judged[firstFailureConfig](t, map[string]any{"b": -1, "name": "x"},
			[]Option{file("first.yaml", "a: -1\n")}, WithOverrideProvider(mapSource{"a": 1}, nil))
		require.NoError(t, err)
		assert.Equal(t, firstFailureConfig{A: 1, Name: "x"}, *got)
		assert.Equal(t, []string{"b"}, rejected)
	})
	t.Run("a value that does not decode, which stops validation", func(t *testing.T) {
		got, rejected, err := judged[layeredConfig](t, map[string]any{"bad": 9, "name": "x"},
			[]Option{file("decode.yaml", "port: eight\n")}, WithOverrideProvider(mapSource{"port": 8}, nil))
		require.NoError(t, err)
		assert.Equal(t, layeredConfig{Port: 8, Max: 1, Name: "x"}, *got)
		assert.Equal(t, []string{"bad"}, rejected)
	})
	t.Run("a field's rule, which stops the Validate method", func(t *testing.T) {
		got, rejected, err := judged[jointConfig](t, map[string]any{"a": 3, "pool[0].host": "x"},
			[]Option{file("rule.yaml", "bad: 9\n")}, WithOverrideProvider(mapSource{"bad": 1}, nil))
		require.NoError(t, err)
		assert.Equal(t, jointConfig{Bad: 1, Pool: []editServer{{"x", 0}}, Limits: limitsSection{Max: 10}}, *got)
		assert.Equal(t, []string{"a"}, rejected)
	})
}

// When nothing repairs a failing layer, the load fails on the final
// configuration, with the values every source gives, and no entry is
// withheld for a failure that names none: an entry that changes a value
// in a Validate method's message is not told apart by it.
func TestEntries_WithInvalidEntriesFailsOnTheFinalConfiguration(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "min: 15\n")
	_, rejected, err := judged[minMaxConfig](t, map[string]any{"max": 12, "name": "x"}, []Option{WithFile(file)})
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "min 15 exceeds max 12")
	assert.Empty(t, rejected)
}

// requiredConfig's url has no default, so the defaults alone fail.
type requiredConfig struct {
	URL  string `koanf:"url" validate:"required"`
	Name string `koanf:"name"`
	Bad  int    `koanf:"bad" validate:"max=5"`
}

// The defaults are a layer like any other: an entry setting a required
// field they leave unset is kept. When nothing sets it the load fails on
// it, withholding only the entries a failure names.
func TestEntries_WithInvalidEntriesJudgesOverDefaultsThatFail(t *testing.T) {
	got, rejected, err := judged[requiredConfig](t, map[string]any{"url": "https://a.example", "name": "x", "bad": 9}, nil)
	require.NoError(t, err)
	assert.Equal(t, requiredConfig{URL: "https://a.example", Name: "x"}, *got)
	assert.Equal(t, []string{"bad"}, rejected)

	_, rejected, err = judged[requiredConfig](t, map[string]any{"name": "x", "bad": 9}, nil)
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "url(required)")
	assert.Equal(t, []string{"bad"}, rejected)
}

type spanSection struct {
	Min  int `koanf:"min" default:"1"`
	Max  int `koanf:"max" default:"10" validate:"gtefield=Min"`
	Step int `koanf:"step" default:"1" validate:"ltefield=Max"`
}

type spanConfig struct {
	Range spanSection `koanf:"range"`
	Name  string      `koanf:"name"`
}

// The entries of every WithInvalidEntries source are judged together: a
// lower source's entry valid beside a higher one's is kept, and the
// higher source's entry breaking a rule of the struct both set is the one
// withheld.
func TestEntries_WithInvalidEntriesJudgesSourcesTogether(t *testing.T) {
	reported := map[string][]string{}
	report := func(name string) SourceOption {
		return WithInvalidEntries(func(_ context.Context, entries []KeyError) error {
			reported[name] = []string{}
			for _, ke := range entries {
				reported[name] = append(reported[name], ke.Key)
			}
			return nil
		})
	}
	got, err := Load[spanConfig](t.Context(),
		WithBaseProvider(mapSource{"range": map[string]any{"min": 20}, "name": "x"}, nil, report("lower")),
		WithOverrideEntries(readEntries(map[string]any{"range.max": 50, "range.step": 80}), report("higher")))
	require.NoError(t, err)
	assert.Equal(t, spanConfig{Range: spanSection{Min: 20, Max: 50, Step: 1}, Name: "x"}, *got)
	assert.Equal(t, map[string][]string{"lower": {}, "higher": {"range.step"}}, reported)
}

// An entry a higher source sets over is kept, whatever it holds: the
// configuration loaded never holds its value.
func TestEntries_WithInvalidEntriesKeepsAnEntryShadowedAbove(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "bad: 1\n")
	var rejected []KeyError
	got, err := Load[layeredConfig](t.Context(),
		WithBaseProvider(mapSource{"bad": 9, "name": "x"}, nil, WithInvalidEntries(func(_ context.Context, entries []KeyError) error {
			rejected = entries
			return nil
		})),
		WithFile(file))
	require.NoError(t, err)
	assert.Equal(t, layeredConfig{Port: 1, Max: 1, Name: "x", Bad: 1}, *got)
	assert.Empty(t, rejected)
}
