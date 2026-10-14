//go:build unit

package config

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/knadh/koanf/parsers/yaml"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tmplDB struct {
	Host     string `koanf:"host" default:"localhost" desc:"Database host"`
	MaxConns int    `koanf:"max_conns" default:"25"`
	Password string `koanf:"password" default:"dev-pass"`
}

type tmplLimits struct {
	Rate  int           `koanf:"rate" default:"100" desc:"Requests per second"`
	Burst time.Duration `koanf:"burst" default:"1s"`
}

type tmplItem struct {
	Name string `koanf:"name"`
	Port int    `koanf:"port" default:"80"`
}

type TmplBase struct {
	Region string `koanf:"region" default:"us-east-1"`
}

type tmplEmpty struct {
	X string `koanf:"x"`
}

type tmplCfg struct {
	TmplBase `koanf:",squash"`
	Name     string                `koanf:"name" validate:"required" desc:"Service name"`
	Port     int                   `koanf:"port" default:"8080" desc:"HTTP listen port"`
	Timeout  time.Duration         `koanf:"timeout" default:"30s"`
	Tags     []string              `koanf:"tags" default:"web,api"`
	Debug    bool                  `koanf:"debug" default:"false"`
	Ratio    float64               `koanf:"ratio" default:"0.5"`
	Started  time.Time             `koanf:"started" default:"2026-01-02T03:04:05Z"`
	DB       tmplDB                `koanf:"db" desc:"Primary database"`
	Opt      *tmplLimits           `koanf:"opt"`
	Empty    tmplEmpty             `koanf:"empty"`
	Features map[string]tmplLimits `koanf:"features"`
	Labels   map[string]string     `koanf:"labels"`
	Items    []tmplItem            `koanf:"items"`
	Value    any                   `koanf:"value"`
	Secret   string                `koanf:"secret" sensitive:"true" default:"s3"`
}

// A template loads as the defaults: the file a struct generates, loaded with
// strict keys, gives the config no source at all gives.
func TestPrintTemplate_LoadsAsTheDefaults(t *testing.T) {
	want, err := Load[tmplCfg](t.Context(), WithSkipValidation())
	require.NoError(t, err)
	for _, format := range []string{FormatYAML, FormatJSON} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, PrintTemplate[tmplCfg]("APP", WithWriter(&buf), WithFormat(format)))
			file := filepath.Join(t.TempDir(), "config."+format)
			writeFile(t, file, buf.String())
			got, err := Load[tmplCfg](t.Context(), WithRequiredFile(file), WithStrictKeys(), WithSkipValidation())
			require.NoError(t, err)
			changes, err := Diff(want, got)
			require.NoError(t, err)
			assert.Empty(t, changes)
		})
	}
}

func TestPrintTemplate_WritesEveryKey(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, PrintTemplate[tmplCfg]("APP", WithWriter(&buf)))
	out := buf.String()
	for _, want := range []string{
		"# HTTP listen port\n# int; env APP_PORT\nport: 8080\n",
		"timeout: 30s\n",
		"tags:\n  - web\n  - api\n",
		"started: \"2026-01-02T03:04:05Z\"\n",
		"# Service name\n# string, required; env APP_NAME\n# name:\n",
		"# Primary database\ndb:\n  # Database host\n  # string; env APP_DB_HOST\n  host: localhost\n",
		"  # string, sensitive; env APP_DB_PASSWORD\n  # password:\n",
		"opt:\n  # Requests per second\n  # int; env APP_OPT_RATE\n  rate: 100\n",
		"# empty:\n#   # string; env APP_EMPTY_X\n#   x:\n",
		"# features:\n#   <name>:\n#     # Requests per second\n#     # int; env APP_FEATURES_<KEY>_RATE\n#     rate: 100\n",
		"# labels:\n#   # env APP_LABELS_<KEY>\n#   <name>: <string>\n",
		"# items:\n#   # string\n#   - name:\n#     # int\n#     port: 80\n",
		"# string, sensitive; env APP_SECRET\n# secret:\n",
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "dev-pass")
	assert.NotContains(t, out, "s3\n")

	buf.Reset()
	require.NoError(t, PrintTemplate[tmplCfg]("", WithWriter(&buf)))
	assert.NotContains(t, buf.String(), "env ")
}

func TestPrintTemplate_RefusesADefaultThatDoesNotDecode(t *testing.T) {
	type cfg struct {
		Port int `koanf:"port" default:"eighty"`
	}
	err := PrintTemplate[cfg]("", WithWriter(&bytes.Buffer{}))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "'port'")
	require.Error(t, PrintTemplate[tmplCfg]("", WithWriter(&bytes.Buffer{}), WithFormat(FormatTable)))
}

func loadedTmpl(t *testing.T) *tmplCfg {
	t.Helper()
	cfg, err := Load[tmplCfg](t.Context(), WithSkipValidation())
	require.NoError(t, err)
	cfg.Name = "api"
	cfg.Features = map[string]tmplLimits{"audit": {Rate: 5, Burst: time.Second}}
	cfg.Labels = map[string]string{"team": "core", "a.b": "x"}
	cfg.Items = []tmplItem{{Name: "a", Port: 81}}
	cfg.DB.Password = "SECRET_DB"
	cfg.Secret = "SECRET_TAGGED"
	return cfg
}

// RedactedTree marshals to a file that loads back as the config, apart from
// what it redacted.
func TestRedactedTree_LoadsBack(t *testing.T) {
	cfg := loadedTmpl(t)
	tree := RedactedTree(cfg)
	assert.Equal(t, "30s", tree["timeout"])
	assert.Equal(t, int64(8080), tree["port"])
	assert.Equal(t, redactedValue, tree["secret"])
	assert.Equal(t, redactedValue, tree["db"].(map[string]any)["password"])

	b, err := yaml.Parser().Marshal(tree)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "SECRET_")
	got, err := Load[tmplCfg](t.Context(), WithBaseProvider(rawBytes(b), yaml.Parser()), WithStrictKeys(), WithSkipValidation())
	require.NoError(t, err)
	changes, err := Diff(cfg, got)
	require.NoError(t, err)
	for _, c := range changes {
		assert.True(t, c.Field.Sensitive, "only redacted keys differ: %s", c.Key)
	}
}

func TestPrintRedacted_YAML(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, PrintRedacted(loadedTmpl(t), WithWriter(&buf), WithFormat(FormatYAML)))
	out := buf.String()
	for _, want := range []string{
		"region: us-east-1\nname: api\nport: 8080\ntimeout: 30s\n",
		"db:\n  host: localhost\n  max_conns: 25\n  password: '[REDACTED]'\n",
		"features:\n  audit:\n    rate: 5\n    burst: 1s\n",
		"labels:\n  a.b: x\n  team: core\n",
		"items:\n  - name: a\n    port: 81\n",
		"value: null\n",
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "SECRET_")
}

// PrintEnv writes the variables the loader reads; loading them gives the
// config back, apart from secrets and keys no variable sets.
func TestPrintEnv(t *testing.T) {
	cfg := loadedTmpl(t)
	var buf bytes.Buffer
	require.NoError(t, PrintEnv(cfg, "APP", WithWriter(&buf)))
	out := buf.String()
	for _, want := range []string{
		"export APP_PORT='8080'\n",
		"export APP_TIMEOUT='30s'\n",
		"export APP_TAGS='web,api'\n",
		"export APP_DB_HOST='localhost'\n",
		"# db.password is sensitive and not shown\n# export APP_DB_PASSWORD=\n",
		"export APP_FEATURES_AUDIT_RATE='5'\n",
		"export APP_LABELS_TEAM='core'\n",
		`# labels["a.b"]: no variable sets it alone; set it in a config file` + "\n",
		"# items: no variable sets it; set it in a config file\n",
		"# value is null; no variable sets that\n",
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "SECRET_")

	var environ []string
	for line := range strings.Lines(out) {
		if rest, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "export "); ok {
			name, quoted, _ := strings.Cut(rest, "=")
			value := strings.ReplaceAll(strings.Trim(quoted, "'"), `'\''`, "'")
			environ = append(environ, name+"="+value)
		}
	}
	got, err := Load[tmplCfg](t.Context(), WithEnvPrefix("APP"), WithEnviron(func() []string { return environ }), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, cfg.Port, got.Port)
	assert.Equal(t, cfg.Tags, got.Tags)
	assert.Equal(t, cfg.Features["audit"], got.Features["audit"])
	assert.Equal(t, "core", got.Labels["team"])
	assert.Equal(t, cfg.Started, got.Started)

	require.Error(t, PrintEnv(cfg, ""))
}
