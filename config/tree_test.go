package config

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type treeText struct{ value string }

func (v *treeText) UnmarshalText(b []byte) error { v.value = string(b); return nil }
func (v *treeText) MarshalText() ([]byte, error) { return []byte(v.value), nil }

func TestTree_ByteArrayRoundTrip(t *testing.T) {
	type cfg struct {
		Bytes [3]byte `koanf:"bytes"`
	}
	original := cfg{Bytes: [3]byte{0, 128, 255}}
	tree, err := Tree(original)
	require.NoError(t, err)
	loaded, err := Decode[cfg](t.Context(), tree)
	require.NoError(t, err)
	assert.Equal(t, original, *loaded)
	projected, err := TreeFunc(original, func(key string, f Field, value any) any {
		assert.Equal(t, "bytes", key)
		assert.Equal(t, reflect.TypeFor[[3]byte](), f.Type)
		return value
	})
	require.NoError(t, err)
	assert.Equal(t, tree, projected)
}

func TestTree_RoundTripAndLeafPolicies(t *testing.T) {
	type child struct {
		Port int `koanf:"port"`
	}
	type cfg struct {
		Password  string              `koanf:"password"`
		Duration  time.Duration       `koanf:"duration"`
		Time      time.Time           `koanf:"time"`
		Text      map[string]treeText `koanf:"text"`
		Bytes     []byte              `koanf:"bytes"`
		NilBytes  []byte              `koanf:"nil_bytes"`
		NilList   []string            `koanf:"nil_list"`
		EmptyList []string            `koanf:"empty_list"`
		Child     *child              `koanf:"child"`
		Tokens    map[string]string   `koanf:"tokens"`
		Secrets   []string            `koanf:"secrets"`
		Dynamic   any                 `koanf:"dynamic" sensitive:"true"`
	}
	original := cfg{Password: "pass", Duration: time.Minute, Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Text: map[string]treeText{"a.b": {value: "hello"}}, Bytes: []byte("data"), EmptyList: []string{},
		Child: &child{Port: 80}, Tokens: map[string]string{"private-key": "private-value"}, Secrets: []string{"hidden"},
		Dynamic: map[string]any{"hidden": "value"}}
	tree, err := Tree(original)
	require.NoError(t, err)
	loaded, err := Decode[cfg](t.Context(), tree)
	require.NoError(t, err)
	assert.Equal(t, original, *loaded)
	identity, err := TreeFunc(original, func(_ string, _ Field, value any) any { return value })
	require.NoError(t, err)
	assert.Equal(t, tree, identity)
	var seen []string
	redacted, err := TreeFunc(original, func(key string, f Field, value any) any {
		seen = append(seen, key)
		declared, err := FieldFor[cfg](key)
		require.NoError(t, err)
		assert.Equal(t, declared.Path, f.Path, key)
		assert.Equal(t, declared.Type, f.Type, key)
		if f.Sensitive && value != nil {
			return redactedValue
		}
		return value
	})
	require.NoError(t, err)
	assert.Equal(t, RedactedTree(original), redacted)
	assert.Contains(t, seen, "tokens")
	assert.Contains(t, seen, "secrets")
	assert.Contains(t, seen, "dynamic")
	assert.NotContains(t, seen, `tokens["private-key"]`)
	assert.NotContains(t, seen, "secrets[0]")
	assert.NotContains(t, seen, `dynamic["hidden"]`)
	assert.Contains(t, seen, `text["a.b"]`)
	_, err = TreeFunc(original, func(key string, _ Field, value any) any {
		if key == "tokens" {
			value.(map[string]any)["private-key"] = "changed"
		}
		return value
	})
	require.NoError(t, err)
	assert.Equal(t, "private-value", original.Tokens["private-key"])
}

func TestTree_InterfaceOwnerAndPointerPolicy(t *testing.T) {
	type row struct {
		Value string `koanf:"value"`
	}
	type cfg struct {
		Data   any  `koanf:"data" desc:"declared interface"`
		Secret *any `koanf:"secret"`
	}
	secret := any(row{Value: "hidden"})
	original := cfg{Data: []row{{Value: "visible"}}, Secret: &secret}
	var seen []string
	_, err := TreeFunc(original, func(key string, f Field, value any) any {
		seen = append(seen, key)
		if key == "data[0].value" {
			assert.Equal(t, "data", f.Path)
			assert.Equal(t, reflect.TypeFor[any](), f.Type)
			assert.Equal(t, "declared interface", f.Tag.Get("desc"))
		}
		return value
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"data[0].value", "secret"}, seen)
	assert.Equal(t, redactedValue, RedactedTree(original)["secret"])
}

var errTreeMarshal = errors.New("cannot encode text")

type failingTreeText struct{}

func (*failingTreeText) UnmarshalText([]byte) error   { return nil }
func (*failingTreeText) MarshalText() ([]byte, error) { return nil, errTreeMarshal }

type opaqueTreeText struct{ Value string }

func (*opaqueTreeText) UnmarshalText([]byte) error { return nil }

func TestTree_ProjectionErrors(t *testing.T) {
	_, err := Tree(struct {
		Text failingTreeText `koanf:"text"`
	}{})
	require.ErrorIs(t, err, errTreeMarshal)
	assert.Contains(t, err.Error(), "text")
	_, err = Tree(struct {
		Text opaqueTreeText `koanf:"text"`
	}{})
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "TextMarshaler")
	assert.Equal(t, redactedValue, RedactedTree(struct {
		Text opaqueTreeText `koanf:"text" sensitive:"true"`
	}{})["text"])
}
