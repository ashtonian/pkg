//go:build unit

package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type provItem struct {
	Port  int `koanf:"port"`
	Limit int `koanf:"limit" default:"50"`
}

type provDB struct {
	Host     string `koanf:"host" default:"localhost"`
	Port     int    `koanf:"port" default:"5432"`
	Password string `koanf:"password"`
}

type provCfg struct {
	Name     string              `koanf:"name" default:"app"`
	Region   string              `koanf:"region"`
	Zone     string              `koanf:"zone"`
	Replicas int                 `koanf:"replicas"`
	Debug    bool                `koanf:"debug"`
	Unset    string              `koanf:"unset"`
	DB       provDB              `koanf:"db"`
	Labels   map[string]string   `koanf:"labels"`
	Tags     []string            `koanf:"tags"`
	Items    map[string]provItem `koanf:"items"`
	Opt      *provItem           `koanf:"opt"`
}

func origin(kind SourceKind, name string) Origin {
	return Origin{Input: Source{Kind: kind, Name: name}, Exact: true}
}

// Every layer is attributed to the source the merge kept — a later source
// that sets the value a lower one had still owns it — and a key no source
// set is the zero value's.
func TestProvenance_AllLayersAndEqualAssignments(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "region: eu\ndb:\n  port: 5432\n  password: s3cret\n")
	snap, err := LoadSnapshot[provCfg](t.Context(),
		WithDefaults(map[string]any{"zone": "a"}),
		WithBaseProvider(confmap.Provider(map[string]any{"replicas": 2, "region": "us"}, "."), nil),
		WithFile(file),
		WithEnvPrefix("APP"),
		WithEnviron(func() []string { return []string{"APP_DEBUG=true"} }),
		WithOverrideProvider(confmap.Provider(map[string]any{"replicas": 3}, "."), nil),
		WithSkipValidation(),
	)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), snap.Generation)
	p := snap.Provenance
	assert.Equal(t, origin(SourceTag, ""), p["name"])
	assert.Equal(t, origin(SourceDefaults, ""), p["zone"])
	assert.Equal(t, origin(SourceFile, file), p["region"], "the file beats the base provider")
	assert.Equal(t, origin(SourceOverride, "0"), p["replicas"])
	assert.Equal(t, origin(SourceEnv, "APP_DEBUG"), p["debug"])
	assert.Equal(t, origin(SourceZero, ""), p["unset"])
	assert.Equal(t, origin(SourceTag, ""), p["db.host"])
	assert.Equal(t, origin(SourceFile, file), p["db.port"], "set to its default, by the file")
	assert.Equal(t, origin(SourceFile, file), p["db.password"])
}

// The merge is koanf's: maps merge entry by entry, an empty map changes
// nothing, a list is replaced whole, and an explicit null is the source's.
func TestProvenance_MapMergeListReplaceNull(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "labels:\n  team: core\ntags: [c]\nopt: null\n")
	base := map[string]any{
		"labels": map[string]any{"team": "x", "tier": "1"},
		"tags":   []any{"a", "b"},
		"opt":    map[string]any{"port": 1},
	}
	empty := map[string]any{"labels": map[string]any{}}
	snap, err := LoadSnapshot[provCfg](t.Context(),
		WithBaseProvider(confmap.Provider(base, ""), nil),
		WithFile(file),
		WithOverrideProvider(confmap.Provider(empty, ""), nil),
		WithSkipValidation(),
	)
	require.NoError(t, err)
	p := snap.Provenance
	assert.Equal(t, origin(SourceFile, file), p[`labels["team"]`])
	assert.Equal(t, origin(SourceBase, "0"), p[`labels["tier"]`], "an empty map over a map clears nothing")
	assert.Equal(t, origin(SourceFile, file), p["tags[0]"])
	_, stale := p["tags[1]"]
	assert.False(t, stale, "a replaced list leaves no element of the old one")
	assert.Equal(t, origin(SourceFile, file), p["opt"], "an explicit null is the file's")
	_, beneath := p["opt.port"]
	assert.False(t, beneath)
}

// An element default filled after the merge is the tag's, and a map decoded
// as a one-element list is that element.
func TestProvenance_ElementDefaultsAndReshaping(t *testing.T) {
	type cfg struct {
		Items map[string]provItem `koanf:"items"`
		List  []provItem          `koanf:"list"`
	}
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "items:\n  audit:\n    port: 1\nlist:\n  port: 2\n")
	snap, err := LoadSnapshot[cfg](t.Context(), WithFile(file), WithSkipValidation())
	require.NoError(t, err)
	p := snap.Provenance
	assert.Equal(t, origin(SourceFile, file), p[`items["audit"].port`])
	assert.Equal(t, origin(SourceTag, ""), p[`items["audit"].limit`])
	assert.Equal(t, origin(SourceFile, file), p["list[0].port"])
	assert.Equal(t, origin(SourceTag, ""), p["list[0].limit"])
}

// originProvider annotates its keys, and counts its reads.
type originProvider struct {
	data        map[string]any
	annotations map[string]Source
	reads       int
}

func (o *originProvider) Read() (map[string]any, error) {
	o.reads++
	return o.data, nil
}

func (o *originProvider) ReadBytes() ([]byte, error) { return nil, errors.New("map only") }

func (o *originProvider) ReadWithOrigins(context.Context) (map[string]any, map[string]Source, error) {
	o.reads++
	return o.data, o.annotations, nil
}

// An OriginProvider names its keys' sources, in one read; a key it does not
// annotate is the provider's, and an annotation naming no key it returned
// fails the load.
func TestProvenance_OriginProvider(t *testing.T) {
	op := &originProvider{
		data:        map[string]any{"db": map[string]any{"host": "h", "port": 1}, "labels": map[string]any{"team": "t"}},
		annotations: map[string]Source{"db.host": {Kind: SourceFlag, Name: "--db-host"}, "labels": {Kind: SourceFlag, Name: "--labels"}},
	}
	snap, err := LoadSnapshot[provCfg](t.Context(), WithOverrideProvider(op, nil), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, 1, op.reads, "one read for data and origins")
	p := snap.Provenance
	assert.Equal(t, origin(SourceFlag, "--db-host"), p["db.host"])
	assert.Equal(t, origin(SourceOverride, "0"), p["db.port"])
	assert.Equal(t, origin(SourceFlag, "--labels"), p[`labels["team"]`], "a section's annotation covers its keys")

	_, err = Load[provCfg](t.Context(), WithOverrideProvider(op, nil), WithSkipValidation())
	require.NoError(t, err)

	op.annotations = map[string]Source{"db.name": {Kind: SourceFlag, Name: "--db-name"}}
	_, err = LoadSnapshot[provCfg](t.Context(), WithOverrideProvider(op, nil), WithSkipValidation())
	require.ErrorIs(t, err, ErrSource)
	assert.Contains(t, err.Error(), "db.name")
}

// A stage that changes a value is listed after the input; one that writes
// the value the key already held is not.
func TestProvenance_Transforms(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "db:\n  password: from-file\n")
	snap, err := LoadSnapshot[provCfg](t.Context(),
		WithFile(file),
		WithSecretPopulator(func(_ context.Context, target any) error {
			target.(*provCfg).DB.Password = "from-store"
			return nil
		}),
		WithPostLoad(func(_ context.Context, c *provCfg) error {
			c.Name = "renamed"
			c.Region = "" // already empty: no change
			return nil
		}),
		WithSkipValidation(),
	)
	require.NoError(t, err)
	p := snap.Provenance
	assert.Equal(t, Origin{Input: Source{Kind: SourceFile, Name: file}, Transforms: []Source{{Kind: SourceSecrets}}, Exact: true}, p["db.password"])
	assert.Equal(t, Origin{Input: Source{Kind: SourceTag}, Transforms: []Source{{Kind: SourcePostLoad, Name: "0"}}, Exact: true}, p["name"])
	assert.Equal(t, origin(SourceZero, ""), p["region"])
}

// A reloader WithProvenance publishes the origins of each config with it,
// and a refused reload keeps the previous ones.
func TestProvenance_Reloader(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "region: eu\n")
	r, err := NewReloader[provCfg](t.Context(), []Option{WithFile(file), WithSkipValidation()}, WithProvenance())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(t.Context()) })
	assert.Equal(t, origin(SourceFile, file), r.Snapshot().Provenance["region"])

	writeFile(t, file, "replicas: 2\n")
	_, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, origin(SourceZero, ""), r.Snapshot().Provenance["region"])
	assert.Equal(t, origin(SourceFile, file), r.Snapshot().Provenance["replicas"])

	writeFile(t, file, "replicas: [\n")
	_, err = r.Reload(t.Context())
	require.ErrorIs(t, err, ErrReload)
	assert.Equal(t, origin(SourceFile, file), r.Snapshot().Provenance["replicas"])

	_, err = NewReloaderFunc(t.Context(), func(context.Context) (*provCfg, error) { return &provCfg{}, nil }, WithProvenance())
	require.Error(t, err)

	plain, err := NewReloader[provCfg](t.Context(), []Option{WithFile(file + ".absent"), WithSkipValidation()})
	require.NoError(t, err)
	assert.Nil(t, plain.Snapshot().Provenance, "recorded only on request")
}

func TestPrintExplain(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "region: eu\ndb:\n  password: SECRET_VALUE\n")
	snap, err := LoadSnapshot[provCfg](t.Context(), WithFile(file),
		WithEnvPrefix("APP"), WithEnviron(func() []string { return []string{"APP_REPLICAS=3"} }), WithSkipValidation())
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, PrintExplain(snap, WithWriter(&out)))
	text := out.String()
	assert.Contains(t, text, "region")
	assert.Contains(t, text, "file "+file)
	assert.Contains(t, text, "environment APP_REPLICAS")
	assert.Contains(t, text, "db.password")
	assert.Contains(t, text, "[REDACTED]")
	assert.NotContains(t, text, "SECRET_VALUE")

	out.Reset()
	require.NoError(t, PrintExplain(snap, WithWriter(&out), WithFormat(FormatJSON), WithSortByName()))
	var rows []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
		Input Source `json:"input"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
	require.NotEmpty(t, rows)
	assert.Equal(t, "db.host", rows[0].Key, "sorted by name")
	assert.NotContains(t, out.String(), "SECRET_VALUE")

	require.Error(t, PrintExplain(snap, WithFormat(FormatYAML)))
	require.Error(t, PrintExplain(Snapshot[provCfg]{Config: snap.Config}), "no provenance")
	require.Error(t, PrintExplain(snap, WithWriter(failingWriter{})))
}

func writeBenchFile(b *testing.B, path, data string) {
	b.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		b.Fatal(err)
	}
}
