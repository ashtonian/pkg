package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mapSource is a provider over a fixed tree, keyed however the test needs.
type mapSource map[string]any

func (m mapSource) Read() (map[string]any, error) { return m, nil }
func (mapSource) ReadBytes() ([]byte, error)      { return nil, errors.New("mapSource: use Read") }

// customDecoded decodes itself from {raw: n}, a key no field of it has.
type customDecoded struct {
	Value int `koanf:"value"`
}

func (v *customDecoded) UnmarshalMapstructure(input any) error {
	m, ok := input.(map[string]any)
	if !ok {
		return fmt.Errorf("want a map, got %T", input)
	}
	n, ok := m["raw"].(int)
	if !ok {
		return errors.New("want raw")
	}
	v.Value = n
	return nil
}

// Strict keys judge what the decoder used, so input a custom decoder or a
// decode hook consumes is not reported as unknown.
func TestStrictKeys_InputACustomDecoderConsumes(t *testing.T) {
	type cfg struct {
		Item customDecoded `koanf:"item"`
	}
	got, err := Load[cfg](t.Context(), WithBaseProvider(mapSource{"item": map[string]any{"raw": 42}}, nil), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, 42, got.Item.Value)
}

func TestStrictKeys_InputAHookConsumes(t *testing.T) {
	type item struct{ Value int }
	type cfg struct {
		Item *item `koanf:"item"`
	}
	hook := mapstructure.DecodeHookFuncType(func(_, to reflect.Type, v any) (any, error) {
		if to == reflect.TypeFor[*item]() {
			return &item{Value: 42}, nil
		}
		return v, nil
	})
	got, err := Load[cfg](t.Context(), WithBaseProvider(mapSource{"item": map[string]any{"external": 42}}, nil),
		WithDecodeHook(hook), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, 42, got.Item.Value)
}

func TestStrictKeys_InputAHookReshapes(t *testing.T) {
	type item struct {
		Port int `koanf:"port"`
	}
	type cfg struct {
		Item item `koanf:"item"`
	}
	hook := mapstructure.DecodeHookFuncType(func(_, to reflect.Type, v any) (any, error) {
		if m, ok := v.(map[string]any); ok && to == reflect.TypeFor[item]() {
			if x, ok := m["external"]; ok {
				return map[string]any{"port": x}, nil
			}
		}
		return v, nil
	})
	got, err := Load[cfg](t.Context(), WithBaseProvider(mapSource{"item": map[string]any{"external": 42}}, nil),
		WithDecodeHook(hook), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, 42, got.Item.Port)
}

type squashedPort struct {
	Port int `koanf:"port"`
}

func TestStrictKeys_SquashedEmbeddedStruct(t *testing.T) {
	type cfg struct {
		squashedPort `koanf:",squash"`
	}
	got, err := Load[cfg](t.Context(), WithBaseProvider(mapSource{"port": 80}, nil), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, 80, got.Port)
}

// A typo inside a slice of structs fails strict keys whatever collection
// type the source holds it in.
func TestStrictKeys_TypoInsideSliceElements(t *testing.T) {
	type item struct {
		Port int `koanf:"port"`
	}
	type cfg struct {
		Items []item `koanf:"items"`
	}
	for _, items := range []any{
		[]map[string]any{{"prot": 80}},
		[1]map[string]any{{"prot": 80}},
		[]any{map[string]int{"prot": 80}},
	} {
		t.Run(fmt.Sprintf("%T", items), func(t *testing.T) {
			_, err := Load[cfg](t.Context(), WithBaseProvider(mapSource{"items": items}, nil), WithSkipValidation())
			require.ErrorIs(t, err, ErrValidation)
		})
	}
}

// Shapes strict keys accept: maps of slices, slices of maps, a remain map,
// an interface and a squashed pointer, keyed in any letter case.
func TestStrictKeys_AcceptsEveryDecodableShape(t *testing.T) {
	type item struct {
		Port int `koanf:"port"`
	}
	type cfg struct {
		Named map[string][]item  `koanf:"named"`
		Rows  []map[string]*item `koanf:"rows"`
		Extra map[string]any     `koanf:",remain"`
		Any   any                `koanf:"any"`
		Embed *item              `koanf:",squash"`
	}
	src := mapSource{
		"Named":   map[any]any{"x": []any{map[any]any{"Port": 80}}},
		"rows":    []any{map[any]any{"a": map[any]any{"Port": 81}}},
		"unknown": true,
		"any":     map[string]any{"arbitrary": 1},
		"PORT":    82,
	}
	got, err := Load[cfg](t.Context(), WithBaseProvider(src, nil), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, 80, got.Named["x"][0].Port)
	assert.Equal(t, 81, got.Rows[0]["a"].Port)
	assert.Equal(t, 82, got.Embed.Port)
	assert.Equal(t, true, got.Extra["unknown"])
}

// Sources merge after their keys take the fields' spelling, while map keys
// stay as written: "x" and "X" are two entries.
func TestLoad_KeyCaseMergesAcrossSources(t *testing.T) {
	type item struct {
		Port int    `koanf:"port"`
		Name string `koanf:"name"`
	}
	type cfg struct {
		Named map[string]item `koanf:"named"`
	}
	got, err := Load[cfg](t.Context(),
		WithBaseProvider(mapSource{"NAMED": map[string]any{
			"x": map[any]any{"Port": 80, "Name": "base"},
			"X": map[string]any{"name": "case-key"},
		}}, nil),
		WithOverrideProvider(mapSource{"named": map[string]any{"x": map[string]any{"PORT": 81}}}, nil), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, 81, got.Named["x"].Port)
	assert.Equal(t, "base", got.Named["x"].Name)
	assert.Equal(t, "case-key", got.Named["X"].Name)
}

// Decode hooks run once, on the merged tree: a conversion is not applied
// twice, and a hook cannot change what a higher source overrides.
func TestLoad_DecodeHooksRunOnceAfterMerging(t *testing.T) {
	t.Run("one conversion per value", func(t *testing.T) {
		type cfg struct {
			Port int `koanf:"port"`
		}
		calls := 0
		hook := mapstructure.DecodeHookFuncType(func(_, to reflect.Type, v any) (any, error) {
			if to.Kind() == reflect.Int {
				calls++
				if calls > 1 {
					return nil, errors.New("converted twice")
				}
			}
			return v, nil
		})
		_, err := Load[cfg](t.Context(), WithBaseProvider(mapSource{"port": 80}, nil), WithDecodeHook(hook), WithSkipValidation())
		require.NoError(t, err)
	})
	t.Run("override wins over a hook default", func(t *testing.T) {
		type block struct {
			Port int    `koanf:"port"`
			Name string `koanf:"name"`
		}
		type cfg struct {
			Block block `koanf:"block"`
		}
		hook := mapstructure.DecodeHookFuncType(func(_, to reflect.Type, v any) (any, error) {
			if m, ok := v.(map[string]any); ok && to == reflect.TypeFor[block]() {
				if _, ok := m["port"]; !ok {
					m["port"] = 99
				}
			}
			return v, nil
		})
		got, err := Load[cfg](t.Context(),
			WithBaseProvider(mapSource{"block": map[string]any{"port": 80}}, nil),
			WithOverrideProvider(mapSource{"block": map[string]any{"name": "override"}}, nil),
			WithDecodeHook(hook), WithSkipValidation())
		require.NoError(t, err)
		assert.Equal(t, 80, got.Block.Port)
		assert.Equal(t, "override", got.Block.Name)
	})
}

type elementEntry struct {
	Port    int  `koanf:"port" default:"50"`
	Enabled bool `koanf:"enabled" default:"true"`
	Child   *struct {
		Port int `koanf:"port" default:"51"`
	} `koanf:"child"`
}

// An explicit null stays null — an entry, or a field on the way to a
// default — and an explicit zero or false is not replaced by a default.
func TestElementDefaults_ExplicitNullAndZeroStay(t *testing.T) {
	type cfg struct {
		Items []*elementEntry          `koanf:"items"`
		Named map[string]*elementEntry `koanf:"named"`
	}
	got, err := Load[cfg](t.Context(),
		WithBaseProvider(rawBytes("items: [null, {port: 0, enabled: false, child: null}]\nnamed: {off: null}\n"), yaml.Parser()),
		WithSkipValidation())
	require.NoError(t, err)
	require.Len(t, got.Items, 2)
	assert.Nil(t, got.Items[0])
	assert.Nil(t, got.Named["off"])
	assert.Nil(t, got.Items[1].Child)
	assert.Zero(t, got.Items[1].Port)
	assert.False(t, got.Items[1].Enabled)
}

// The weakly decoded shapes — a map for a one-element slice, a list of maps
// for one map — take element defaults too.
func TestElementDefaults_WeaklyDecodedShapes(t *testing.T) {
	type cfg struct {
		Items []elementEntry          `koanf:"items"`
		Named map[string]elementEntry `koanf:"named"`
	}
	tests := []struct {
		source string
		entry  func(*cfg) elementEntry
	}{
		{"items: {enabled: false}\n", func(c *cfg) elementEntry { return c.Items[0] }},
		{"named: [{first: {enabled: false}}]\n", func(c *cfg) elementEntry { return c.Named["first"] }},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			got, err := Load[cfg](t.Context(), WithBaseProvider(rawBytes(tt.source), yaml.Parser()), WithSkipValidation())
			require.NoError(t, err)
			e := tt.entry(got)
			assert.Equal(t, 50, e.Port)
			assert.False(t, e.Enabled)
			require.NotNil(t, e.Child)
			assert.Equal(t, 51, e.Child.Port)
		})
	}
}

// A struct whose keys cannot be told apart fails the load before any source
// is read.
func TestLoad_RejectsAnUnaddressableSchema(t *testing.T) {
	type cfg struct {
		A int `koanf:"a[0]"`
	}
	_, err := Load[cfg](t.Context())
	require.ErrorIs(t, err, ErrSchema)
}

// Unknown keys are named in the key grammar: a source key under a struct
// that cannot read as a field is quoted.
func TestStrictKeys_NamesUnknownKeys(t *testing.T) {
	type entry struct {
		Port int `koanf:"port"`
	}
	type cfg struct {
		Server entry            `koanf:"server"`
		Named  map[string]entry `koanf:"named"`
	}
	src := mapSource{
		"server": map[string]any{"a.b": 1, "prot": 2},
		"named":  map[string]any{"x.y": map[string]any{"prot": 3}},
	}
	_, err := Load[cfg](t.Context(), WithBaseProvider(src, nil), WithSkipValidation())
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), `decodes: named["x.y"].prot (did you mean named["x.y"].port?), server."a.b", server.prot (did you mean server.port?)`)
}

// An unknown key comes with the field of its struct it most likely
// misspells, in an *UnknownKeysError; a key no field is close to has none.
func TestStrictKeys_SuggestsTheClosestField(t *testing.T) {
	type entry struct {
		TimeoutPerRepo int    `koanf:"timeout_per_repo"`
		SSHKey         string `koanf:"ssh_key"`
		DB             string `koanf:"db"`
	}
	type cfg struct {
		Sync  entry            `koanf:"sync"`
		Items []entry          `koanf:"items"`
		Named map[string]entry `koanf:"named"`
		Port  int              `koanf:"port"`
	}
	src := mapSource{
		"sync":  map[string]any{"timout_per_repo": 1, "remote_name": "x", "x": 1, "dv": 1},
		"items": []any{map[string]any{"ssh-key": "k"}},
		"named": map[string]any{"a": map[string]any{"SSH-Key": "k"}},
		"prot":  1,
	}
	_, err := Load[cfg](t.Context(), WithBaseProvider(src, nil), WithSkipValidation())
	require.ErrorIs(t, err, ErrValidation)
	var unknown *UnknownKeysError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, reflect.TypeFor[cfg](), unknown.Type)
	assert.Equal(t, []UnknownKey{
		{Key: "items[0].ssh-key", Suggestion: "items[0].ssh_key"},
		{Key: `named["a"].SSH-Key`, Suggestion: `named["a"].ssh_key`},
		{Key: "prot", Suggestion: "port"},
		{Key: "sync.dv", Suggestion: "sync.db"},
		{Key: "sync.remote_name"},
		{Key: "sync.timout_per_repo", Suggestion: "sync.timeout_per_repo"},
		{Key: "sync.x"},
	}, unknown.Keys)
}

func TestEditDistance(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"port", "port", 0},
		{"prot", "port", 2},
		{"ssh-key", "ssh_key", 1},
		{"timout", "timeout", 1},
		{"größe", "grösse", 2},
	} {
		assert.Equal(t, tt.want, editDistance(tt.a, tt.b), "%q → %q", tt.a, tt.b)
	}
}

// Validation errors name map entries by their quoted key, whatever the key
// holds.
func TestValidation_NamesMapEntriesByKey(t *testing.T) {
	type entry struct {
		Limit int `koanf:"limit" validate:"min=1"`
	}
	type cfg struct {
		Limits map[string]entry `koanf:"limits" validate:"dive"`
	}
	src := mapSource{"limits": map[string]any{"a.b": map[string]any{"limit": 0}, "ok": map[string]any{"limit": 5}}}
	_, err := Load[cfg](t.Context(), WithBaseProvider(src, nil))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), `limits["a.b"].limit(min=1)`)
}

// A WithUnknownKeys handler sees the unknown keys and the variables under
// the prefix that name no key, each with what it most likely misspells, and
// a nil return lets the load go on without them.
func TestUnknownKeys_HandlerSeesKeysAndVariables(t *testing.T) {
	type cfg struct {
		Server struct {
			Port int `koanf:"port"`
		} `koanf:"server"`
		Debug bool `koanf:"debug"`
	}
	var seen []*UnknownKeysError
	got, err := Load[cfg](t.Context(),
		WithBaseProvider(mapSource{"server": map[string]any{"port": 8080, "prot": 1}, "sidecar": 1}, nil),
		WithEnvPrefix("APP"),
		WithEnviron(func() []string { return []string{"APP_DEBUG=true", "APP_SERVER_PROT=1", "APP_UNRELATED_SETTING=1"} }),
		WithUnknownKeys(func(_ context.Context, unknown *UnknownKeysError) error {
			seen = append(seen, unknown)
			return nil
		}),
		WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, 8080, got.Server.Port)
	assert.True(t, got.Debug)
	require.Len(t, seen, 1)
	assert.Equal(t, []UnknownKey{{Key: "server.prot", Suggestion: "server.port"}, {Key: "sidecar"}}, seen[0].Keys)
	assert.Equal(t, []UnknownKey{
		{Key: "APP_SERVER_PROT", Suggestion: "APP_SERVER_PORT"},
		{Key: "APP_UNRELATED_SETTING"},
	}, seen[0].Variables)
	assert.Equal(t, "keys no field of config.cfg decodes: server.prot (did you mean server.port?), sidecar; "+
		"environment variables no field of config.cfg reads: APP_SERVER_PROT (did you mean APP_SERVER_PORT?), APP_UNRELATED_SETTING",
		seen[0].Error())
}

// The handler decides: here it ignores the legacy section and fails on the
// rest, with ErrValidation and the error it returned. A section no field
// holds is one key, not one per key beneath it.
func TestUnknownKeys_HandlerDecides(t *testing.T) {
	type cfg struct {
		Port int `koanf:"port"`
	}
	ignoreLegacy := WithUnknownKeys(func(_ context.Context, unknown *UnknownKeysError) error {
		rest := *unknown
		rest.Keys = slices.DeleteFunc(slices.Clone(unknown.Keys), func(k UnknownKey) bool {
			return k.Key == "legacy" || strings.HasPrefix(k.Key, "legacy.")
		})
		if len(rest.Keys) == 0 {
			return nil
		}
		return &rest
	})

	_, err := Load[cfg](t.Context(), WithBaseProvider(mapSource{"legacy": map[string]any{"old": 1}}, nil), ignoreLegacy)
	require.NoError(t, err)

	_, err = Load[cfg](t.Context(), WithBaseProvider(mapSource{"legacy": map[string]any{"old": 1}, "prot": 1}, nil), ignoreLegacy)
	require.ErrorIs(t, err, ErrValidation)
	var unknown *UnknownKeysError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, []UnknownKey{{Key: "prot", Suggestion: "port"}}, unknown.Keys)
}

// Without a handler, unknown keys fail and variables that name no key are
// ignored: they never fail a load and are not reported with its keys.
func TestUnknownKeys_DefaultIgnoresVariables(t *testing.T) {
	type cfg struct {
		Port int `koanf:"port"`
	}
	environ := WithEnviron(func() []string { return []string{"APP_PROT=1"} })
	_, err := Load[cfg](t.Context(), WithEnvPrefix("APP"), environ)
	require.NoError(t, err)

	_, err = Load[cfg](t.Context(), WithBaseProvider(mapSource{"prot": 1}, nil), WithEnvPrefix("APP"), environ)
	var unknown *UnknownKeysError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, []UnknownKey{{Key: "prot", Suggestion: "port"}}, unknown.Keys)
	assert.Empty(t, unknown.Variables)

	called := false
	_, err = Load[cfg](t.Context(), WithBaseProvider(mapSource{"port": 1}, nil),
		WithUnknownKeys(func(context.Context, *UnknownKeysError) error { called = true; return nil }))
	require.NoError(t, err)
	assert.False(t, called, "a load with nothing unknown does not call the handler")
}

// Under a Reloader the handler runs on every reload, so a key added to a
// live source is reported and the reload still publishes.
func TestUnknownKeys_HandlerRunsOnReload(t *testing.T) {
	type cfg struct {
		Port int `koanf:"port"`
	}
	var mu sync.Mutex
	settings := map[string]any{"port": 1}
	src, err := NewMapProvider(func(context.Context) (map[string]any, error) {
		mu.Lock()
		defer mu.Unlock()
		return maps.Clone(settings), nil
	}, "")
	require.NoError(t, err)

	var reported [][]UnknownKey
	r, err := NewReloader[cfg](t.Context(), []Option{
		WithBaseProvider(src, nil),
		WithUnknownKeys(func(_ context.Context, unknown *UnknownKeysError) error {
			reported = append(reported, unknown.Keys)
			return nil
		}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Stop(context.Background())) })

	mu.Lock()
	settings = map[string]any{"port": 2, "prot": 3}
	mu.Unlock()
	_, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 2, r.Config().Port)
	assert.Equal(t, [][]UnknownKey{{{Key: "prot", Suggestion: "port"}}}, reported)
}
