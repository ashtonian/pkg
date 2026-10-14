//go:build unit

package config

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type keyOfDB struct {
	Host string `koanf:"host"`
	Port int    `koanf:"port"`
}

type KeyOfBase struct {
	Region string `koanf:"region"`
}

type keyOfCfg struct {
	KeyOfBase `koanf:",squash"`
	DB        keyOfDB           `koanf:"db"`
	Opt       *keyOfDB          `koanf:"opt"`
	Limits    map[string]int    `koanf:"limits"`
	Items     []keyOfDB         `koanf:"items"`
	Skip      string            `koanf:"-"`
	Next      *keyOfCfg         `koanf:"next"`
	Marks     [2]struct{}       `koanf:"marks"`
	Labels    map[string]string `koanf:"labels"`
}

func TestKeyOf(t *testing.T) {
	var c keyOfCfg
	for _, tt := range []struct {
		key string
		got func() (string, error)
	}{
		{"db", func() (string, error) { return KeyOf(&c, &c.DB) }},
		{"db.port", func() (string, error) { return KeyOf(&c, &c.DB.Port) }},
		{"region", func() (string, error) { return KeyOf(&c, &c.Region) }},
		{"opt", func() (string, error) { return KeyOf(&c, &c.Opt) }},
		{"limits", func() (string, error) { return KeyOf(&c, &c.Limits) }},
		{"items", func() (string, error) { return KeyOf(&c, &c.Items) }},
	} {
		got, err := tt.got()
		require.NoError(t, err, tt.key)
		assert.Equal(t, tt.key, got)
	}

	c.Opt = &keyOfDB{}
	got, err := KeyOf(&c, &c.Opt.Host)
	require.NoError(t, err)
	assert.Equal(t, "opt.host", got)

	c.Next = &c // a cycle ends
	got, err = KeyOf(&c, &c.Labels)
	require.NoError(t, err)
	assert.Equal(t, "labels", got)
}

func TestKeyOf_Refuses(t *testing.T) {
	var c keyOfCfg
	var other keyOfCfg
	c.Items = []keyOfDB{{}}
	for name, err := range map[string]error{
		"the root":            second(KeyOf(&c, &c)),
		"a skipped field":     second(KeyOf(&c, &c.Skip)),
		"a squashed struct":   second(KeyOf(&c, &c.KeyOfBase)),
		"a list element":      second(KeyOf(&c, &c.Items[0].Port)),
		"another config":      second(KeyOf(&c, &other.DB.Port)),
		"behind a nil":        second(KeyOf(&c, (*keyOfDB)(nil))),
		"shared address":      second(KeyOf(&c, &c.Marks[0])),
		"nil config":          second(KeyOf[keyOfCfg](nil, &c.DB)),
		"unaddressable value": second(KeyOfValue(&c, reflect.ValueOf(1))),
	} {
		assert.ErrorIs(t, err, ErrKey, name)
	}
}

func second[A any](_ A, err error) error { return err }
