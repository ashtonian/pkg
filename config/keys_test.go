package config

import (
	"errors"
	"fmt"
	"reflect"
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
	assert.Contains(t, err.Error(), `decodes: named["x.y"].prot, server."a.b", server.prot`)
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
