package config

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type layeredDB struct {
	Host string `koanf:"host"`
	Port int    `koanf:"port"`
}

type layeredCfg struct {
	DB     layeredDB         `koanf:"db"`
	Port   int               `koanf:"port" env:"PORT"`
	Labels map[string]string `koanf:"labels"`
}

func environ(vars ...string) Option {
	return WithEnviron(func() []string { return vars })
}

// Each key reads the most specific prefix that sets it, then its env tag's
// names; keys a narrower prefix leaves unset come from a broader one.
func TestEnvPrefixes_Precedence(t *testing.T) {
	for _, tt := range []struct {
		name    string
		environ []string
		want    layeredCfg
	}{
		{
			name:    "the narrower prefix wins",
			environ: []string{"APP_DB_HOST=shared", "APP_WORKER_DB_HOST=worker"},
			want:    layeredCfg{DB: layeredDB{Host: "worker"}},
		},
		{
			name:    "a key the narrower prefix leaves unset is the shared one",
			environ: []string{"APP_WORKER_DB_HOST=worker", "APP_DB_PORT=5432"},
			want:    layeredCfg{DB: layeredDB{Host: "worker", Port: 5432}},
		},
		{
			name:    "every prefixed name outranks an env tag's",
			environ: []string{"PORT=80", "APP_PORT=81"},
			want:    layeredCfg{Port: 81},
		},
		{
			name:    "an env tag's name is read when no prefix sets the key",
			environ: []string{"PORT=80"},
			want:    layeredCfg{Port: 80},
		},
		{
			name:    "map entries merge across prefixes, the narrower winning on a shared key",
			environ: []string{"APP_LABELS_TEAM=core", "APP_LABELS_TIER=1", "APP_WORKER_LABELS_TIER=2"},
			want:    layeredCfg{Labels: map[string]string{"team": "core", "tier": "2"}},
		},
		{
			name:    "a whole map under the narrower prefix replaces the shared one",
			environ: []string{"APP_LABELS=team=core", "APP_WORKER_LABELS=team=worker"},
			want:    layeredCfg{Labels: map[string]string{"team": "worker"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load[layeredCfg](t.Context(), WithEnvPrefixes("APP_WORKER", "APP"), environ(tt.environ...))
			require.NoError(t, err)
			assert.Equal(t, tt.want, *got)
		})
	}
}

// Prefixes are read as WithEnvPrefix reads one: a trailing "_" is optional,
// and empty or repeated prefixes are left out.
func TestEnvPrefixes_List(t *testing.T) {
	assert.Equal(t, []string{"APP_WORKER", "APP"}, envPrefixList([]string{"APP_WORKER_", "", "APP", "APP_", "APP_WORKER"}))
	assert.Nil(t, envPrefixList([]string{"", "_"}))

	got, err := Load[layeredCfg](t.Context(), WithEnvPrefixes("", "APP_"), environ("APP_DB_HOST=h"))
	require.NoError(t, err)
	assert.Equal(t, "h", got.DB.Host, "an empty service prefix leaves the shared one")

	got, err = Load[layeredCfg](t.Context(), WithEnvPrefixes(), environ("APP_DB_HOST=h", "PORT=80"))
	require.NoError(t, err)
	assert.Zero(t, *got, "with no prefix the environment, env tags included, is not read")

	got, err = Load[layeredCfg](t.Context(), WithEnvPrefixes("APP_WORKER", "APP"), WithEnvPrefix("APP"), environ("APP_WORKER_DB_HOST=w", "APP_DB_HOST=h"))
	require.NoError(t, err)
	assert.Equal(t, "h", got.DB.Host, "the last call wins")
}

// A variable under a broader prefix that a narrower prefix's key reads is
// not unknown; one only a sibling service reads can be claimed, so a strict
// handler still reports a misspelling, with the variable it most likely
// meant.
func TestEnvPrefixes_UnknownVariables(t *testing.T) {
	var reported *UnknownKeysError
	strict := WithUnknownKeys(func(_ context.Context, unknown *UnknownKeysError) error {
		reported = unknown
		return unknown
	})
	_, err := Load[layeredCfg](t.Context(), WithEnvPrefixes("APP_WORKER", "APP"), strict,
		environ("APP_WORKER_DB_HOST=w", "APP_DB_HOST=h"))
	require.NoError(t, err, "APP_WORKER_DB_HOST is under APP too, and APP_WORKER's db.host reads it")

	_, err = Load[layeredCfg](t.Context(), WithEnvPrefixes("APP_WORKER", "APP"), strict,
		WithVariableFilter(func(name string) bool { return strings.HasPrefix(name, "APP_API_") }),
		environ("APP_API_DB_HOST=api", "APP_WORKER_DB_HOTS=typo"))
	require.ErrorIs(t, err, ErrValidation)
	require.NotNil(t, reported)
	assert.Equal(t, []UnknownKey{{Key: "APP_WORKER_DB_HOTS", Suggestion: "APP_WORKER_DB_HOST"}}, reported.Variables)
}

// A name that reads one key under one prefix and another key under a second
// fails the load naming every reading and where it comes from.
func TestEnvPrefixes_AmbiguousAcrossPrefixes(t *testing.T) {
	type worker struct {
		DB layeredDB `koanf:"db"`
	}
	type cfg struct {
		DB     layeredDB `koanf:"db"`
		Worker worker    `koanf:"worker"`
		Port   int       `koanf:"port" env:"APP_WORKER_DB_PORT"`
	}
	_, err := Load[cfg](t.Context(), WithEnvPrefixes("APP_WORKER", "APP"), environ("APP_WORKER_DB_HOST=h"))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "APP_WORKER_DB_HOST (could set db.host under APP_WORKER or worker.db.host under APP)")

	_, err = Load[cfg](t.Context(), WithEnvPrefixes("APP_WORKER", "APP"), environ("APP_WORKER_DB_PORT=1"))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "APP_WORKER_DB_PORT (could set db.port under APP_WORKER or port by its env tag or worker.db.port under APP)")
}

// A whole map or format:"json" section and a key beneath it cannot both be
// set, under one prefix or across two: the load fails naming both variables.
func TestEnvPrefixes_ValueAndKeysBeneathIt(t *testing.T) {
	type cfg struct {
		DB     layeredDB         `koanf:"db" format:"json"`
		Labels map[string]string `koanf:"labels"`
	}
	for _, tt := range []struct {
		name    string
		environ []string
		names   []string
	}{
		{"shared map, narrower entry", []string{"APP_LABELS=team=core", "APP_WORKER_LABELS_TIER=2"}, []string{"APP_LABELS", "APP_WORKER_LABELS_TIER"}},
		{"narrower map, shared entry", []string{"APP_WORKER_LABELS=team=core", "APP_LABELS_TIER=2"}, []string{"APP_WORKER_LABELS", "APP_LABELS_TIER"}},
		{"shared section, narrower key", []string{`APP_DB={"host":"h"}`, "APP_WORKER_DB_PORT=1"}, []string{"APP_DB", "APP_WORKER_DB_PORT"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load[cfg](t.Context(), WithEnvPrefixes("APP_WORKER", "APP"), environ(tt.environ...))
			require.ErrorIs(t, err, ErrValidation)
			assert.Contains(t, err.Error(), "set both a value and keys beneath it")
			for _, name := range tt.names {
				assert.Contains(t, err.Error(), name)
			}
		})
	}
}

// Provenance names the variable behind each value and the prefix it was
// read under; an env tag's name has none.
func TestEnvPrefixes_Provenance(t *testing.T) {
	snap, err := LoadSnapshot[layeredCfg](t.Context(), WithEnvPrefixes("APP_WORKER", "APP"),
		environ("APP_WORKER_DB_HOST=w", "APP_DB_HOST=h", "APP_DB_PORT=1", "PORT=80", "APP_WORKER_LABELS_TEAM=core"))
	require.NoError(t, err)
	env := func(name, prefix string) Origin {
		return Origin{Input: Source{Kind: SourceEnv, Name: name, Prefix: prefix}, Exact: true}
	}
	assert.Equal(t, env("APP_WORKER_DB_HOST", "APP_WORKER"), snap.Provenance["db.host"])
	assert.Equal(t, env("APP_DB_PORT", "APP"), snap.Provenance["db.port"])
	assert.Equal(t, env("PORT", ""), snap.Provenance["port"])
	assert.Equal(t, env("APP_WORKER_LABELS_TEAM", "APP_WORKER"), snap.Provenance[`labels["team"]`])
}

// A reloader reads every prefix again on each reload.
func TestEnvPrefixes_Reload(t *testing.T) {
	var mu sync.Mutex
	vars := []string{"APP_DB_HOST=shared"}
	r, err := NewReloader[layeredCfg](t.Context(), []Option{
		WithEnvPrefixes("APP_WORKER", "APP"),
		WithEnviron(func() []string {
			mu.Lock()
			defer mu.Unlock()
			return vars
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, "shared", r.Config().DB.Host)

	mu.Lock()
	vars = []string{"APP_DB_HOST=shared", "APP_WORKER_DB_HOST=worker"}
	mu.Unlock()
	_, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "worker", r.Config().DB.Host)

	mu.Lock()
	vars = []string{"APP_DB_HOST=moved"}
	mu.Unlock()
	_, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "moved", r.Config().DB.Host, "a narrower variable removed falls back to the shared one")
}

// Every diagnostic names a key's variables in the order a load reads them.
func TestEnvPrefixes_Diagnostics(t *testing.T) {
	fields := Fields[layeredCfg]()
	assert.Equal(t, []string{"APP_WORKER_PORT", "APP_PORT", "PORT"}, fields[2].EnvVars("APP_WORKER", "APP_"))
	assert.Equal(t, "APP_WORKER_PORT", fields[2].EnvVar("APP_WORKER"))
	assert.Nil(t, fields[2].EnvVars())

	rows, err := Usage[layeredCfg]("APP_WORKER", "APP")
	require.NoError(t, err)
	byKey := map[string]UsageEntry{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	port := byKey["port"]
	assert.Equal(t, "APP_WORKER_PORT", port.Env)
	assert.Equal(t, []string{"APP_PORT", "PORT"}, port.AlsoEnv)
	assert.Equal(t, []string{"APP_WORKER_PORT", "APP_PORT", "PORT"}, port.EnvVars)
	assert.Equal(t, []string{"APP_WORKER_LABELS_<KEY>", "APP_LABELS_<KEY>"}, byKey["labels[*]"].EnvVars)
	assert.Equal(t, []string{"APP_DB_HOST"}, byKey["db.host"].AlsoEnv, "no env tag: only the broader prefix")

	var table bytes.Buffer
	require.NoError(t, PrintUsage[layeredCfg]("APP_WORKER", WithFallbackEnvPrefixes("APP"), WithWriter(&table)))
	assert.Contains(t, table.String(), "APP_WORKER_DB_HOST, APP_DB_HOST")
	assert.Contains(t, table.String(), "APP_WORKER_PORT, APP_PORT, PORT")

	var markdown bytes.Buffer
	require.NoError(t, PrintUsage[layeredCfg]("APP_WORKER", WithFallbackEnvPrefixes("APP"), WithWriter(&markdown), WithFormat(FormatMarkdown)))
	assert.Contains(t, markdown.String(), "| APP_WORKER_PORT, APP_PORT, PORT |")

	var exports bytes.Buffer
	require.NoError(t, PrintUsage[layeredCfg]("", WithFallbackEnvPrefixes("APP_WORKER", "APP"), WithWriter(&exports), WithFormat(FormatEnv)))
	assert.Contains(t, exports.String(), "# export APP_WORKER_PORT=\n", "an export names the most specific variable")

	var tmpl bytes.Buffer
	require.NoError(t, PrintTemplate[layeredCfg]("APP_WORKER", WithFallbackEnvPrefixes("APP"), WithWriter(&tmpl)))
	assert.Contains(t, tmpl.String(), "# int; env APP_WORKER_PORT, APP_PORT, PORT\n")
	assert.Contains(t, tmpl.String(), "# env APP_WORKER_LABELS_<KEY>, APP_LABELS_<KEY>\n")

	raw, err := JSONSchema[layeredCfg]("APP_WORKER", "APP")
	require.NoError(t, err)
	var schema struct {
		Properties map[string]struct {
			Env        []string `json:"x-config-env"`
			Properties map[string]struct {
				Env []string `json:"x-config-env"`
			} `json:"properties"`
			AnyOf []struct {
				AdditionalProperties struct {
					Env []string `json:"x-config-env"`
				} `json:"additionalProperties"`
			} `json:"anyOf"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	assert.Equal(t, []string{"APP_WORKER_PORT", "APP_PORT", "PORT"}, schema.Properties["port"].Env)
	assert.Equal(t, []string{"APP_WORKER_DB_HOST", "APP_DB_HOST"}, schema.Properties["db"].Properties["host"].Env)
	assert.Nil(t, schema.Properties["db"].Env, "no variable sets a section whole")
	assert.Equal(t, []string{"APP_WORKER_LABELS", "APP_LABELS"}, schema.Properties["labels"].Env)
	assert.Equal(t, []string{"APP_WORKER_LABELS_<KEY>", "APP_LABELS_<KEY>"}, schema.Properties["labels"].AnyOf[0].AdditionalProperties.Env)

	raw, err = JSONSchema[layeredCfg]()
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "x-config-env", "without a prefix the environment is not read")
}

// PrintEnv writes each key under the first prefix, and leaves out a name
// that another key reads under any prefix of the load.
func TestEnvPrefixes_PrintEnv(t *testing.T) {
	type worker struct {
		DB layeredDB `koanf:"db"`
	}
	type cfg struct {
		DB     layeredDB `koanf:"db"`
		Worker worker    `koanf:"worker"`
	}
	c := cfg{DB: layeredDB{Host: "h", Port: 1}, Worker: worker{DB: layeredDB{Host: "w"}}}

	var single bytes.Buffer
	require.NoError(t, PrintEnv(c, "APP_WORKER", WithWriter(&single)))
	assert.Contains(t, single.String(), "export APP_WORKER_DB_HOST='h'\n", "alone, APP_WORKER reads it as db.host only")

	var layered bytes.Buffer
	require.NoError(t, PrintEnv(c, "APP_WORKER", WithFallbackEnvPrefixes("APP"), WithWriter(&layered)))
	out := layered.String()
	assert.Contains(t, out, "# db.host: no variable sets it alone; set it in a config file\n", "under APP it is worker.db.host")
	assert.Contains(t, out, "export APP_WORKER_WORKER_DB_HOST='w'\n")

	assert.EqualError(t, PrintEnv(c, "", WithFallbackEnvPrefixes("")), "config: PrintEnv needs an environment prefix")
}
