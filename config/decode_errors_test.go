//go:build unit

package config

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// echoText fails to decode, quoting its input the way time.Parse and many
// hand-written decoders do.
type echoText string

func (e *echoText) UnmarshalText(b []byte) error { return fmt.Errorf("bad value %q", b) }

const leaked = "FAKE_SECRET_VALUE"

// A value given to a sensitive field never reaches the load error, whichever
// rule makes the field sensitive; the error still names the field and is an
// ErrValidation.
func TestLoad_DecodeErrorWithholdsSensitiveValues(t *testing.T) {
	type creds struct {
		Pin echoText `koanf:"pin"`
	}
	type cfg struct {
		Tagged   time.Time           `koanf:"tagged" sensitive:"true"`
		Password echoText            `koanf:"password"`
		Vault    creds               `koanf:"vault" sensitive:"true"`
		Labels   map[string]echoText `koanf:"labels"`
	}
	for _, tt := range []struct {
		name, env, field string
	}{
		{"tag", "DECODE_TAGGED", "tagged"},
		{"field name", "DECODE_PASSWORD", "password"},
		{"beneath a tagged struct", "DECODE_VAULT_PIN", "vault.pin"},
		{"map key name", "DECODE_LABELS_TOKEN", "labels[token]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.env, leaked)
			_, err := Load[cfg](t.Context(), WithEnvPrefix("DECODE"), WithSkipValidation())
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrValidation)
			assert.NotContains(t, err.Error(), leaked)
			assert.Contains(t, err.Error(), "'"+tt.field+"'")
			assert.Contains(t, err.Error(), "sensitive and not shown")
		})
	}
}

// A failure on an ordinary field keeps its decoder's detail and stays
// reachable as a *mapstructure.DecodeError, even beside a withheld one.
func TestLoad_DecodeErrorKeepsOrdinaryDetail(t *testing.T) {
	type cfg struct {
		Plain  echoText `koanf:"plain"`
		Secret echoText `koanf:"secret"`
	}
	t.Setenv("DECODE_PLAIN", "visible-input")
	_, err := Load[cfg](t.Context(), WithEnvPrefix("DECODE"), WithSkipValidation())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "visible-input")
	var de *mapstructure.DecodeError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "plain", de.Name())

	t.Setenv("DECODE_SECRET", leaked)
	_, err = Load[cfg](t.Context(), WithEnvPrefix("DECODE"), WithSkipValidation())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "visible-input")
	assert.NotContains(t, err.Error(), leaked)
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "plain", de.Name())
}

// A reload that fails on a sensitive field reports the same withheld error,
// so neither the reload error callback nor the log sees the value.
func TestReload_DecodeErrorWithholdsSensitiveValues(t *testing.T) {
	type cfg struct {
		Token time.Time `koanf:"token" sensitive:"true"`
	}
	t.Setenv("DECODE_TOKEN", "2026-10-13T00:00:00Z")
	var reported []error
	r, err := NewReloader[cfg](
		t.Context(),
		[]Option{WithEnvPrefix("DECODE"), WithSkipValidation()},
		WithOnReloadError(func(_ string, err error) { reported = append(reported, err) }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(t.Context()) })

	t.Setenv("DECODE_TOKEN", leaked)
	_, err = r.Reload(t.Context())
	require.ErrorIs(t, err, ErrReload)
	assert.NotContains(t, err.Error(), leaked)
	require.Len(t, reported, 1)
	assert.NotContains(t, reported[0].Error(), leaked)
	assert.False(t, errors.Is(err, ErrReloadInProgress))
}

// The decoder's names resolve through squashed structs, map keys and slice
// indexes; a name that does not resolve is treated as sensitive.
func TestDecodeTarget(t *testing.T) {
	type inner struct {
		APIKey string `koanf:"api_key"`
		Host   string `koanf:"host"`
	}
	type auth struct {
		Mode string `koanf:"mode"`
	}
	type cfg struct {
		Embedded inner            `koanf:",squash"`
		Auth     auth             `koanf:"auth"`
		Pools    []inner          `koanf:"pools"`
		Byname   map[string]inner `koanf:"byname"`
	}
	root := reflect.TypeFor[cfg]()
	for _, tt := range []struct {
		name      string
		sensitive bool
	}{
		{"host", false},
		{"api_key", true},
		{"auth.mode", false}, // a struct branch's name does not cover its fields
		{"auth", true},       // but does cover the branch itself
		{"pools[0].host", false},
		{"pools[0].api_key", true},
		{"byname[primary].host", false},
		{"byname[credential].host", true},
		{"byname[a.b].host", false},
		{"missing", true},
		{"host[", true},
	} {
		_, sensitive := decodeTarget(root, tt.name)
		assert.Equal(t, tt.sensitive, sensitive, tt.name)
	}
}

func TestSplitDecodeName(t *testing.T) {
	segs, ok := splitDecodeName("features[a.b].limits[0].max")
	require.True(t, ok)
	var got []string
	for _, s := range segs {
		got = append(got, fmt.Sprintf("%s/%v", s.name, s.indexed))
	}
	assert.Equal(t, "features/false a.b/true limits/false 0/true max/false", strings.Join(got, " "))
	for _, bad := range []string{"", "a.[x", "[open", ".x"} {
		_, ok := splitDecodeName(bad)
		assert.False(t, ok, bad)
	}
}

// A struct squashed into its parent keeps its sensitive tag over the fields
// it contributes.
func TestLoad_DecodeErrorWithholdsThroughSquash(t *testing.T) {
	type inner struct {
		Rotated time.Time `koanf:"rotated"`
	}
	type cfg struct {
		Inner inner `koanf:",squash" sensitive:"true"`
	}
	_, err := Load[cfg](t.Context(), WithDefaults(map[string]any{"rotated": leaked}), WithSkipValidation())
	require.ErrorIs(t, err, ErrValidation)
	assert.NotContains(t, err.Error(), leaked)
	assert.Contains(t, err.Error(), "'rotated'")
}

// nestedDecode decodes itself with a decode of its own, whose errors carry
// names relative to it.
type nestedDecode struct {
	Rows []struct {
		Token time.Time `koanf:"token"`
	} `koanf:"rows"`
}

func (v *nestedDecode) UnmarshalMapstructure(input any) error {
	type raw nestedDecode
	d, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		TagName: "koanf", Result: (*raw)(v), DecodeHook: mapstructure.TextUnmarshallerHookFunc(),
	})
	if err != nil {
		return err
	}
	return fmt.Errorf("nested conversion: %w", d.Decode(input))
}

// A failure on a field whose type could hold a sensitive value anywhere
// beneath it — here a slice of structs with a token — is withheld whole.
func TestLoad_DecodeErrorWithholdsANestedDecode(t *testing.T) {
	type cfg struct {
		Block nestedDecode `koanf:"block"`
	}
	_, err := Load[cfg](t.Context(),
		WithBaseProvider(rawBytes("block:\n  rows:\n    - token: "+leaked+"\n"), yaml.Parser()), WithSkipValidation())
	require.ErrorIs(t, err, ErrValidation)
	assert.NotContains(t, err.Error(), leaked)
	assert.Contains(t, err.Error(), "'block'")
	assert.Contains(t, err.Error(), "may hold a sensitive value")
}

// remainDecode decodes itself into a remain map of times, so whatever key a
// source writes under it — a token — reaches a value-quoting decoder.
type remainDecode struct {
	Extra map[string]time.Time `koanf:",remain"`
}

func (v *remainDecode) UnmarshalMapstructure(input any) error {
	type raw remainDecode
	d, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		TagName: "koanf", Result: (*raw)(v), DecodeHook: mapstructure.TextUnmarshallerHookFunc(),
	})
	if err != nil {
		return err
	}
	return d.Decode(input)
}

// textComposite decodes a whole struct from one string, reporting the nested
// decode's error, which quotes it.
type textComposite struct {
	Token time.Time `koanf:"token"`
}

func (v *textComposite) UnmarshalText(input []byte) error {
	type raw textComposite
	d, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		TagName: "koanf", Result: (*raw)(v), DecodeHook: mapstructure.TextUnmarshallerHookFunc(),
	})
	if err != nil {
		return err
	}
	return d.Decode(map[string]any{"token": string(input)})
}

// A self-decoding struct's error is withheld when the struct could hold a
// sensitive value, whichever way it decodes.
func TestLoad_DecodeErrorWithholdsSelfDecodingStructs(t *testing.T) {
	t.Run("remain map", func(t *testing.T) {
		type cfg struct {
			Block remainDecode `koanf:"block"`
		}
		_, err := Load[cfg](t.Context(), WithBaseProvider(rawBytes("block:\n  token: "+leaked+"\n"), yaml.Parser()), WithSkipValidation())
		require.ErrorIs(t, err, ErrValidation)
		assert.NotContains(t, err.Error(), leaked)
		assert.Contains(t, err.Error(), "may hold a sensitive value")
	})
	t.Run("text unmarshaler", func(t *testing.T) {
		type cfg struct {
			Block textComposite `koanf:"block"`
		}
		_, err := Load[cfg](t.Context(), WithDefaults(map[string]any{"block": leaked}), WithSkipValidation())
		require.ErrorIs(t, err, ErrValidation)
		assert.NotContains(t, err.Error(), leaked)
		assert.Contains(t, err.Error(), "may hold a sensitive value")
	})
}

// A decode hook's error is a decode failure like any other: withheld on a
// field that could hold a sensitive value, so a hook that quotes its input
// cannot leak through it.
func TestLoad_DecodeHookErrorIsWithheldLikeADecodeError(t *testing.T) {
	type block struct {
		Token string `koanf:"token"`
		Port  int    `koanf:"port"`
	}
	type cfg struct {
		Block block `koanf:"block"`
	}
	hook := func(_, to reflect.Type, data any) (any, error) {
		if to == reflect.TypeFor[block]() {
			return nil, fmt.Errorf("bad block %v", data)
		}
		return data, nil
	}
	_, err := Load[cfg](t.Context(), WithDefaults(map[string]any{"block.token": leaked}), WithDecodeHook(hook), WithSkipValidation())
	require.ErrorIs(t, err, ErrValidation)
	assert.NotContains(t, err.Error(), leaked)
	assert.Contains(t, err.Error(), "'block'")
}

// Only sensitive fields, and fields that could hold one, are withheld: an
// ordinary field's decoder may quote what it was given, and a type mismatch
// that quotes nothing is kept whole even on a map.
func TestLoad_DecodeErrorOrdinaryFieldsKeepTheirMessage(t *testing.T) {
	type cfg struct {
		Start time.Time      `koanf:"start"`
		Ports map[string]int `koanf:"ports"`
	}
	_, err := Load[cfg](t.Context(), WithDefaults(map[string]any{"start": "not-a-time"}), WithSkipValidation())
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), `"not-a-time"`)

	_, err = Load[cfg](t.Context(), WithDefaults(map[string]any{"ports": 17}), WithSkipValidation())
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "'ports' expected type 'map[string]int', got unconvertible type 'int'")
	var de *mapstructure.DecodeError
	require.ErrorAs(t, err, &de)
}
