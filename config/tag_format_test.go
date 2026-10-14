package config

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type route struct {
	Path    string `koanf:"path"`
	Timeout string `koanf:"timeout" default:"5s" alias:"wait"`
}

type formatCfg struct {
	Routes []route          `koanf:"routes" format:"json" desc:"Routes to serve"`
	Limits map[string]route `koanf:"limits" format:"json"`
	Tags   []string         `koanf:"tags" format:"json" default:"[\"a,b\",\"c\"]"`
	DB     formatDB         `koanf:"db" format:"json" env:"DATABASE"`
}

type formatDB struct {
	Host string `koanf:"host" default:"localhost"`
	Port int    `koanf:"port" default:"5432"`
}

func loadFormat(t *testing.T, environ ...string) (*formatCfg, error) {
	t.Helper()
	return Load[formatCfg](t.Context(), WithEnvPrefix("APP"), WithEnviron(func() []string { return environ }), WithSkipValidation())
}

// A JSON string set for a format:"json" field is parsed before decoding, so
// its keys take the field's aliases and element defaults like any source's.
func TestFormatJSON_Variables(t *testing.T) {
	got, err := loadFormat(t,
		`APP_ROUTES=[{"path":"/a"},{"path":"/b","wait":"1s"}]`,
		`APP_LIMITS={"x":{"path":"/x"}}`,
		`DATABASE={"host":"db.internal"}`)
	require.NoError(t, err)
	assert.Equal(t, []route{{Path: "/a", Timeout: "5s"}, {Path: "/b", Timeout: "1s"}}, got.Routes)
	assert.Equal(t, map[string]route{"x": {Path: "/x", Timeout: "5s"}}, got.Limits)
	assert.Equal(t, formatDB{Host: "db.internal", Port: 5432}, got.DB)
	assert.Equal(t, []string{"a,b", "c"}, got.Tags, "the default is JSON too")
}

// A file may give the value itself, or its JSON text as a string.
func TestFormatJSON_Files(t *testing.T) {
	dir := t.TempDir()
	structured, text := filepath.Join(dir, "structured.yaml"), filepath.Join(dir, "text.yaml")
	writeFile(t, structured, "routes:\n  - path: /a\n")
	writeFile(t, text, "routes: '[{\"path\":\"/a\"}]'\n")
	for _, file := range []string{structured, text} {
		got, err := Load[formatCfg](t.Context(), WithFile(file), WithSkipValidation())
		require.NoError(t, err)
		assert.Equal(t, []route{{Path: "/a", Timeout: "5s"}}, got.Routes, file)
	}
}

// Keys inside the JSON are checked as any source's: an unknown one fails
// the load with its key and a suggestion.
func TestFormatJSON_StrictKeys(t *testing.T) {
	_, err := loadFormat(t, `APP_ROUTES=[{"pth":"/a"}]`)
	var unknown *UnknownKeysError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, []UnknownKey{{Key: "routes[0].pth", Suggestion: "routes[0].path"}}, unknown.Keys)
}

// Text that is not JSON fails with ErrValidation naming the key and the
// line, never quoting the text.
func TestFormatJSON_Malformed(t *testing.T) {
	_, err := loadFormat(t, "APP_ROUTES=[\n{\"path\": hunter2}]")
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "routes (line 2)")
	assert.NotContains(t, err.Error(), "hunter2")
}

// One variable for a section and another for a key inside it cannot both
// be set; JSON null is an explicit null.
func TestFormatJSON_SectionAndNull(t *testing.T) {
	_, err := loadFormat(t, `APP_DB={"host":"a"}`, `APP_DB_PORT=1`)
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "APP_DB and APP_DB_PORT set both a value and keys beneath it")

	got, err := loadFormat(t, `APP_ROUTES=null`)
	require.NoError(t, err)
	assert.Nil(t, got.Routes)
}

// Provenance names the variable that set each element; Diff compares
// element by element.
func TestFormatJSON_ProvenanceAndDiff(t *testing.T) {
	snap, err := LoadSnapshot[formatCfg](t.Context(), WithEnvPrefix("APP"), WithSkipValidation(),
		WithEnviron(func() []string { return []string{`APP_ROUTES=[{"path":"/a"}]`} }))
	require.NoError(t, err)
	assert.Equal(t, Source{Kind: SourceEnv, Name: "APP_ROUTES"}, snap.Provenance["routes[0].path"].Input)
	assert.Equal(t, SourceTag, snap.Provenance["routes[0].timeout"].Input.Kind)

	next := *snap.Config
	next.Routes = []route{{Path: "/b", Timeout: "5s"}}
	changes, err := Diff(snap.Config, &next)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, "routes[0].path", changes[0].Key)
}

// Usage tables, starter files, the JSON Schema and PrintEnv say a variable
// takes JSON, and PrintEnv writes it.
func TestFormatJSON_Outputs(t *testing.T) {
	var usage bytes.Buffer
	require.NoError(t, PrintUsage[formatCfg]("APP", WithWriter(&usage)))
	assert.Contains(t, usage.String(), "APP_ROUTES (JSON)")
	assert.Contains(t, usage.String(), "APP_DB, DATABASE (JSON)")

	var tmpl bytes.Buffer
	require.NoError(t, PrintTemplate[formatCfg]("APP", WithWriter(&tmpl)))
	assert.Contains(t, tmpl.String(), "env APP_ROUTES (JSON)")
	assert.Contains(t, tmpl.String(), "env APP_DB, DATABASE (JSON)")

	raw, err := JSONSchema[formatCfg]()
	require.NoError(t, err)
	var doc struct {
		Properties map[string]struct {
			AnyOf []map[string]any `json:"anyOf"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Len(t, doc.Properties["routes"].AnyOf, 2)
	assert.Equal(t, "application/json", doc.Properties["routes"].AnyOf[1]["contentMediaType"])

	cfg, err := loadFormat(t, `APP_ROUTES=[{"path":"/a"}]`)
	require.NoError(t, err)
	var env bytes.Buffer
	require.NoError(t, PrintEnv(cfg, "APP", WithWriter(&env)))
	assert.Contains(t, env.String(), `export APP_ROUTES='[{"path":"/a","timeout":"5s"}]'`)
	assert.Contains(t, env.String(), `export APP_TAGS='["a,b","c"]'`)

	type secretRoutes struct {
		Routes []struct {
			Token string `koanf:"token"`
		} `koanf:"routes" format:"json"`
	}
	sec, err := Load[secretRoutes](t.Context(), WithEnvPrefix("APP"), WithSkipValidation(),
		WithEnviron(func() []string { return []string{`APP_ROUTES=[{"token":"hunter2"}]`} }))
	require.NoError(t, err)
	env.Reset()
	require.NoError(t, PrintEnv(sec, "APP", WithWriter(&env)))
	assert.Contains(t, env.String(), "# routes holds sensitive values and is not shown")
	assert.NotContains(t, env.String(), "hunter2")
}

// A format tag that cannot mean what it says, and a default on a section,
// are refused before anything loads.
func TestFormatJSON_Refused(t *testing.T) {
	type anotherFormat struct {
		Routes []route `koanf:"routes" format:"yaml"`
	}
	type scalar struct {
		Port int `koanf:"port" format:"json"`
	}
	type withSep struct {
		Tags []string `koanf:"tags" format:"json" sep:";"`
	}
	type badDefault struct {
		Routes []route `koanf:"routes" format:"json" default:"[{\"pth\":\"/a\"}]"`
	}
	type sectionDefault struct {
		DB formatDB `koanf:"db" default:"x"`
	}
	for want, err := range map[string]error{
		`the one format is "json"`:               loadErr[anotherFormat](t),
		"for a section, map, list or interface":  loadErr[scalar](t),
		"JSON is not split":                      loadErr[withSep](t),
		"has a default that does not decode":     loadErr[badDefault](t),
		"is a section; give its fields defaults": loadErr[sectionDefault](t),
	} {
		require.ErrorIs(t, err, ErrSchema, want)
		assert.Contains(t, err.Error(), want)
	}
}

func loadErr[T any](t *testing.T) error {
	_, err := Load[T](t.Context())
	return err
}
