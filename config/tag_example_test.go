package config

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type exampleCfg struct {
	Host     string        `koanf:"host" validate:"required" example:"db.internal" desc:"Database host"`
	Port     int           `koanf:"port" default:"5432" example:"6432"`
	Timeout  time.Duration `koanf:"timeout" example:"5s"`
	Replicas []string      `koanf:"replicas" example:"db-1,db-2"`
	Password string        `koanf:"password" example:"hunter2"`
}

// A starter file writes a key with no default as its example, commented
// out; a default is still the value, and a sensitive key's example is never
// shown.
func TestExample_StarterFile(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, PrintTemplate[exampleCfg]("APP", WithWriter(&out)))
	text := out.String()
	assert.Contains(t, text, "# host: db.internal\n")
	assert.Contains(t, text, "port: 5432\n")
	assert.Contains(t, text, "# timeout: 5s\n")
	assert.Contains(t, text, "# replicas:\n#   - db-1\n#   - db-2\n")
	assert.Contains(t, text, "# password:\n")
	assert.NotContains(t, text, "hunter2")
}

// Usage tables show an example where there is no default, and the JSON
// Schema states it typed; neither shows a sensitive key's.
func TestExample_UsageAndSchema(t *testing.T) {
	var table bytes.Buffer
	require.NoError(t, PrintUsage[exampleCfg]("APP", WithWriter(&table)))
	assert.Contains(t, table.String(), "(required) e.g. db.internal")
	assert.NotContains(t, table.String(), "hunter2")

	var env bytes.Buffer
	require.NoError(t, PrintUsage[exampleCfg]("APP", WithWriter(&env), WithFormat(FormatEnv)))
	assert.Contains(t, env.String(), "# export APP_HOST='db.internal'\n")
	assert.Contains(t, env.String(), "# export APP_PASSWORD=\n")

	raw, err := JSONSchema[exampleCfg]()
	require.NoError(t, err)
	var doc struct {
		Properties map[string]struct {
			Examples []any `json:"examples"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	assert.Equal(t, []any{"db.internal"}, doc.Properties["host"].Examples)
	assert.Equal(t, []any{float64(6432)}, doc.Properties["port"].Examples)
	assert.Equal(t, []any{[]any{"db-1", "db-2"}}, doc.Properties["replicas"].Examples)
	assert.Nil(t, doc.Properties["password"].Examples)
}

// An example its field cannot hold, or one on a section, is refused before
// anything loads, without quoting it.
func TestExample_Refused(t *testing.T) {
	type badType struct {
		Port int `koanf:"port" example:"not-a-port"`
	}
	_, err := Load[badType](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "example that does not decode into int")
	assert.NotContains(t, err.Error(), "not-a-port")

	type section struct {
		DB struct {
			Host string `koanf:"host"`
		} `koanf:"db" example:"x"`
	}
	_, err = Load[section](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "is a section")
}
