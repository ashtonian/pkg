package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

// Each file takes the parser of the longest suffix its name ends in, so
// files of different formats load together, later files winning.
func TestLoad_FileParserBySuffix(t *testing.T) {
	type cfg struct {
		Host  string `koanf:"host"`
		Port  int    `koanf:"port"`
		Debug string `koanf:"debug"`
		Name  string `koanf:"name"`
	}
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	jsonFile := filepath.Join(dir, "overrides.JSON")
	envFile := filepath.Join(dir, "local.env")
	writeFile(t, yamlFile, "host: yaml\nport: 1\nname: yaml\n")
	writeFile(t, jsonFile, `{"port": 2, "debug": "json", "name": "json"}`)
	writeFile(t, envFile, "name=env\n")

	got, err := Load[cfg](t.Context(),
		WithFiles(yamlFile, jsonFile, envFile),
		WithFileParser("env", kvParser{}),
		WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, cfg{Host: "yaml", Port: 2, Debug: "json", Name: "env"}, *got)
}

// A longer suffix wins over a shorter one; "" sets the parser for every file
// no other suffix matches; a nil parser hands a suffix's files to a shorter
// one.
func TestLoad_FileParserSuffixes(t *testing.T) {
	type cfg struct {
		Host string `koanf:"host"`
	}
	dir := t.TempDir()
	kvJSON := filepath.Join(dir, "app.kv.json")
	conf := filepath.Join(dir, "app.conf")
	yamlInJSON := filepath.Join(dir, "legacy.json")
	writeFile(t, kvJSON, "host=kv\n")
	writeFile(t, conf, "host=conf\n")
	writeFile(t, yamlInJSON, "host: yaml\n")

	load := func(path string, opts ...Option) (*cfg, error) {
		return Load[cfg](t.Context(), append(opts, WithRequiredFile(path), WithSkipValidation())...)
	}

	got, err := load(kvJSON, WithFileParser(".kv.json", kvParser{}))
	require.NoError(t, err)
	assert.Equal(t, "kv", got.Host)

	got, err = load(conf, WithFileParser("", kvParser{}))
	require.NoError(t, err)
	assert.Equal(t, "conf", got.Host)
	_, err = load(conf)
	require.ErrorIs(t, err, ErrFileParse, "an unregistered suffix is YAML")

	_, err = load(yamlInJSON)
	require.ErrorIs(t, err, ErrFileParse, ".json is JSON")
	got, err = load(yamlInJSON, WithFileParser(".json", nil))
	require.NoError(t, err)
	assert.Equal(t, "yaml", got.Host)
}

// A file its parser rejects fails with ErrFileParse, naming the file and,
// for JSON, the line.
func TestLoad_FileParserRejects(t *testing.T) {
	type cfg struct {
		Host     string `koanf:"host"`
		Password string `koanf:"password"`
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.env")
	writeFile(t, bad, "no separator\n")
	_, err := Load[cfg](t.Context(), WithFile(bad), WithFileParser(".env", kvParser{}), WithSkipValidation())
	require.ErrorIs(t, err, ErrFileParse)
	assert.Contains(t, err.Error(), bad)

	broken := filepath.Join(dir, "broken.json")
	writeFile(t, broken, "{\n  \"host\": \"db\",\n  \"password\": \"hunter2\" oops\n}\n")
	_, err = Load[cfg](t.Context(), WithFile(broken), WithSkipValidation())
	require.ErrorIs(t, err, ErrFileParse)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, 3, pe.Line)
	assert.NotContains(t, err.Error(), "hunter2")
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

// A load refuses a key no field decodes, naming it;
// keys under a map, an interface or a ,remain struct pass, and matching is
// case-insensitive like decoding.
func TestLoad_StrictKeys(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	writeFile(t, file, "server:\n  prot: 80\n  Port: 81\nlabels:\n  any_key: v\nany:\n  deep:\n    x: 1\nplugins:\n  name: p\n  other: 1\n")

	_, err := Load[strictCfg](t.Context(), WithFile(file), WithSkipValidation())
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "server.prot")
	for _, accepted := range []string{"server.Port", "labels.any_key", "any.deep", "plugins.other"} {
		assert.NotContains(t, err.Error(), accepted)
	}

	writeFile(t, file, "server:\n  Port: 81\n")
	got, err := Load[strictCfg](t.Context(), WithFile(file), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, 81, got.Server.Port)

	_, err = Load[strictCfg](t.Context(), WithDefaults(map[string]any{"servr.port": 1}), WithSkipValidation())
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "servr")

	// An environment variable that fits no key is never loaded.
	_, err = Load[strictCfg](t.Context(), WithEnvPrefix("STRICT"), WithSkipValidation(),
		WithEnviron(func() []string { return []string{"STRICT_SERVR_PORT=1"} }))
	require.NoError(t, err)

	// WithAllowUnknownKeys lets them through.
	writeFile(t, file, "server:\n  prot: 80\n")
	_, err = Load[strictCfg](t.Context(), WithFile(file), WithSkipValidation(), WithAllowUnknownKeys())
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

// Strict keys follow decoding exactly: keys inside slice elements and map
// entries of structs are checked against the element; a map's own keys, a
// weakly typed map-for-slice and input a decode hook consumes are not keys.
func TestLoad_StrictKeysFollowDecoding(t *testing.T) {
	type item struct {
		Port int `koanf:"port"`
	}
	type cfg struct {
		Items   []item            `koanf:"items"`
		Named   map[string]item   `koanf:"named"`
		Labels  map[string]string `koanf:"labels"`
		Address string            `koanf:"address"`
	}
	strict := func(t *testing.T, source string, opts ...Option) (*cfg, error) {
		t.Helper()
		return Load[cfg](t.Context(), append([]Option{
			WithBaseProvider(rawBytes(source), yaml.Parser()), WithSkipValidation(),
		}, opts...)...)
	}

	for source, key := range map[string]string{
		"items:\n  - prot: 80\n":             "items[0].prot",
		"named:\n  primary:\n    prot: 80\n": `named["primary"].prot`,
	} {
		_, err := strict(t, source)
		require.ErrorIs(t, err, ErrValidation, source)
		assert.Contains(t, err.Error(), key)
	}

	got, err := strict(t, "labels:\n  example.com: ok\n")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"example.com": "ok"}, got.Labels, "a dotted map key is a key, not a path")

	got, err = strict(t, "items:\n  port: 80\n")
	require.NoError(t, err)
	assert.Equal(t, []item{{Port: 80}}, got.Items, "weak typing decodes a map as a one-element slice")

	asAddress := mapstructure.DecodeHookFuncType(func(from, to reflect.Type, data any) (any, error) {
		if from.Kind() == reflect.Map && to.Kind() == reflect.String {
			return "decoded", nil
		}
		return data, nil
	})
	got, err = strict(t, "address:\n  host: example\n", WithDecodeHook(asAddress))
	require.NoError(t, err)
	assert.Equal(t, "decoded", got.Address)
}

// A key spelled in another letter case is rewritten to the field's own
// spelling before sources merge, so precedence holds whatever case a source
// used; one source spelling one field two ways is refused.
func TestLoad_KeySpellingFollowsTheField(t *testing.T) {
	type cfg struct {
		Port int `koanf:"port" default:"8080"`
	}
	got, err := Load[cfg](t.Context(), WithBaseProvider(rawBytes("Port: 9000\n"), yaml.Parser()))
	require.NoError(t, err)
	assert.Equal(t, 9000, got.Port, "a higher source wins over the default whatever its letter case")

	_, err = Load[cfg](t.Context(), WithBaseProvider(rawBytes("port: 1\nPort: 2\n"), yaml.Parser()))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), `port (as "Port" and "port")`)
}

// A parser's own message may quote the source, so a parse failure's message
// names the source and line only; the parser's error stays reachable.
func TestLoad_ParseErrorQuotesNothing(t *testing.T) {
	type cfg struct {
		Token string `koanf:"token" sensitive:"true"`
	}
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "host: a\ntoken: !!int "+leaked+"\n")
	_, err := Load[cfg](t.Context(), WithFile(file))
	require.ErrorIs(t, err, ErrFileParse)
	assert.NotContains(t, err.Error(), leaked)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, file, pe.Source)
	assert.Contains(t, pe.Err.Error(), leaked, "the parser's error is kept for a trusted diagnostic")

	writeFile(t, file, "host: a\ntoken: [unclosed\n")
	_, err = Load[cfg](t.Context(), WithFile(file))
	require.ErrorAs(t, err, &pe)
	assert.Positive(t, pe.Line, "the line the parser named")
	assert.Contains(t, err.Error(), fmt.Sprintf("at line %d", pe.Line))

	_, err = Load[cfg](t.Context(), WithOverrideProvider(rawBytes("token: !!int "+leaked+"\n"), yaml.Parser()))
	require.ErrorIs(t, err, ErrSource)
	assert.NotContains(t, err.Error(), leaked)
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "override provider 0", pe.Source)

	// A value that reads like a location is not taken for one.
	_, err = Load[cfg](t.Context(), WithBaseProvider(rawBytes("token: !!int line 8421\n"), yaml.Parser()))
	require.ErrorAs(t, err, &pe)
	assert.Zero(t, pe.Line)
	assert.NotContains(t, err.Error(), "8421")
}

// An element struct's default tags apply to every map entry and slice
// element a source creates, below what the sources set.
func TestLoad_ElementDefaults(t *testing.T) {
	type limits struct {
		Name  string `koanf:"name"`
		Limit int    `koanf:"limit" default:"50"`
		TLS   struct {
			Enabled bool `koanf:"enabled" default:"true"`
		} `koanf:"tls"`
	}
	type cfg struct {
		Features map[string]limits  `koanf:"features"`
		Pools    []limits           `koanf:"pools"`
		Optional map[string]*limits `koanf:"optional"`
	}
	got, err := Load[cfg](t.Context(),
		WithBaseProvider(rawBytes("features:\n  audit:\n    limit: 7\n  empty: {}\npools:\n  - name: a\noptional:\n  off:\n"), yaml.Parser()),
		WithEnvPrefix("APP"),
		WithEnviron(func() []string { return []string{"APP_FEATURES_DEVICES_NAME=d"} }),
		WithSkipValidation(),
	)
	require.NoError(t, err)
	assert.Equal(t, 7, got.Features["audit"].Limit, "a value a source set wins")
	assert.True(t, got.Features["audit"].TLS.Enabled, "nested element defaults apply too")
	assert.Equal(t, 50, got.Features["devices"].Limit, "an entry the environment created")
	assert.Equal(t, 50, got.Features["empty"].Limit, "an entry written as {}")
	require.Len(t, got.Pools, 1)
	assert.Equal(t, 50, got.Pools[0].Limit)
	assert.Contains(t, got.Optional, "off")
	assert.Nil(t, got.Optional["off"], "an explicit null stays null")
}

// csv decodes itself, so a string bound for it is not split.
type csv []string

func (c *csv) UnmarshalText(b []byte) error {
	*c = csv{"whole:" + string(b)}
	return nil
}

// A comma-separated string fills a slice of any element type, from a
// default tag or the environment alike; []byte and a slice that decodes
// itself take it whole.
func TestLoad_CommaSeparatedSlices(t *testing.T) {
	type cfg struct {
		Hosts   []string        `koanf:"hosts" default:"a,b"`
		Ports   []int           `koanf:"ports" default:"80,443"`
		Waits   []time.Duration `koanf:"waits" default:"1s,2m"`
		Ratios  []float64       `koanf:"ratios"`
		Empty   []int           `koanf:"empty" default:""`
		Raw     []byte          `koanf:"raw" default:"a,b"`
		Decoded csv             `koanf:"decoded" default:"a,b"`
	}
	got, err := Load[cfg](t.Context(),
		WithEnvPrefix("APP"),
		WithEnviron(func() []string { return []string{"APP_RATIOS=0.5,1.5", "APP_PORTS=8080,8443"} }),
		WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, cfg{
		Hosts:   []string{"a", "b"},
		Ports:   []int{8080, 8443},
		Waits:   []time.Duration{time.Second, 2 * time.Minute},
		Ratios:  []float64{0.5, 1.5},
		Empty:   []int{},
		Raw:     []byte("a,b"),
		Decoded: csv{"whole:a,b"},
	}, *got)
}
