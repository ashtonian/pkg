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
