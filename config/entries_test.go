package config

import (
	"bytes"
	"context"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func storedEntries(t *testing.T, stored map[string]any) ContextProvider {
	t.Helper()
	p, err := NewEntryProvider(func(context.Context) (map[string]any, error) { return stored, nil })
	require.NoError(t, err)
	return p
}

// What CheckKey accepts, persisted verbatim, loads back as the config
// DecodeEdits makes of the same entries: a field, a map key holding a dot,
// a field of a map entry, a list index, a key beneath an interface.
func TestEntryProvider_LoadsWhatCheckKeyAccepts(t *testing.T) {
	defaults := WithDefaults(editDefaults(true))
	stored := map[string]any{}
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
		stored[key] = value
	}
	got, err := Load[editConfig](t.Context(), defaults, WithBaseProvider(storedEntries(t, stored), nil))
	require.NoError(t, err)
	want, err := DecodeEdits[editConfig](t.Context(), nil, stored, defaults)
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
	viaEntries, err := Load[editConfig](t.Context(), defaults, WithBaseProvider(storedEntries(t, map[string]any{`addresses["first"].port`: 9, "primary.port": 6}), nil))
	require.NoError(t, err)
	assert.Equal(t, viaDots, viaEntries, "a dotted provider keeps its own grammar")
}

// Entries apply over the sources beneath them, below the ones above: an
// index edits the list a lower source set, element by element, and the
// other elements stay that source's.
func TestEntryProvider_Precedence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "primary: {host: f, port: 1}\npool: [{host: a, port: 1}, {host: b, port: 2}]\n")
	stored := map[string]any{"primary.port": 7, "pool[1].port": 9, "pool[2].host": "c"}

	snap, err := LoadSnapshot[editConfig](t.Context(), WithFile(file), WithOverrideProvider(storedEntries(t, stored), nil))
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

	stored = map[string]any{"primary.port": 7, "pool[0].port": 9}
	below, err := Load[editConfig](t.Context(), WithBaseProvider(storedEntries(t, stored), nil), WithFile(file))
	require.NoError(t, err)
	assert.Equal(t, editServer{"f", 1}, below.Primary, "a file above the entries wins")
	assert.Equal(t, []editServer{{"a", 1}, {"b", 2}}, below.Pool, "and replaces the list they edited")
}

// A reload rebuilds an index entry over the list beneath it as it is now,
// so the lower source's other elements take effect, and nothing is
// reported shadowed.
func TestEntryProvider_ReloadSeesTheListBeneath(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "pool: [{host: a, port: 1}, {host: b, port: 2}]\n")
	for _, tt := range withAndWithoutProvenance {
		t.Run(tt.name, func(t *testing.T) {
			r, err := NewReloader[editConfig](t.Context(), []Option{WithFile(file), WithOverrideProvider(storedEntries(t, map[string]any{"pool[1].port": 9}), nil)}, tt.opts...)
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
func TestEntryProvider_EntriesThatCannotAllApply(t *testing.T) {
	type secretsCfg struct {
		Tokens     map[string]string `koanf:"tokens" sensitive:"true"`
		editConfig `koanf:",squash"`
	}
	defaults := WithDefaults(map[string]any{"primary": map[string]any{"host": "p", "port": 5}, "pool": []any{map[string]any{"host": "x", "port": 1}}})
	for _, tc := range []struct {
		name   string
		stored map[string]any
		keys   []string
		reason string
	}{
		{"one value under two spellings", map[string]any{"primary.port": 6, "MAIN.Port": 7}, []string{"primary.port"}, "set more than once"},
		{"an index past the next element", map[string]any{"pool[2].host": "z"}, []string{"pool[2].host"}, "beyond the next element"},
		{"beneath a sensitive container", map[string]any{`tokens["prod"]`: "FAKE_SECRET_VALUE", `TOKENS["prod"]`: "FAKE_SECRET_VALUE"}, []string{"tokens"}, "set more than once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load[secretsCfg](t.Context(), defaults, WithBaseProvider(storedEntries(t, tc.stored), nil))
			require.ErrorIs(t, err, ErrValidation)
			var ke *KeyError
			require.ErrorAs(t, err, &ke)
			assert.Equal(t, tc.keys[0], ke.Key)
			assert.Contains(t, err.Error(), tc.reason)
			assert.NotContains(t, err.Error(), "prod")
			assert.NotContains(t, err.Error(), "FAKE_SECRET_VALUE")

			var rejected []KeyError
			got, err := Load[secretsCfg](t.Context(), defaults, WithBaseProvider(storedEntries(t, tc.stored), nil,
				WithInvalidEntries(func(_ context.Context, entries []KeyError) error { rejected = entries; return nil })))
			require.NoError(t, err)
			var keys []string
			for _, r := range rejected {
				keys = append(keys, r.Key)
			}
			assert.Equal(t, tc.keys, keys)
			assert.Equal(t, editServer{"p", 5}, got.Primary)
			assert.Equal(t, []editServer{{"x", 1}}, got.Pool)
			assert.Empty(t, got.Tokens)
		})
	}
}

// WithInvalidEntries judges each entry as CheckKey does, against defaults,
// and keeps the valid ones.
func TestEntryProvider_WithInvalidEntriesChecksEachEntry(t *testing.T) {
	var rejected []KeyError
	got, err := Load[keyConfig](t.Context(), WithBaseProvider(storedEntries(t, map[string]any{"port": 200, "nested.count": 4, "items[0].port": 0}), nil,
		WithInvalidEntries(func(_ context.Context, entries []KeyError) error { rejected = entries; return nil })))
	require.NoError(t, err)
	assert.Equal(t, 8, got.Port)
	assert.Equal(t, 4, got.Nested.Count)
	assert.Empty(t, got.Items)
	require.Len(t, rejected, 2)
	assert.Equal(t, []string{"items[0].port", "port"}, []string{rejected[0].Key, rejected[1].Key})
}

// An entry naming nothing config reads is an unknown key like any source's,
// with the key it most likely misspells; a key under a former name loads
// and warns.
func TestEntryProvider_UnknownAndAliasedKeys(t *testing.T) {
	stored := map[string]any{"primary.prot": 1, "pool[x]": 2, "MAIN.port": 6, "bogus": map[string]any{"a": 1}}
	_, err := Load[editConfig](t.Context(), WithBaseProvider(storedEntries(t, stored), nil))
	var unknown *UnknownKeysError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, []UnknownKey{{Key: "bogus"}, {Key: "pool[x]"}, {Key: "primary.prot", Suggestion: "primary.port"}}, unknown.Keys)

	var logs bytes.Buffer
	snap, err := LoadSnapshot[editConfig](t.Context(), WithBaseProvider(storedEntries(t, stored), nil), WithAllowUnknownKeys(),
		WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))))
	require.NoError(t, err)
	assert.Equal(t, 6, snap.Config.Primary.Port)
	assert.Len(t, snap.UnknownKeys.Keys, 3)
	assert.Contains(t, logs.String(), "MAIN (now primary) in base provider 0")
}

func TestEntryProvider_Boundaries(t *testing.T) {
	_, err := NewEntryProvider(nil)
	require.Error(t, err)
	p := storedEntries(t, map[string]any{"port": 9})
	_, err = p.Read()
	require.Error(t, err, "only a load can resolve the keys")
	_, err = Load[keyConfig](t.Context(), WithBaseProvider(p, yaml.Parser()))
	require.ErrorIs(t, err, ErrSource)
	failed := context.DeadlineExceeded
	broken, err := NewEntryProvider(func(context.Context) (map[string]any, error) { return nil, failed })
	require.NoError(t, err)
	_, err = Load[keyConfig](t.Context(), WithBaseProvider(broken, nil))
	require.ErrorIs(t, err, ErrSource)
	require.ErrorIs(t, err, failed)
	empty, err := NewEntryProvider(func(context.Context) (map[string]any, error) { return nil, nil })
	require.NoError(t, err)
	got, err := Load[keyConfig](t.Context(), WithBaseProvider(empty, nil))
	require.NoError(t, err)
	assert.Equal(t, 8, got.Port)
}
