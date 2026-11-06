package config

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type keyConfig struct {
	Port   int `koanf:"port" alias:"old_port" default:"8" validate:"min=1,max=100"`
	Floor  int `koanf:"floor" default:"1"`
	Nested struct {
		Count int `koanf:"count" default:"2" validate:"min=1"`
	} `koanf:"nested"`
	Items []struct {
		Port int `koanf:"port" default:"3" validate:"min=1"`
	} `koanf:"items" format:"json" validate:"dive"`
	Names map[string]int `koanf:"names" validate:"dive,min=1"`
}

func (c *keyConfig) Validate() error {
	if c.Port < c.Floor {
		return errors.New("port is below floor")
	}
	return nil
}

func TestCheckKeyMatchesSchemaAndDoesNoSourceIO(t *testing.T) {
	provider, err := NewMapProvider(func(context.Context) (map[string]any, error) {
		t.Error("provider read during key check")
		return nil, nil
	}, "")
	require.NoError(t, err)
	opts := []Option{WithBaseProvider(provider, nil), WithRequiredFile("/missing/config.yaml"),
		WithEnvPrefix("APP"), WithEnviron(func() []string { t.Error("environment read"); return nil }),
		WithSecretPopulator(func(context.Context, any) error { t.Error("secrets read"); return nil }),
		WithPostLoad(func(context.Context, *keyConfig) error { t.Error("post-load ran"); return nil }),
		WithUnknownKeys(func(context.Context, *UnknownKeysError) error { t.Error("unknown callback ran"); return nil })}
	for _, tc := range []struct {
		key      string
		value    any
		category error
	}{
		{"port", "9", nil}, {"PORT", 10, nil}, {"old_port", 11, nil},
		{"port", "invalid-secret-value", ErrValidation}, {"port", 101, ErrValidation},
		{"nested.count", 0, ErrValidation}, {"floor", 10, ErrValidation},
		{"prot", 4, ErrKey}, {"items[*].port", 3, ErrKey},
		{"items[0].port", 3, nil}, {"items[999999999].port", 3, ErrValidation},
		{`names["a.b"]`, 2, nil}, {`names["a.b"]`, 0, ErrValidation},
	} {
		t.Run(fmt.Sprintf("%s/%v", tc.key, tc.value), func(t *testing.T) {
			err := CheckKey[keyConfig](t.Context(), tc.key, tc.value, opts...)
			if tc.category == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.category)
			var ke *KeyError
			require.ErrorAs(t, err, &ke)
			assert.Equal(t, tc.category == ErrKey, ke.Unknown)
			assert.NotContains(t, err.Error(), "invalid-secret-value")
		})
	}
	require.NoError(t, CheckKey[keyConfig](t.Context(), "floor", 10, WithDefaults(map[string]any{"port": 20})))
	require.NoError(t, CheckKey[keyConfig](t.Context(), "port", "nine", WithDecodeHook(mapstructure.DecodeHookFuncType(func(from, to reflect.Type, v any) (any, error) {
		if from.Kind() == reflect.String && to.Kind() == reflect.Int && v == "nine" {
			return 9, nil
		}
		return v, nil
	}))))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, CheckKey[keyConfig](ctx, "port", 9, opts...), context.Canceled)
}

func TestWithInvalidEntriesPreservesLowerSourcesAndUnknowns(t *testing.T) {
	input := map[string]any{"port": "bad", "floor": "also-bad", "nested": map[string]any{"count": 4, "future": true},
		"items": []any{map[string]any{"port": "bad", "future": true}}, "future": true}
	var rejected []KeyError
	snap, err := LoadSnapshot[keyConfig](t.Context(),
		WithBaseProvider(confmap.Provider(map[string]any{"port": 7, "items": []any{map[string]any{"port": 5}}}, ""), nil),
		WithOverrideProvider(confmap.Provider(input, ""), nil, WithInvalidEntries(func(_ context.Context, entries []KeyError) error { rejected = entries; return nil })),
		WithAllowUnknownKeys())
	require.NoError(t, err)
	assert.Equal(t, 7, snap.Config.Port)
	assert.Equal(t, 4, snap.Config.Nested.Count)
	require.Len(t, snap.Config.Items, 1)
	assert.Equal(t, 5, snap.Config.Items[0].Port)
	require.Len(t, rejected, 3)
	assert.Equal(t, []string{"floor", "items", "port"}, []string{rejected[0].Key, rejected[1].Key, rejected[2].Key})
	assert.Equal(t, SourceBase, snap.Provenance["port"].Input.Kind)
	assert.Equal(t, SourceOverride, snap.Provenance["nested.count"].Input.Kind)
	require.NotNil(t, snap.UnknownKeys)
	assert.Equal(t, []UnknownKey{{Key: "future"}, {Key: "items[0].future"}, {Key: "nested.future"}}, snap.UnknownKeys.Keys)
	assert.Equal(t, "bad", input["port"], "provider data remains owned by the provider")
	for _, r := range rejected {
		assert.NotContains(t, r.Error(), "bad")
	}
}

func TestWithInvalidEntriesJSONAndStrictUnknowns(t *testing.T) {
	for _, value := range []any{`not json`, `[{"port":0,"future":true}]`} {
		var rejected []KeyError
		_, err := Load[keyConfig](t.Context(), WithBaseProvider(confmap.Provider(map[string]any{"items": value, "future": 1}, ""), nil,
			WithInvalidEntries(func(_ context.Context, entries []KeyError) error { rejected = entries; return nil })))
		var unknown *UnknownKeysError
		require.ErrorAs(t, err, &unknown)
		require.Len(t, rejected, 1)
		assert.Equal(t, "items", rejected[0].Key)
		assert.Contains(t, unknown.Keys, UnknownKey{Key: "future"})
	}
}

func TestWithInvalidEntriesFailureBoundaries(t *testing.T) {
	called := false
	p, err := NewMapProvider(func(context.Context) (map[string]any, error) { called = true; return map[string]any{"port": 9}, nil }, "")
	require.NoError(t, err)
	_, err = Load[keyConfig](t.Context(), WithDefaults(map[string]any{"port": 0}), WithBaseProvider(p, nil, WithInvalidEntries(nil)))
	require.ErrorIs(t, err, ErrValidation)
	assert.False(t, called, "invalid defaults fail before a source can be blamed")
	failed := errors.New("read failed")
	_, err = Load[keyConfig](t.Context(), WithBaseProvider(failingProvider{err: failed}, nil, WithInvalidEntries(nil)))
	require.ErrorIs(t, err, ErrSource)
	require.ErrorIs(t, err, failed)
	_, err = Load[keyConfig](t.Context(), WithBaseProvider(p, nil, WithInvalidEntries(func(context.Context, []KeyError) error { return failed })))
	require.ErrorIs(t, err, ErrSource)
	require.ErrorIs(t, err, failed)
	// Both entries pass individually against defaults; their combination fails.
	_, err = Load[keyConfig](t.Context(), WithBaseProvider(confmap.Provider(map[string]any{"port": 2, "floor": 3}, ""), nil, WithInvalidEntries(nil)))
	require.ErrorIs(t, err, ErrValidation)
}

func TestDecodeKeyReturnsDetachedValidatedConfiguration(t *testing.T) {
	input := map[string]int{"service": 4}
	got, err := DecodeKey[keyConfig](t.Context(), "names", input)
	require.NoError(t, err)
	assert.Equal(t, 8, got.Port)
	assert.Equal(t, 4, got.Names["service"])
	input["service"] = 99
	assert.Equal(t, 4, got.Names["service"])
	got.Names["service"] = 6
	assert.Equal(t, 99, input["service"])
	got, err = DecodeKey[keyConfig](t.Context(), "port", 0)
	require.ErrorIs(t, err, ErrValidation)
	assert.Nil(t, got)
}
