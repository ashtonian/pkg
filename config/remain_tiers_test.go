package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type remainMisspeltItem struct {
	Port int `koanf:"port" defualt:"8"`
}

type remainMisspeltCfg struct {
	Plugins struct {
		Extra map[string]remainMisspeltItem `koanf:",remain"`
	} `koanf:"plugins"`
}

type remainBadExampleItem struct {
	Port int `koanf:"port" example:"eight"`
}

type remainBadExampleCfg struct {
	Plugins struct {
		Extra map[string]remainBadExampleItem `koanf:",remain"`
	} `koanf:"plugins"`
}

// A remain field's element holds a remain field of its own.
type remainNestedItem struct {
	Inner struct {
		Extra map[string]remainMisspeltItem `koanf:",remain"`
	} `koanf:"inner"`
}

type remainNestedCfg struct {
	Plugins struct {
		Extra map[string]remainNestedItem `koanf:",remain"`
	} `koanf:"plugins"`
}

// remainLoopRoot and remainLoopLeaf reach each other through their remain
// fields only.
type remainLoopRoot struct {
	Name string                    `koanf:"name"`
	Rest map[string]remainLoopLeaf `koanf:",remain"`
}

type remainLoopLeaf struct {
	Port int                       `koanf:"port" defualt:"8"`
	More map[string]remainLoopRoot `koanf:",remain"`
}

type remainLoopOKRoot struct {
	Name string                      `koanf:"name"`
	Rest map[string]remainLoopOKLeaf `koanf:",remain"`
}

type remainLoopOKLeaf struct {
	Port int                         `koanf:"port"`
	More map[string]remainLoopOKRoot `koanf:",remain"`
}

// A remain field's element type is a type of the config like any field's:
// every entry point checks its tags at the tier it checks the config's, so
// a load refuses the declaration Diff refuses, however deep the remain
// field, and a type reaching itself through remain fields is checked once.
func TestLoad_ChecksARemainFieldsElementTypeAsDiffDoes(t *testing.T) {
	ctx := t.Context()
	refused := func(t *testing.T, load, diff, key error, says string) {
		t.Helper()
		for name, err := range map[string]error{"Load": load, "Diff": diff, "CheckKey": key} {
			require.ErrorIs(t, err, ErrSchema, name)
			assert.Contains(t, err.Error(), says, name)
		}
	}

	t.Run("a misspelt tag key", func(t *testing.T) {
		_, load := Load[remainMisspeltCfg](ctx, WithSkipValidation())
		a, b := remainMisspeltCfg{}, remainMisspeltCfg{}
		b.Plugins.Extra = map[string]remainMisspeltItem{"foo": {Port: 1}}
		_, diff := Diff(&a, &b)
		refused(t, load, diff, CheckKey[remainMisspeltCfg](ctx, "plugins.foo.port", 1),
			`field Port of config.remainMisspeltItem has tag key "defualt"; did you mean "default"?`)
	})

	t.Run("an example its field cannot hold", func(t *testing.T) {
		_, load := Load[remainBadExampleCfg](ctx, WithSkipValidation())
		a, b := remainBadExampleCfg{}, remainBadExampleCfg{}
		b.Plugins.Extra = map[string]remainBadExampleItem{"foo": {Port: 1}}
		_, diff := Diff(&a, &b)
		refused(t, load, diff, CheckKey[remainBadExampleCfg](ctx, "plugins.foo.port", 1), "field Port of config.remainBadExampleItem has an example that does not decode into int")
	})

	t.Run("within a remain field's element", func(t *testing.T) {
		_, load := Load[remainNestedCfg](ctx, WithSkipValidation())
		a, b := remainNestedCfg{}, remainNestedCfg{}
		item := remainNestedItem{}
		item.Inner.Extra = map[string]remainMisspeltItem{"bar": {Port: 1}}
		b.Plugins.Extra = map[string]remainNestedItem{"foo": item}
		_, diff := Diff(&a, &b)
		refused(t, load, diff, CheckKey[remainNestedCfg](ctx, "plugins.foo.inner.bar.port", 1), `tag key "defualt"`)
	})

	t.Run("through remain fields that reach each other", func(t *testing.T) {
		_, load := Load[remainLoopRoot](ctx, WithSkipValidation())
		a, b := remainLoopRoot{}, remainLoopRoot{Rest: map[string]remainLoopLeaf{"foo": {Port: 1}}}
		_, diff := Diff(&a, &b)
		refused(t, load, diff, CheckKey[remainLoopRoot](ctx, "foo.port", 1), `field Port of config.remainLoopLeaf has tag key "defualt"`)

		got, err := Load[remainLoopOKRoot](ctx, WithDefaults(map[string]any{"foo": map[string]any{"port": 1, "bar": map[string]any{"name": "n"}}}))
		require.NoError(t, err)
		assert.Equal(t, "n", got.Rest["foo"].More["bar"].Name)
	})
}
