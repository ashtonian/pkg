package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type usageLimits struct {
	MaxPageSize int `koanf:"max_page_size" default:"50" desc:"Largest page"`
}

type usageCfg struct {
	Port     int                    `koanf:"port" default:"8080" desc:"HTTP listen port"`
	Timeout  time.Duration          `koanf:"timeout" default:"30s"`
	Name     string                 `koanf:"name" validate:"required"`
	Password string                 `koanf:"password" default:"hunter2"`
	Features map[string]usageLimits `koanf:"features"`
	Labels   map[string]string      `koanf:"labels"`
}

func TestPrintUsage_Table(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, PrintUsage[usageCfg]("APP", WithWriter(&buf)))
	want := `KEY                                ENV                                   TYPE               DEFAULT     DESCRIPTION
port                               APP_PORT                              int                8080        HTTP listen port
timeout                            APP_TIMEOUT                           time.Duration      30s
name                               APP_NAME                              string             (required)
password                           APP_PASSWORD                          string             [REDACTED]
features[*].max_page_size          APP_FEATURES_<KEY>_MAX_PAGE_SIZE      int                50          Largest page
labels[*]                          APP_LABELS_<KEY>                      string
`
	assert.Equal(t, normalizeSpaces(want), normalizeSpaces(buf.String()))
}

func TestPrintUsage_WithoutPrefixHasNoEnvColumn(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, PrintUsage[usageCfg]("", WithWriter(&buf)))
	assert.NotContains(t, buf.String(), "APP_")
	assert.NotContains(t, buf.String(), "ENV")
	assert.NotContains(t, buf.String(), "hunter2")
}

func TestPrintUsage_JSON(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, PrintUsage[usageCfg]("APP_", WithWriter(&buf), WithFormat(FormatJSON), WithSortByName()))
	var rows []usageRow
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rows))
	require.Len(t, rows, 6)
	assert.Equal(t, "features[*].max_page_size", rows[0].Key)
	assert.Equal(t, usageRow{Key: "name", Env: "APP_NAME", Type: "string", Required: true}, rows[2])
	assert.Equal(t, usageRow{Key: "password", Env: "APP_PASSWORD", Type: "string", Default: "[REDACTED]", Sensitive: true}, rows[3])
}

// The env template exports keys with a default, and comments out the rest:
// no default, a sensitive key, a map entry.
func TestPrintUsage_EnvTemplate(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, PrintUsage[usageCfg]("APP", WithWriter(&buf), WithFormat(FormatEnv)))
	out := buf.String()
	for _, want := range []string{
		"# HTTP listen port\n# port (int)\nexport APP_PORT='8080'\n",
		"# name (string), required\n# export APP_NAME=\n",
		"# password (string)\n# export APP_PASSWORD=\n",
		"# export APP_FEATURES_<KEY>_MAX_PAGE_SIZE=\n",
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "hunter2")

	err := PrintUsage[usageCfg]("", WithFormat(FormatEnv), WithWriter(&buf))
	require.Error(t, err)
}

func TestField_SensitiveAndEnvVar(t *testing.T) {
	type vault struct {
		Addr string `koanf:"addr"`
	}
	type auth struct {
		Issuer string `koanf:"issuer"`
		Secret string `koanf:"secret"`
	}
	type cfg struct {
		Vault  vault  `koanf:"vault" sensitive:"true"`
		Auth   auth   `koanf:"auth"`
		APIKey string `koanf:"api_key"`
		Host   string `koanf:"host"`
	}
	got := map[string]bool{}
	for _, f := range Fields[cfg]() {
		got[f.Path] = f.Sensitive
	}
	assert.Equal(t, map[string]bool{
		"vault.addr":  true,  // beneath a tagged struct
		"auth.issuer": false, // a struct's name does not cover it
		"auth.secret": true,
		"api_key":     true,
		"host":        false,
	}, got)

	f := Field{Path: "db.max_conns"}
	assert.Equal(t, "APP_DB_MAX_CONNS", f.EnvVar("APP"))
	assert.Equal(t, "APP_DB_MAX_CONNS", f.EnvVar("APP_"))
	assert.Empty(t, f.EnvVar(""))
}

func normalizeSpaces(s string) string {
	var lines []string
	for line := range strings.Lines(s) {
		lines = append(lines, strings.Join(strings.Fields(line), " "))
	}
	return strings.Join(lines, "\n")
}

// A default whose type can hold sensitive entries — a map, here — is
// withheld: only a decoded value reveals its keys. A collection's own default
// shows on the collection's row; its entries' rows carry none.
func TestPrintUsage_WithholdsCompositeDefaults(t *testing.T) {
	type cfg struct {
		Settings map[string]string `koanf:"settings" default:"{\"password\":\"FAKE_SECRET_VALUE\"}"`
		Tags     []string          `koanf:"tags" default:"web,api"`
	}
	var buf bytes.Buffer
	require.NoError(t, PrintUsage[cfg]("APP", WithWriter(&buf), WithFormat(FormatJSON)))
	assert.NotContains(t, buf.String(), "FAKE_SECRET_VALUE")
	var rows []usageRow
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rows))
	require.Len(t, rows, 3)
	assert.Equal(t, usageRow{Key: "settings", Env: "APP_SETTINGS", Type: "map[string]string", Default: "[REDACTED]"}, rows[0])
	assert.Equal(t, usageRow{Key: "settings[*]", Env: "APP_SETTINGS_<KEY>", Type: "string"}, rows[1])
	assert.Equal(t, "web,api", rows[2].Default, "a slice of scalars has no keys to hide")

	f := Fields[cfg]()[0]
	_, printable := f.PrintableDefault()
	assert.False(t, printable)
}

// The env template never exports a withheld default: the line is commented
// out, so sourcing the template cannot set a variable to a placeholder.
func TestPrintUsage_EnvTemplateCommentsOutWithheldDefaults(t *testing.T) {
	type cfg struct {
		Settings map[string]string `koanf:"settings" default:"{\"password\":\"FAKE_SECRET_VALUE\"}"`
		Any      any               `koanf:"any" default:"FAKE_SECRET_VALUE"`
	}
	var buf bytes.Buffer
	require.NoError(t, PrintUsage[cfg]("APP", WithFormat(FormatEnv), WithWriter(&buf)))
	out := buf.String()
	assert.NotContains(t, out, "FAKE_SECRET_VALUE")
	assert.NotContains(t, out, "\nexport APP_SETTINGS=")
	assert.NotContains(t, out, "\nexport APP_ANY=")
	assert.Contains(t, out, "# export APP_SETTINGS=")
}

// remainRow is a slice element whose remain map holds any key a source
// writes, so a default for the slice is withheld.
type remainRow struct {
	Extra map[string]string `koanf:",remain"`
}

func TestPrintUsage_WithholdsRemainDefaults(t *testing.T) {
	type cfg struct {
		Rows []remainRow `koanf:"rows" default:"[{\"password\":\"FAKE_SECRET_VALUE\"}]"`
	}
	var buf bytes.Buffer
	require.NoError(t, PrintUsage[cfg]("APP", WithFormat(FormatJSON), WithWriter(&buf)))
	assert.NotContains(t, buf.String(), "FAKE_SECRET_VALUE")
	assert.Contains(t, buf.String(), `"default": "[REDACTED]"`)
}
