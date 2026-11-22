package config

import (
	"reflect"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The keys config reads are far enough from other libraries' keys that
// none is taken for a misspelling.
func TestTagKeys_Misspellings(t *testing.T) {
	tests := []struct {
		written string
		meant   string
	}{
		{"defualt", "default"},
		{"vaildate", "validate"},
		{"konaf", "koanf"},
		{"sensitve", "sensitive"},
		{"env_ref", "envref"},
		{"DESC", "desc"},
		{"secert", "secret"},
		{"exmaple", "example"},
		{"sep", ""},
		{"json", ""},
		{"yaml", ""},
		{"mapstructure", ""},
		{"help", ""},
		{"short", ""},
		{"enum", ""},
		{"doc", ""},
		{"form", ""},
	}
	for _, tt := range tests {
		t.Run(tt.written, func(t *testing.T) {
			meant, ok := gowalker.MisspeltTagKey(tt.written, tagKeys)
			assert.Equal(t, tt.meant, meant)
			assert.Equal(t, tt.meant != "", ok)
		})
	}
}

type misspeltBase struct {
	Region string `koanf:"region" defualt:"eu"`
}

type misspeltItem struct {
	Port int `koanf:"port" vaildate:"min=1"`
}

type misspeltCfg struct {
	misspeltBase `koanf:",squash"`

	Host  string         `koanf:"host" json:"host" yaml:"host" help:"the host"`
	Items []misspeltItem `koanf:"items"`
}

// A load refuses every misspelt key of its type's graph, through squashed
// structs and list elements, naming the field, the key written and the key
// meant, unless another library's key is named as foreign.
func TestLoad_RefusesMisspeltTagKeys(t *testing.T) {
	_, err := Load[misspeltCfg](t.Context(), WithSkipValidation())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), `field Region of config.misspeltBase has tag key "defualt"; did you mean "default"?`)
	assert.Contains(t, err.Error(), `field Port of config.misspeltItem has tag key "vaildate"; did you mean "validate"?`)
	assert.NotContains(t, err.Error(), `"json"`)

	err = CheckKey[misspeltCfg](t.Context(), "host", "h", WithForeignTagKeys("defualt"))
	require.ErrorIs(t, err, ErrSchema)
	assert.NotContains(t, err.Error(), "defualt")
	assert.Contains(t, err.Error(), "vaildate")

	got, err := Load[misspeltCfg](t.Context(), WithForeignTagKeys("defualt"), WithForeignTagKeys("vaildate"),
		WithDefaults(map[string]any{"host": "h"}), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, "h", got.Host)
	assert.Empty(t, got.Region, "a foreign key is not config's")
}

// A tag reflect cannot read whole, or one that repeats a key, is refused
// before anything loads: reflect would read only part of it. The types are
// built at run time, as vet refuses such tags in source.
func TestLoad_RefusesUnreadableTags(t *testing.T) {
	for _, tag := range []reflect.StructTag{`koanf:port default:"8"`, `koanf:"port" default:"8" default:"9"`} {
		t.Run(string(tag), func(t *testing.T) {
			typ := reflect.StructOf([]reflect.StructField{{Name: "Port", Type: reflect.TypeFor[int](), Tag: tag}})
			_, err := loadSchema(typ, buildOptions(nil))
			require.ErrorIs(t, err, ErrSchema)
			assert.Contains(t, err.Error(), "Port")
		})
	}
}
