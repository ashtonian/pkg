package config

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type envTagCfg struct {
	Port   int               `koanf:"port" env:"PORT,HTTP_PORT" desc:"Listen port"`
	DSN    string            `koanf:"dsn" env:"DATABASE_URL"`
	Labels map[string]string `koanf:"labels" env:"LABELS"`
	Debug  bool              `koanf:"debug"`
}

func loadEnvTag(t *testing.T, environ ...string) (*envTagCfg, error) {
	t.Helper()
	return Load[envTagCfg](t.Context(), WithEnvPrefix("APP"), WithEnviron(func() []string { return environ }), WithSkipValidation())
}

// An env tag's variables are read as written, without the prefix. The
// prefixed variable wins over them, and an earlier tagged one over a later.
func TestEnvTag_ReadsNamedVariables(t *testing.T) {
	got, err := loadEnvTag(t, "HTTP_PORT=81", "PORT=80", "DATABASE_URL=postgres://db", "LABELS=team=core,tier=1")
	require.NoError(t, err)
	assert.Equal(t, 80, got.Port, "PORT is listed before HTTP_PORT")
	assert.Equal(t, "postgres://db", got.DSN)
	assert.Equal(t, map[string]string{"team": "core", "tier": "1"}, got.Labels)

	got, err = loadEnvTag(t, "PORT=80", "APP_PORT=9090", "HTTP_PORT=81")
	require.NoError(t, err)
	assert.Equal(t, 9090, got.Port, "the prefixed variable wins")

	got, err = loadEnvTag(t, "HTTP_PORT=81")
	require.NoError(t, err)
	assert.Equal(t, 81, got.Port)
}

// The environment is read only under WithEnvPrefix, tagged names included.
func TestEnvTag_NotReadWithoutAPrefix(t *testing.T) {
	got, err := Load[envTagCfg](t.Context(), WithEnviron(func() []string { return []string{"PORT=80"} }), WithSkipValidation())
	require.NoError(t, err)
	assert.Zero(t, got.Port)
}

// A tagged name that is another key's own variable could set either: the
// load fails naming both, as for any ambiguous variable.
func TestEnvTag_AmbiguousWithAnotherKey(t *testing.T) {
	type cfg struct {
		Port  int  `koanf:"port" env:"APP_DEBUG"`
		Debug bool `koanf:"debug"`
	}
	_, err := Load[cfg](t.Context(), WithEnvPrefix("APP"), WithEnviron(func() []string { return []string{"APP_DEBUG=1"} }), WithSkipValidation())
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "APP_DEBUG (could set debug or port)")
}

// An env tag that no variable can address is refused before anything loads.
func TestEnvTag_Refused(t *testing.T) {
	type section struct {
		DB struct {
			Host string `koanf:"host"`
		} `koanf:"db" env:"DB"`
	}
	_, err := Load[section](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "no variable can set a struct")

	type item struct {
		Port int `koanf:"port" env:"ITEM_PORT"`
	}
	type inList struct {
		Items []item `koanf:"items"`
	}
	_, err = Load[inList](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "items[*].port is a key of every list element or map entry")

	type listOfSections struct {
		Items []item `koanf:"items" env:"ITEMS"`
	}
	_, err = Load[listOfSections](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "no variable can set a []config.item")

	type twice struct {
		A string `koanf:"a" env:"SHARED"`
		B string `koanf:"b" env:"SHARED"`
	}
	_, err = Load[twice](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "a and b both name the environment variable SHARED")

	type badName struct {
		A string `koanf:"a" env:"PORT,2FAST"`
	}
	_, err = Load[badName](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), `"2FAST" is not a variable name`)
}

// Usage tables, starter files and EnvVars list every variable a key reads.
func TestEnvTag_Listed(t *testing.T) {
	fields := Fields[envTagCfg]()
	assert.Equal(t, []string{"APP_PORT", "PORT", "HTTP_PORT"}, fields[0].EnvVars("APP"))
	assert.Nil(t, fields[0].EnvVars(""))

	var usage bytes.Buffer
	require.NoError(t, PrintUsage[envTagCfg]("APP", WithWriter(&usage)))
	assert.Contains(t, usage.String(), "APP_PORT, PORT, HTTP_PORT")

	var export bytes.Buffer
	require.NoError(t, PrintUsage[envTagCfg]("APP", WithWriter(&export), WithFormat(FormatEnv)))
	assert.Contains(t, export.String(), "# export APP_PORT=\n", "an export line names one variable")

	var rows bytes.Buffer
	require.NoError(t, PrintUsage[envTagCfg]("APP", WithWriter(&rows), WithFormat(FormatJSON)))
	assert.Contains(t, rows.String(), `"env": "APP_PORT",
    "also_env": [
      "PORT",
      "HTTP_PORT"
    ]`)

	var tmpl bytes.Buffer
	require.NoError(t, PrintTemplate[envTagCfg]("APP", WithWriter(&tmpl)))
	assert.Contains(t, tmpl.String(), "env APP_PORT, PORT, HTTP_PORT")
	assert.Contains(t, tmpl.String(), "env APP_DSN, DATABASE_URL")
}
