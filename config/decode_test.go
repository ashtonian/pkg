package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type plugin struct {
	Name    string        `koanf:"name" validate:"required"`
	Timeout time.Duration `koanf:"timeout" default:"5s"`
	Hosts   []string      `koanf:"hosts" alias:"servers"`
}

// Decode runs data through Load's pipeline: defaults beneath it, aliases,
// strict keys and validation; a source opts add loads above it.
func TestDecode(t *testing.T) {
	got, err := Decode[plugin](t.Context(), map[string]any{"name": "audit", "servers": "a,b"})
	require.NoError(t, err)
	assert.Equal(t, plugin{Name: "audit", Timeout: 5 * time.Second, Hosts: []string{"a", "b"}}, *got)

	got, err = Decode[plugin](t.Context(), map[string]any{"name": "audit"},
		WithDefaults(map[string]any{"timeout": "1s"}),
		WithEnvPrefix("APP"), WithEnviron(func() []string { return []string{"APP_NAME=override"} }))
	require.NoError(t, err)
	assert.Equal(t, "override", got.Name, "a variable loads above data")
	assert.Equal(t, time.Second, got.Timeout, "WithDefaults loads above the tag default")

	_, err = Decode[plugin](t.Context(), map[string]any{"nmae": "audit"})
	var unknown *UnknownKeysError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, "name", unknown.Keys[0].Suggestion)

	_, err = Decode[plugin](t.Context(), nil)
	require.ErrorIs(t, err, ErrValidation, "name is required")
}
