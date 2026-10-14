package config

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sepCfg struct {
	Greetings []string `koanf:"greetings" sep:";" default:"hello, world;hi, there"`
	Ports     []int    `koanf:"ports" sep:"|" default:"80|443"`
	Whole     []string `koanf:"whole" sep:"none" default:"a,b"`
	Plain     []string `koanf:"plain" default:"a,b"`
	Routes    []struct {
		Methods []string `koanf:"methods" sep:" " default:"GET HEAD"`
	} `koanf:"routes"`
}

// A sep tag splits a list's default on its separator, or not at all with
// "none"; a field without one still splits on commas; an element field's sep
// applies to the default filled into every element.
func TestSep_Defaults(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "routes:\n  - {}\n")
	got, err := Load[sepCfg](t.Context(), WithFile(file), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, []string{"hello, world", "hi, there"}, got.Greetings)
	assert.Equal(t, []int{80, 443}, got.Ports)
	assert.Equal(t, []string{"a,b"}, got.Whole)
	assert.Equal(t, []string{"a", "b"}, got.Plain)
	require.Len(t, got.Routes, 1)
	assert.Equal(t, []string{"GET", "HEAD"}, got.Routes[0].Methods)
	assert.Equal(t, []any{"hello, world", "hi, there"}, DefaultsFromTags[sepCfg]()["greetings"])
}

// A string a file, variable or env tag sets for the field splits the same
// way; a list a source gives is left as it is.
func TestSep_Sources(t *testing.T) {
	type cfg struct {
		Greetings []string `koanf:"greetings" sep:";" env:"GREETINGS"`
		Routes    []struct {
			Methods []string `koanf:"methods" sep:" "`
		} `koanf:"routes"`
	}
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "routes:\n  - methods: GET POST\n  - methods: [PUT, DELETE]\n")
	got, err := Load[cfg](t.Context(), WithFile(file), WithEnvPrefix("APP"),
		WithEnviron(func() []string { return []string{"GREETINGS=hello, world;bye"} }), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, []string{"hello, world", "bye"}, got.Greetings)
	require.Len(t, got.Routes, 2)
	assert.Equal(t, []string{"GET", "POST"}, got.Routes[0].Methods)
	assert.Equal(t, []string{"PUT", "DELETE"}, got.Routes[1].Methods)
}

// Starter files, the JSON Schema and examples decode a tag's text with the
// field's separator too.
func TestSep_Outputs(t *testing.T) {
	type cfg struct {
		Greetings []string `koanf:"greetings" sep:";" default:"hello, world;hi"`
		Hosts     []string `koanf:"hosts" sep:";" example:"a.internal, b;c"`
	}
	var tmpl bytes.Buffer
	require.NoError(t, PrintTemplate[cfg]("", WithWriter(&tmpl)))
	assert.Contains(t, tmpl.String(), "greetings:\n  - hello, world\n  - hi\n")
	assert.Contains(t, tmpl.String(), "# hosts:\n#   - a.internal, b\n#   - c\n")

	raw, err := JSONSchema[cfg]()
	require.NoError(t, err)
	var doc struct {
		Properties map[string]struct {
			Default  any   `json:"default"`
			Examples []any `json:"examples"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	assert.Equal(t, []any{"hello, world", "hi"}, doc.Properties["greetings"].Default)
	assert.Equal(t, []any{[]any{"a.internal, b", "c"}}, doc.Properties["hosts"].Examples)
}

// A sep tag on anything but a list of values, or an empty one, is refused.
func TestSep_Refused(t *testing.T) {
	type onString struct {
		Name string `koanf:"name" sep:";"`
	}
	_, err := Load[onString](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "only a list of values is split")

	type onBytes struct {
		Raw []byte `koanf:"raw" sep:";"`
	}
	_, err = Load[onBytes](t.Context())
	require.ErrorIs(t, err, ErrSchema)

	type onStructs struct {
		Items []struct {
			X int `koanf:"x"`
		} `koanf:"items" sep:";"`
	}
	_, err = Load[onStructs](t.Context())
	require.ErrorIs(t, err, ErrSchema)

	type empty struct {
		Hosts []string `koanf:"hosts" sep:""`
	}
	_, err = Load[empty](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "empty sep tag")
}
