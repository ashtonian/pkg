package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A map's default tag is key=value entries, which a file's entries merge
// with; values decode into the map's element type, and a variable holding
// the whole map reads the same way.
func TestMapDefault_EntriesMergeWithSources(t *testing.T) {
	type cfg struct {
		Labels  map[string]string  `koanf:"labels" default:"team=core,tier=backend"`
		Limits  map[string]int     `koanf:"limits" default:"read=100,write=10"`
		Routes  map[string]string  `koanf:"routes" default:"api=http://api:8080/v1?a=b"`
		Weights map[string]float64 `koanf:"weights"`
		Empty   map[string]string  `koanf:"empty" default:""`
	}
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "labels:\n  tier: frontend\n  owner: web\n")

	got, err := Load[cfg](t.Context(), WithFile(file),
		WithEnvPrefix("APP"), WithEnviron(func() []string { return []string{"APP_WEIGHTS=a=0.5,b=1.5"} }),
		WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"team": "core", "tier": "frontend", "owner": "web"}, got.Labels)
	assert.Equal(t, map[string]int{"read": 100, "write": 10}, got.Limits)
	assert.Equal(t, map[string]string{"api": "http://api:8080/v1?a=b"}, got.Routes)
	assert.Equal(t, map[string]float64{"a": 0.5, "b": 1.5}, got.Weights)
	assert.Equal(t, map[string]string{}, got.Empty)
	assert.Equal(t, map[string]any{"team": "core", "tier": "backend"}, DefaultsFromTags[cfg]()["labels"])
}

// A map default that is not key=value entries is refused before anything
// loads, naming the field and not the entry's text.
func TestMapDefault_MalformedIsASchemaError(t *testing.T) {
	type cfg struct {
		Labels map[string]string `koanf:"labels" default:"team=core,hunter2"`
	}
	_, err := Load[cfg](t.Context(), WithSkipValidation())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "field Labels")
	assert.Contains(t, err.Error(), "map entry 2 is not key=value")
	assert.NotContains(t, err.Error(), "hunter2")

	type twice struct {
		Labels map[string]string `koanf:"labels" default:"a=1,a=2"`
	}
	_, err = Load[twice](t.Context(), WithSkipValidation())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), `repeats a key`)
}
