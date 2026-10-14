//go:build unit

package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawBytes is a byte-returning provider, the shape of a file or HTTP source.
type rawBytes []byte

func (b rawBytes) ReadBytes() ([]byte, error)  { return b, nil }
func (rawBytes) Read() (map[string]any, error) { return nil, errors.New("rawBytes needs a parser") }

// kvParser parses "key=value" lines, standing in for any non-YAML format.
type kvParser struct{}

func (kvParser) Unmarshal(b []byte) (map[string]any, error) {
	out := map[string]any{}
	for line := range strings.Lines(string(b)) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, errors.New("kv: line without '='")
		}
		out[k] = v
	}
	return out, nil
}

func (kvParser) Marshal(map[string]any) ([]byte, error) { return nil, errors.New("not used") }

type layered struct {
	A string `koanf:"a" default:"tag"`
	B string `koanf:"b" default:"tag"`
	C string `koanf:"c" default:"tag"`
	D string `koanf:"d" default:"tag"`
	E string `koanf:"e" default:"tag"`
}

// Every source sits at its documented layer: tag defaults < WithDefaults <
// base providers < files < environment < override providers.
func TestLoad_SourcePrecedence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "c: file\nd: file\ne: file\n")
	got, err := Load[layered](t.Context(),
		WithDefaults(map[string]any{"a": "defaults", "b": "defaults", "c": "defaults", "d": "defaults", "e": "defaults"}),
		WithBaseProvider(rawBytes("b: base\nc: base\nd: base\ne: base\n"), yaml.Parser()),
		WithFile(file),
		WithEnvPrefix("LAYER"),
		WithEnviron(func() []string { return []string{"LAYER_D=env", "LAYER_E=env"} }),
		WithOverrideProvider(confmap.Provider(map[string]any{"e": "override"}, "."), nil),
		WithSkipValidation(),
	)
	require.NoError(t, err)
	assert.Equal(t, layered{A: "defaults", B: "base", C: "file", D: "env", E: "override"}, *got)
}

// ctxProvider records the context its read was given.
type ctxProvider struct {
	got *context.Context
}

func (p ctxProvider) Read() (map[string]any, error) { return nil, errors.New("read without a context") }
func (p ctxProvider) ReadBytes() ([]byte, error)    { return nil, errors.New("read without a context") }
func (p ctxProvider) ReadContext(ctx context.Context) (map[string]any, error) {
	*p.got = ctx
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"a": "remote"}, nil
}
func (p ctxProvider) ReadBytesContext(context.Context) ([]byte, error) {
	return nil, errors.New("not used")
}

// A ContextProvider is read with the load's context, so a cancelled load
// stops its read.
func TestLoad_ContextProviderGetsTheLoadContext(t *testing.T) {
	type key struct{}
	var got context.Context
	ctx := context.WithValue(t.Context(), key{}, "load")
	cfg, err := Load[layered](ctx, WithBaseProvider(ctxProvider{&got}, nil), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, "remote", cfg.A)
	require.NotNil(t, got)
	assert.Equal(t, "load", got.Value(key{}))

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = Load[layered](cancelled, WithOverrideProvider(ctxProvider{&got}, nil), WithSkipValidation())
	require.ErrorIs(t, err, ErrSource)
	assert.ErrorIs(t, err, context.Canceled)
}

// A caller's decode hook runs before the built-ins, so it can take over a
// conversion: here a ";"-separated list the comma split would leave whole.
func TestLoad_DecodeHookRunsFirst(t *testing.T) {
	type cfg struct {
		Hosts []string `koanf:"hosts"`
	}
	semicolons := func(from, to reflect.Type, data any) (any, error) {
		if from.Kind() == reflect.String && to == reflect.TypeFor[[]string]() {
			return strings.Split(data.(string), ";"), nil
		}
		return data, nil
	}
	got, err := Load[cfg](t.Context(),
		WithDefaults(map[string]any{"hosts": "a;b,c"}),
		WithDecodeHook(mapstructure.DecodeHookFuncType(semicolons)),
		WithSkipValidation(),
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b,c"}, got.Hosts)
}

// WithFileParser reads every file in another format; a file it rejects fails
// with ErrFileParse.
func TestLoad_FileParser(t *testing.T) {
	type cfg struct {
		Host string `koanf:"host"`
	}
	dir := t.TempDir()
	good, bad := filepath.Join(dir, "app.env"), filepath.Join(dir, "bad.env")
	writeFile(t, good, "host=db.internal\n")
	writeFile(t, bad, "no separator\n")

	got, err := Load[cfg](t.Context(), WithFile(good), WithFileParser(kvParser{}), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, "db.internal", got.Host)

	_, err = Load[cfg](t.Context(), WithFile(bad), WithFileParser(kvParser{}), WithSkipValidation())
	require.ErrorIs(t, err, ErrFileParse)
	assert.Contains(t, err.Error(), bad)
}

type strictCfg struct {
	Server struct {
		Port int `koanf:"port"`
	} `koanf:"server"`
	Labels  map[string]string `koanf:"labels"`
	Any     any               `koanf:"any"`
	Plugins struct {
		Name  string         `koanf:"name"`
		Extra map[string]any `koanf:",remain"`
	} `koanf:"plugins"`
}

// WithStrictKeys refuses a key no field decodes, naming it and its source;
// keys under a map, an interface or a ,remain struct pass, and matching is
// case-insensitive like decoding.
func TestLoad_StrictKeys(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	writeFile(t, file, "server:\n  prot: 80\n  Port: 81\nlabels:\n  any_key: v\nany:\n  deep:\n    x: 1\nplugins:\n  name: p\n  other: 1\n")

	_, err := Load[strictCfg](t.Context(), WithFile(file), WithStrictKeys(), WithSkipValidation())
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), file)
	assert.Contains(t, err.Error(), "server.prot")
	for _, accepted := range []string{"server.Port", "labels.any_key", "any.deep", "plugins.other"} {
		assert.NotContains(t, err.Error(), accepted)
	}

	writeFile(t, file, "server:\n  Port: 81\n")
	got, err := Load[strictCfg](t.Context(), WithFile(file), WithStrictKeys(), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, 81, got.Server.Port)

	_, err = Load[strictCfg](t.Context(), WithDefaults(map[string]any{"servr.port": 1}), WithStrictKeys(), WithSkipValidation())
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "WithDefaults")

	// An environment variable that fits no key is never loaded.
	_, err = Load[strictCfg](t.Context(), WithEnvPrefix("STRICT"), WithStrictKeys(), WithSkipValidation(),
		WithEnviron(func() []string { return []string{"STRICT_SERVR_PORT=1"} }))
	require.NoError(t, err)

	// Without the option, unknown keys are ignored.
	writeFile(t, file, "server:\n  prot: 80\n")
	_, err = Load[strictCfg](t.Context(), WithFile(file), WithSkipValidation())
	require.NoError(t, err)
}

// A post-load hook written for another type fails the load before any
// source is read.
func TestLoad_PostLoadHookForAnotherType(t *testing.T) {
	type other struct{}
	_, err := Load[layered](t.Context(),
		WithRequiredFile(filepath.Join(t.TempDir(), "missing.yaml")),
		WithPostLoad(func(context.Context, *other) error { return nil }),
	)
	require.ErrorIs(t, err, ErrPostLoad)
	assert.NotErrorIs(t, err, ErrFileRead)
}

func TestLoad_RequiredFiles(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")
	writeFile(t, a, "a: one\n")
	_, err := Load[layered](t.Context(), WithRequiredFiles(a, b), WithSkipValidation())
	require.ErrorIs(t, err, ErrFileRead)
	assert.ErrorIs(t, err, os.ErrNotExist)

	writeFile(t, b, "a: two\n")
	got, err := Load[layered](t.Context(), WithRequiredFiles(a, b), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, "two", got.A, "a later file wins")
}
