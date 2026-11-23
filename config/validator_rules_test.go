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
	Items map[string]elementRuleItem `koanf:"items"`
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
	require.ErrorIs(t, err, ErrSchema, "an element's rules are read too")
	assert.Contains(t, err.Error(), "field Port of config.elementRuleItem")
	assert.Contains(t, err.Error(), "'maxx'")
	assert.NotContains(t, err.Error(), "9", "a rule's param is never quoted")
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
