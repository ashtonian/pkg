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
labels                             APP_LABELS                            map[string]string
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
	require.Len(t, rows, 7)
	assert.Equal(t, "features[*].max_page_size", rows[0].Key)
	assert.Equal(t, usageRow{Key: "name", Env: "APP_NAME", EnvVars: []string{"APP_NAME"}, Reload: ReloadRestart, Type: "string", Required: true}, rows[3])
	assert.Equal(t, usageRow{Key: "password", Env: "APP_PASSWORD", EnvVars: []string{"APP_PASSWORD"}, Reload: ReloadRestart, Type: "string", Withheld: true, Sensitive: true}, rows[4])
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
	assert.Equal(t, usageRow{Key: "settings", Env: "APP_SETTINGS", EnvVars: []string{"APP_SETTINGS"}, Reload: ReloadRestart, Type: "map[string]string", Withheld: true}, rows[0])
	assert.Equal(t, usageRow{Key: "settings[*]", Env: "APP_SETTINGS_<KEY>", EnvVars: []string{"APP_SETTINGS_<KEY>"}, Reload: ReloadRestart, Type: "string"}, rows[1])
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
	assert.Contains(t, buf.String(), `"withheld": true`)
}

func TestUsage_CatalogMatchesJSONAndSchemaEnums(t *testing.T) {
	type cfg struct {
		Mode     string `koanf:"mode" default:"hello world" validate:"oneof='hello world' goodbye" alias:"old_mode" env:"LEGACY_MODE" reload:"live"`
		Count    int    `koanf:"count" validate:"oneof=1 05 +7"`
		Password string `koanf:"password" default:"secret-value" validate:"oneof=secret-value other-secret"`
	}
	rows, err := Usage[cfg]("APP")
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, []string{"hello world", "goodbye"}, rows[0].Allowed)
	assert.Equal(t, []string{"APP_MODE", "LEGACY_MODE"}, rows[0].EnvVars)
	assert.Equal(t, []string{"old_mode"}, rows[0].Deprecated)
	assert.Equal(t, ReloadLive, rows[0].Reload)
	assert.Equal(t, "mode", rows[0].Field.Path)
	assert.Equal(t, []string{"1"}, rows[1].Allowed)
	assert.Empty(t, rows[2].Default)
	assert.Empty(t, rows[2].Allowed)
	assert.True(t, rows[2].Withheld)
	want, err := json.Marshal(rows)
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, PrintUsage[cfg]("APP", WithWriter(&out), WithFormat(FormatJSON)))
	assert.JSONEq(t, string(want), out.String())
	assert.NotContains(t, out.String(), "secret-value")
	assert.NotContains(t, out.String(), "other-secret")
	rows[0].EnvVars[0] = "changed"
	rows[0].Allowed[0] = "changed"
	rows[0].Field.Index[0] = 999
	again, err := Usage[cfg]("APP")
	require.NoError(t, err)
	assert.Equal(t, "APP_MODE", again[0].EnvVars[0])
	assert.Equal(t, "hello world", again[0].Allowed[0])
	assert.Equal(t, []int{0}, again[0].Field.Index)
}

type usageItem struct {
	Port int `koanf:"port"`
}

type usageFeature struct {
	Limit  int               `koanf:"limit"`
	Labels map[string]string `koanf:"labels"`
}

type usageVariablesCfg struct {
	Features map[string]usageFeature `koanf:"features"`
	ByID     map[int]string          `koanf:"by_id"`
	Items    []usageItem             `koanf:"items"`
}

func (c *usageVariablesCfg) SetDefaults() { c.Items = []usageItem{{Port: 80}} }

// Usage names a variable only where the loader reads one that can set the
// key: not an entry of a map inside a map entry, not a map keyed by
// anything but strings, and not a list of sections, with a default or not.
func TestUsage_NamesOnlyVariablesThatSetAKey(t *testing.T) {
	type cfg = usageVariablesCfg
	rows, err := Usage[cfg]("APP")
	require.NoError(t, err)
	env := map[string][]string{}
	for _, r := range rows {
		env[r.Key] = r.EnvVars
	}
	assert.Equal(t, map[string][]string{
		"features[*].limit":     {"APP_FEATURES_<KEY>_LIMIT"},
		"features[*].labels":    {"APP_FEATURES_<KEY>_LABELS"},
		"features[*].labels[*]": nil,
		"by_id":                 nil,
		"items":                 nil,
		"items[*].port":         nil,
	}, env)

	var tmpl bytes.Buffer
	require.NoError(t, PrintTemplate[cfg]("APP", WithWriter(&tmpl)))
	assert.NotContains(t, tmpl.String(), "APP_BY_ID")
	assert.NotContains(t, tmpl.String(), "APP_ITEMS")
	assert.NotContains(t, tmpl.String(), "APP_FEATURES_<KEY>_LABELS_<KEY>")
}

type usageColumnsCfg struct {
	Level   string        `koanf:"level" default:"info" validate:"oneof=debug info warn" desc:"Log level"`
	Timeout time.Duration `koanf:"timeout" default:"5s" validate:"min=1s" desc:"Timeout"`
	Rate    int           `koanf:"rate" default:"10" validate:"oneof=10 20,min=10" desc:"Rate"`
}

// A caller's column follows the variables, or the key without a prefix, in
// both the table and Markdown; Markdown's Bounds cell shows allowed values,
// then bounds, durations as durations.
func TestPrintUsage_Columns(t *testing.T) {
	flag := func(e UsageEntry) string { return "--" + e.Key }
	second := func(e UsageEntry) string { return strings.ToUpper(e.Key[:1]) }
	tests := []struct {
		name   string
		prefix string
		format string
		want   []string
	}{
		{"table", "APP", FormatTable, []string{
			"KEY      ENV          FLAG       INITIAL  TYPE           DEFAULT  DESCRIPTION",
			"level    APP_LEVEL    --level    L        string         info     Log level",
		}},
		{"table without prefix", "", FormatTable, []string{
			"KEY      FLAG       INITIAL  TYPE           DEFAULT  DESCRIPTION",
		}},
		{"markdown", "APP", FormatMarkdown, []string{
			"| Key | Environment | Flag | Initial | Type | Default | Bounds | Description |",
			"| level | APP_LEVEL | --level | L | string | info | one of debug, info, warn | Log level |",
			"| timeout | APP_TIMEOUT | --timeout | T | time.Duration | 5s | duration &gt;= 1s | Timeout |",
			"| rate | APP_RATE | --rate | R | int | 10 | one of 10, 20; value &gt;= 10 | Rate |",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, PrintUsage[usageColumnsCfg](tt.prefix, WithWriter(&buf), WithFormat(tt.format),
				WithUsageColumn("Flag", flag), WithUsageColumn("Initial", second)))
			for _, line := range tt.want {
				assert.Contains(t, normalizeSpaces(buf.String()), normalizeSpaces(line))
			}
			assert.NotContains(t, strings.ToLower(buf.String()), "reload")
		})
	}
}

// The Reload column appears once some key's reload class is not restart.
func TestPrintUsage_ReloadColumnWhenItInforms(t *testing.T) {
	type cfg struct {
		Port int `koanf:"port" default:"80"`
		Rate int `koanf:"rate" default:"5" reload:"live"`
	}
	for _, format := range []string{FormatTable, FormatMarkdown} {
		var buf bytes.Buffer
		require.NoError(t, PrintUsage[cfg]("APP", WithWriter(&buf), WithFormat(format)))
		assert.Contains(t, strings.ToLower(buf.String()), "reload", format)
		assert.Contains(t, buf.String(), "restart", format)
		assert.Contains(t, buf.String(), "live", format)
	}
}

// Table lines carry no trailing padding when the last cells are empty.
func TestPrintUsage_TableHasNoTrailingSpace(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, PrintUsage[usageCfg]("APP", WithWriter(&buf)))
	for _, line := range strings.Split(buf.String(), "\n") {
		assert.Equal(t, strings.TrimRight(line, " "), line)
	}
}
