package config

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type unknownSection struct {
	Host string `koanf:"host"`
	Port int    `koanf:"port"`
}

type openSection struct {
	Name string         `koanf:"name"`
	Rest map[string]any `koanf:",remain"`
}

type unknownEntriesConfig struct {
	Port    int            `koanf:"port" default:"1"`
	Section unknownSection `koanf:"section"`
	Extra   any            `koanf:"extra"`
	Open    openSection    `koanf:"open"`
	Item    customDecoded  `koanf:"item"`
	Items   []struct {
		Port int `koanf:"port"`
	} `koanf:"items"`
}

// unknownReport is a WithUnknownEntries report recording what it receives
// in got, and failing with err.
func unknownReport(got *[]UnknownKey, err error) SourceOption {
	return WithUnknownEntries(func(_ context.Context, keys []UnknownKey) error {
		*got = keys
		return err
	})
}

// Keys a source under WithUnknownEntries sets that no field reads go to its
// report and the load goes on: a removed field's stored row, and a key
// inside a stored section's value. The snapshot lists them as input the
// load accepted. The same keys in a file fail the load.
func TestUnknownEntries_ReportsTheSourcesUnknownKeys(t *testing.T) {
	var reported []UnknownKey
	entries := map[string]any{"prot": 8, "section": map[string]any{"host": "h", "hots": "x"}}
	snap, err := LoadSnapshot[unknownEntriesConfig](t.Context(), WithBaseEntries(readEntries(entries), unknownReport(&reported, nil)))
	require.NoError(t, err)
	assert.Equal(t, unknownSection{Host: "h"}, snap.Config.Section)
	want := []UnknownKey{{Key: "prot", Suggestion: "port"}, {Key: "section.hots", Suggestion: "section.host"}}
	assert.Equal(t, want, reported)
	require.NotNil(t, snap.UnknownKeys)
	assert.Equal(t, want, snap.UnknownKeys.Keys)

	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "prot: 8\nsection: {host: h, hots: x}\n")
	_, err = Load[unknownEntriesConfig](t.Context(), WithFile(file))
	var unknown *UnknownKeysError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, want, unknown.Keys)

	reported = nil
	_, err = Load[unknownEntriesConfig](t.Context(), WithBaseEntries(readEntries(entries), WithUnknownEntries(nil)))
	require.NoError(t, err, "a nil report accepts them silently")
}

// What decoding accepts is not unknown, so nothing is reported: a key
// beneath an interface, one a remain field collects, and the keys a
// self-decoding value reads itself, which reach it untouched.
func TestUnknownEntries_ReportsOnlyWhatDecodingLeavesUnused(t *testing.T) {
	var reported []UnknownKey
	got, err := Load[unknownEntriesConfig](t.Context(), WithBaseEntries(readEntries(map[string]any{
		"extra.anything": 1,
		"open.future":    2,
		"item":           map[string]any{"raw": 42},
	}), unknownReport(&reported, nil)))
	require.NoError(t, err)
	assert.Nil(t, reported)
	assert.Equal(t, map[string]any{"anything": 1}, got.Extra)
	assert.Equal(t, map[string]any{"future": 2}, got.Open.Rest)
	assert.Equal(t, 42, got.Item.Value)
}

// A report's error fails the load with ErrValidation, the error kept.
func TestUnknownEntries_ReportErrorFailsTheLoad(t *testing.T) {
	stale := errors.New("stale rows")
	var reported []UnknownKey
	_, err := Load[unknownEntriesConfig](t.Context(), WithBaseEntries(readEntries(map[string]any{"prot": 8}), unknownReport(&reported, stale)))
	require.ErrorIs(t, err, ErrValidation)
	require.ErrorIs(t, err, stale)
	assert.Equal(t, []UnknownKey{{Key: "prot", Suggestion: "port"}}, reported)
}

// A key the merge holds is claimed only when every source that set any
// part of it claims its unknown keys, a source set over included, and no
// default is there; the highest of those reports it. Otherwise it is the
// load's and fails it: a claim never hides the same key in another source,
// beneath it or above it.
func TestUnknownEntries_AKeyIsClaimedOnlyWhenEverySetterClaims(t *testing.T) {
	var lower, higher []UnknownKey
	_, err := Load[unknownEntriesConfig](t.Context(),
		WithBaseProvider(mapSource{"prot": 1, "legacy": map[string]any{"a": 1}}, nil, unknownReport(&lower, nil)),
		WithOverrideEntries(readEntries(map[string]any{"prot": 2, "legacy": map[string]any{"b": 1}}), unknownReport(&higher, nil)))
	require.NoError(t, err)
	assert.Nil(t, lower)
	assert.Equal(t, []UnknownKey{{Key: "legacy"}, {Key: "prot", Suggestion: "port"}}, higher)

	lower, higher = nil, nil
	_, err = Load[unknownEntriesConfig](t.Context(),
		WithBaseProvider(mapSource{"legacy": map[string]any{"a": 1}}, nil, unknownReport(&lower, nil)),
		WithOverrideProvider(mapSource{"legacy": map[string]any{"b": 1}}, nil, unknownReport(&higher, nil)))
	require.NoError(t, err)
	assert.Nil(t, lower)
	assert.Equal(t, []UnknownKey{{Key: "legacy"}}, higher)

	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "port: 5\nbogus: 1\n")
	var reported []UnknownKey
	claiming := func(data mapSource) Option { return WithOverrideProvider(data, nil, unknownReport(&reported, nil)) }
	for name, opts := range map[string][]Option{
		"a lower file sets it": {WithFile(file), claiming(mapSource{"bogus": 2})},
		"a lower source sets it": {
			WithBaseProvider(mapSource{"prot": 1}, nil),
			claiming(mapSource{"prot": 2}),
		},
		"a higher source sets it": {
			WithBaseProvider(mapSource{"prot": 1}, nil, unknownReport(&reported, nil)),
			WithOverrideProvider(mapSource{"prot": 2}, nil),
		},
		"a higher source sets a key of the section": {
			WithBaseProvider(mapSource{"legacy": map[string]any{"a": 1}}, nil, unknownReport(&reported, nil)),
			WithOverrideProvider(mapSource{"legacy": map[string]any{"b": 1}}, nil),
		},
		"a lower source sets a key of the section": {
			WithBaseProvider(mapSource{"legacy": map[string]any{"b": 1}}, nil),
			claiming(mapSource{"legacy": map[string]any{"a": 1}}),
		},
		"a default sets a key of the section": {
			WithDefaults(map[string]any{"legacy.b": 1}),
			claiming(mapSource{"legacy": map[string]any{"a": 1}}),
		},
	} {
		t.Run(name, func(t *testing.T) {
			reported = nil
			_, err := Load[unknownEntriesConfig](t.Context(), opts...)
			var unknown *UnknownKeysError
			require.ErrorAs(t, err, &unknown, "the load's, which fails on it")
			assert.Nil(t, reported)
		})
	}

	// A lower source's entry naming the key never merges, but it set the
	// key: the load's handler receives it, and the claiming source does not.
	reported = nil
	var loadWide []UnknownKey
	_, err = Load[unknownEntriesConfig](t.Context(),
		WithBaseEntries(readEntries(map[string]any{"bogus": 1})), claiming(mapSource{"bogus": 2}),
		WithUnknownKeys(func(_ context.Context, u *UnknownKeysError) error { loadWide = u.Keys; return nil }))
	require.NoError(t, err)
	assert.Nil(t, reported)
	assert.Equal(t, []UnknownKey{{Key: "bogus"}}, loadWide)
}

// With WithInvalidEntries on the same source, the keys inside a withheld
// entry's value go to this source's report too, and the load's handler
// sees none of them.
func TestUnknownEntries_WithInvalidEntriesReportsWithheldKeysToTheSource(t *testing.T) {
	var reported []UnknownKey
	var rejected []KeyError
	loadWide := false
	_, err := Load[unknownEntriesConfig](t.Context(),
		WithOverrideProvider(mapSource{
			"items":   []any{map[string]any{"port": "bad", "future": true}},
			"section": map[string]any{"host": "h", "hots": "x"},
		}, nil, unknownReport(&reported, nil), WithInvalidEntries(func(_ context.Context, entries []KeyError) error {
			rejected = entries
			return nil
		})),
		WithUnknownKeys(func(context.Context, *UnknownKeysError) error { loadWide = true; return nil }))
	require.NoError(t, err)
	require.Len(t, rejected, 1)
	assert.Equal(t, "items", rejected[0].Key)
	assert.Equal(t, []UnknownKey{{Key: "items[0].future"}, {Key: "section.hots", Suggestion: "section.host"}}, reported)
	assert.False(t, loadWide)
}
