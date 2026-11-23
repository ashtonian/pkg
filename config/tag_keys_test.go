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

type repeatedSecretCfg struct {
	//lint:ignore SA5008 the repeated key is what a load refuses
	Password string `koanf:"password" sensitive:"true" default:"SECRET-MARKER" default:"x"`
}

// A schema error names the field and the tag key, never a tag's value: a
// default on a sensitive field is the secret itself. An unnamed section's
// type prints without its tags.
func TestLoad_SchemaErrorsLeaveTagValuesOut(t *testing.T) {
	_, err := Load[repeatedSecretCfg](t.Context(), WithSkipValidation())
	require.ErrorIs(t, err, ErrSchema)
	assert.NotContains(t, err.Error(), "SECRET-MARKER")
	assert.Contains(t, err.Error(), `field Password: struct tag repeats the key "default"`)

	for _, tag := range []reflect.StructTag{
		`koanf:"token" default:"SECRET-MARKER" env: "TOKEN"`,
		`koanf:"token" default:"SECRET-MARKER" default:"x"`,
	} {
		section := reflect.StructOf([]reflect.StructField{{Name: "Token", Type: reflect.TypeFor[string](), Tag: tag}})
		root := reflect.StructOf([]reflect.StructField{
			{Name: "Auth", Type: section, Tag: `koanf:"auth"`},
			{Name: "Other", Type: reflect.TypeFor[string](), Tag: `koanf:"other" default:"SECRET-MARKER"`},
		})
		_, err := loadSchema(root, buildOptions(nil))
		require.ErrorIs(t, err, ErrSchema)
		assert.NotContains(t, err.Error(), "SECRET-MARKER")
		assert.Contains(t, err.Error(), "field Token")
	}
}

type sharedDeclarations struct {
	Name string `koanf:"name" defualt:"x" sensitive:"maybe"`
}

type recursiveDeclarations struct {
	Name     string                           `koanf:"name"`
	Children []recursiveDeclarations          `koanf:"children"`
	ByName   map[string]recursiveDeclarations `koanf:"by_name"`
}

type sharedDeclarationsCfg struct {
	misspeltBase `koanf:",squash"`

	First  sharedDeclarations            `koanf:"first"`
	Second *sharedDeclarations           `koanf:"second"`
	Items  []misspeltItem                `koanf:"items"`
	Named  map[string]sharedDeclarations `koanf:"named"`
	Tree   recursiveDeclarations         `koanf:"tree"`
}

// The declaration checks read every struct of a graph once, whatever the
// paths that reach it: a type held at several paths, behind a pointer or in
// a collection is one set of declarations, and a recursive type ends.
func TestDeclarationChecks_VisitEachStructOnce(t *testing.T) {
	root, err := metadataOf(reflect.TypeFor[sharedDeclarationsCfg]())
	require.NoError(t, err)
	var visited []string
	root.eachDeclaring(func(view *gowalker.Schema) {
		visited = append(visited, gowalker.TypeText(view.Root().Type()))
	})
	assert.Equal(t, []string{
		"config.sharedDeclarationsCfg", "config.sharedDeclarations", "config.misspeltItem", "config.recursiveDeclarations",
	}, visited)

	e := schemaMetadata(reflect.TypeFor[sharedDeclarationsCfg]())
	var written []string
	for _, m := range e.misspelt {
		written = append(written, m.field+":"+m.written)
	}
	assert.Equal(t, []string{"Region:defualt", "Name:defualt", "Port:vaildate"}, written, "squashed and element declarations are checked, a shared one once")

	_, err = rootSchema(reflect.TypeFor[sharedDeclarationsCfg]())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), `field Name of config.sharedDeclarations has sensitive:"maybe"`)
}
