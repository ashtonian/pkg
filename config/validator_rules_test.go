package config

import (
	"context"
	"testing"

	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type misspeltRuleItem struct {
	Port int `koanf:"port" validate:"min=1"`
}

type misspeltRuleCfg struct {
	Name  string             `koanf:"name"`
	Count int                `koanf:"count" validate:"requird"`
	Items []misspeltRuleItem `koanf:"items" validate:"dive"`
}

type keysWithoutDiveCfg struct {
	Limits map[string]int `koanf:"limits" validate:"keys,min=1,endkeys"`
}

type elementRuleItem struct {
	Port int `koanf:"port" validate:"min=1,maxx=9"`
}

type elementRuleCfg struct {
	Items map[string]elementRuleItem `koanf:"items" validate:"dive"`
}

// A validate tag the default validator cannot read fails every load and
// key check with ErrSchema naming the field, before any source is read,
// rather than panicking in the middle of the load.
func TestDefaultValidator_RefusesUnreadableRules(t *testing.T) {
	read := 0
	src, err := NewMapProvider(func(context.Context) (map[string]any, error) { read++; return nil, nil }, "")
	require.NoError(t, err)
	opts := []Option{WithBaseProvider(src, nil)}

	_, err = Load[misspeltRuleCfg](t.Context(), opts...)
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), `field Count of config.misspeltRuleCfg has a validate tag the validator cannot read: Undefined validation function 'requird'`)
	assert.Zero(t, read, "no source is read")

	err = CheckKey[misspeltRuleCfg](t.Context(), "name", "x")
	require.ErrorIs(t, err, ErrSchema)
	var ke *KeyError
	assert.NotErrorAs(t, err, &ke, "a declaration is not one key's fault")
	_, err = DecodeKey[misspeltRuleCfg](t.Context(), "name", "x")
	require.ErrorIs(t, err, ErrSchema)

	_, err = Load[keysWithoutDiveCfg](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "field Limits of config.keysWithoutDiveCfg")
	assert.Contains(t, err.Error(), "'keys' tag must be immediately preceded by the 'dive' tag")

	_, err = Load[elementRuleCfg](t.Context())
	require.ErrorIs(t, err, ErrSchema, "an element's rules are read past a dive")
	assert.Contains(t, err.Error(), "field Port of config.elementRuleItem")
	assert.Contains(t, err.Error(), "'maxx'")
}

// A reloader whose type has an unreadable rule is never built, so nothing
// is published.
func TestDefaultValidator_ReloaderRefusesUnreadableRules(t *testing.T) {
	r, err := NewReloader[misspeltRuleCfg](t.Context(), nil)
	require.ErrorIs(t, err, ErrSchema)
	assert.Nil(t, r)

	r, err = NewReloaderFunc(t.Context(), func(ctx context.Context) (*misspeltRuleCfg, error) {
		return Load[misspeltRuleCfg](ctx)
	})
	require.ErrorIs(t, err, ErrSchema)
	assert.Nil(t, r)
}

// The check reads declarations only: a valid rule a value breaks is still a
// validation error, a rule a caller's validator registers still runs, and
// skipping validation reads no rule.
func TestDefaultValidator_RuleChecksAreDeclarationsOnly(t *testing.T) {
	_, err := Decode[misspeltRuleItem](t.Context(), map[string]any{"port": 0})
	require.ErrorIs(t, err, ErrValidation)
	assert.NotErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "port(min=1)")

	type evenCfg struct {
		Count int `koanf:"count" validate:"even"`
	}
	v := validator.New()
	ran := 0
	require.NoError(t, v.RegisterValidation("even", func(fl validator.FieldLevel) bool {
		ran++
		return fl.Field().Int()%2 == 0
	}))
	_, err = Decode[evenCfg](t.Context(), map[string]any{"count": 3}, WithValidator(v))
	require.ErrorIs(t, err, ErrValidation)
	got, err := Decode[evenCfg](t.Context(), map[string]any{"count": 4}, WithValidator(v))
	require.NoError(t, err)
	assert.Equal(t, 4, got.Count)
	assert.Equal(t, 2, ran)

	got2, err := Load[misspeltRuleCfg](t.Context(), WithSkipValidation())
	require.NoError(t, err)
	assert.Zero(t, got2.Count)
}

// A panic in the caller's own code — a rule it registered — is not a
// declaration problem and still panics.
func TestDefaultValidator_CallerPanicsAreTheirs(t *testing.T) {
	type boomCfg struct {
		Count int `koanf:"count" validate:"boom"`
	}
	v := validator.New()
	require.NoError(t, v.RegisterValidation("boom", func(validator.FieldLevel) bool { panic("caller rule") }))
	assert.PanicsWithValue(t, "caller rule", func() {
		_, _ = Load[boomCfg](t.Context(), WithValidator(v))
	})
}

type unreadInner struct {
	Port int `validate:"requird"`
}

type unreadSkippedCfg struct {
	Name  string      `koanf:"name"`
	Inner unreadInner `koanf:"-"`
}

type unreadText struct {
	Host string `validate:"hostnam"`
}

func (u *unreadText) UnmarshalText(b []byte) error { u.Host = string(b); return nil }

type unreadTextCfg struct {
	Addr unreadText `koanf:"addr"`
}

type unreadPointerCfg struct {
	Inner *unreadInner `koanf:"inner"`
}

type unreadEmbedded struct {
	Port int `koanf:"port" validate:"requird"`
}

type unreadEmbeddedCfg struct {
	unreadEmbedded `koanf:",squash"`
}

type unreadKeysItem struct {
	Port int `koanf:"port" validate:"maxx=9"`
}

type unreadKeysCfg struct {
	Items map[string]unreadKeysItem `koanf:"items" validate:"dive,keys,min=1,endkeys"`
}

// The pre-check reads the tags the default validator descends to, as it
// descends, not as config's schema does: a struct config skips (koanf:"-")
// or decodes whole from text is still validated field by field, and so are
// an embedded struct, a pointer's struct and, past a dive and its keys
// rules, a map's struct values.
func TestDefaultValidator_RefusesUnreadableRulesItDescendsTo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		load  func(ctx context.Context, opts ...Option) error
		field string
	}{
		{"koanf skipped struct", loadErrOpts[unreadSkippedCfg], "field Port of config.unreadInner"},
		{"text unmarshaler struct", loadErrOpts[unreadTextCfg], "field Host of config.unreadText"},
		{"pointer to struct", loadErrOpts[unreadPointerCfg], "field Port of config.unreadInner"},
		{"embedded struct", loadErrOpts[unreadEmbeddedCfg], "field Port of config.unreadEmbedded"},
		{"map values past keys", loadErrOpts[unreadKeysCfg], "field Port of config.unreadKeysItem"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := 0
			src, err := NewMapProvider(func(context.Context) (map[string]any, error) { read++; return nil, nil }, "")
			require.NoError(t, err)
			err = tc.load(t.Context(), WithBaseProvider(src, nil))
			require.ErrorIs(t, err, ErrSchema)
			assert.Contains(t, err.Error(), tc.field)
			assert.Zero(t, read, "refused before any source is read")
		})
	}
}

func loadErrOpts[T any](ctx context.Context, opts ...Option) error {
	_, err := Load[T](ctx, opts...)
	return err
}

type unreadElem struct {
	Port int `koanf:"port" validate:"maxx=9"`
}

type unreadNoDiveCfg struct {
	Items map[string]unreadElem `koanf:"items"`
	List  []unreadElem          `koanf:"list" validate:"min=0"`
}

type unreadStructOnlyCfg struct {
	Elem unreadOnly `koanf:"elem" validate:"structonly"`
}

type unreadOnly struct {
	Inner unreadElem `koanf:"inner"`
}

type unreadStructOnlyReadCfg struct {
	Elem unreadElem `koanf:"elem" validate:"structonly"`
}

type unreadKeyedCfg struct {
	Keyed map[unreadKey]int `koanf:"keyed" validate:"dive,keys,endkeys"`
}

type unreadKey struct {
	Name string `validate:"maxx=1"`
}

func (k unreadKey) MarshalText() ([]byte, error) { return []byte(k.Name), nil }

func (k *unreadKey) UnmarshalText(b []byte) error { k.Name = string(b); return nil }

// A rule the validator never reads is not refused: a collection's elements
// without a dive, the fields' values of a struct a structonly rule reaches
// (whose own tags it reads), a map key struct whose keys rules end at
// endkeys.
func TestDefaultValidator_LeavesUnreadRulesAlone(t *testing.T) {
	got, err := Decode[unreadNoDiveCfg](t.Context(), map[string]any{
		"items": map[string]any{"a": map[string]any{"port": 1}},
		"list":  []any{map[string]any{"port": 2}},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, got.Items["a"].Port)
	assert.Equal(t, 2, got.List[0].Port)

	_, err = Decode[unreadStructOnlyCfg](t.Context(), map[string]any{"elem": map[string]any{"inner": map[string]any{"port": 1}}})
	require.NoError(t, err)
	_, err = Decode[unreadStructOnlyReadCfg](t.Context(), nil)
	require.ErrorIs(t, err, ErrSchema, "structonly still reads the struct's own tags")
	assert.Contains(t, err.Error(), "field Port of config.unreadElem")

	keyed, err := Decode[unreadKeyedCfg](t.Context(), map[string]any{"keyed": map[string]any{"ab": 1}})
	require.NoError(t, err)
	assert.Equal(t, 1, keyed.Keyed[unreadKey{Name: "ab"}])
}

type applyDiveOnIntCfg struct {
	Count int `koanf:"count" validate:"dive"`
}

type applyBadParamCfg struct {
	Count int `koanf:"count" validate:"min=abc"`
}

type applyLenOnBoolCfg struct {
	On bool `koanf:"on" validate:"len=3"`
}

type applyOneOfOnFloatCfg struct {
	Ratio float64 `koanf:"ratio" validate:"oneof=1 2"`
}

type applyRequiredIfCfg struct {
	Mode  string `koanf:"mode"`
	Count int    `koanf:"count" validate:"required_if=Mode"`
}

type applyDynamicCfg struct {
	Plugin any `koanf:"plugin"`
}

type applyDynamicPlugin struct {
	Port int `validate:"requird"`
}

// A rule that cannot apply to its field's type panics in go-playground
// wherever validation meets it. A load, a key check and a reloader's first
// load return it as ErrSchema carrying the validator's message, which names
// the field where the validator does.
func TestDefaultValidator_RuleThatCannotApplyIsSchemaError(t *testing.T) {
	for _, tc := range []struct {
		name string
		load func(context.Context) error
		msg  string
	}{
		{"dive on an int", func(ctx context.Context) error {
			_, err := Decode[applyDiveOnIntCfg](ctx, map[string]any{"count": 1})
			return err
		}, "can't dive on a non slice or map"},
		{"unparsable param", func(ctx context.Context) error {
			_, err := Decode[applyBadParamCfg](ctx, map[string]any{"count": 1})
			return err
		}, `parsing "abc": invalid syntax`},
		{"len on a bool", func(ctx context.Context) error {
			_, err := Decode[applyLenOnBoolCfg](ctx, map[string]any{"on": true})
			return err
		}, "Bad field type bool"},
		{"oneof on a float", func(ctx context.Context) error {
			_, err := Decode[applyOneOfOnFloatCfg](ctx, map[string]any{"ratio": 1.5})
			return err
		}, "Bad field type float64"},
		{"required_if with an odd param count", func(ctx context.Context) error {
			_, err := Decode[applyRequiredIfCfg](ctx, map[string]any{"mode": "x"})
			return err
		}, "Bad param number for required_if count"},
		{"unreadable tag beneath an interface", func(ctx context.Context) error {
			_, err := Load[applyDynamicCfg](ctx, WithPostLoad(func(_ context.Context, c *applyDynamicCfg) error {
				c.Plugin = applyDynamicPlugin{Port: 1}
				return nil
			}))
			return err
		}, "Undefined validation function 'requird' on field 'Port'"},
		{"reloader's first load", func(ctx context.Context) error {
			r, err := NewReloader[applyBadParamCfg](ctx, []Option{WithDefaults(map[string]any{"count": 1})})
			assert.Nil(t, r)
			return err
		}, `parsing "abc": invalid syntax`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() { err = tc.load(t.Context()) })
			require.ErrorIs(t, err, ErrSchema)
			assert.NotErrorIs(t, err, ErrValidation)
			assert.Contains(t, err.Error(), "does not apply to its field")
			assert.Contains(t, err.Error(), tc.msg)
		})
	}
}

type panickyValue struct{ N int }

func (panickyValue) Validate() error { panic("caller method") }

type callerMethodCfg struct {
	Value panickyValue `koanf:"value" validate:"validateFn"`
}

// A key check meets the same rule: its error is the declaration's.
func TestDefaultValidator_KeyCheckRuleThatCannotApplyIsSchemaError(t *testing.T) {
	var err error
	require.NotPanics(t, func() { _, err = DecodeKey[applyDiveOnIntCfg](t.Context(), "count", 1) })
	require.ErrorIs(t, err, ErrSchema)
}

// A panic raised by the caller's own code that the default validator calls
// — a validateFn rule's method — is the caller's and still panics.
func TestDefaultValidator_CallerMethodPanicsAreTheirs(t *testing.T) {
	assert.PanicsWithValue(t, "caller method", func() {
		_, _ = Decode[callerMethodCfg](t.Context(), map[string]any{"value": map[string]any{"n": 1}})
	})
}
