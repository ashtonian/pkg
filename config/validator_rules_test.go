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

type unreadEmbedded struct {
	Port int `koanf:"port" validate:"requird"`
}

type unreadEmbeddedCfg struct {
	unreadEmbedded `koanf:",squash"`
}

type unreadSectionCfg struct {
	Outer unreadOuter `koanf:"outer"`
}

type unreadOuter struct {
	Inner unreadInner `koanf:"inner"`
}

// The pre-check reads the tags the default validator reads on every
// validation, as it descends, not as config's schema does: a struct config
// skips (koanf:"-") or decodes whole from text is still validated field by
// field, and so are an embedded struct and a section's sections.
func TestDefaultValidator_RefusesUnreadableRulesItDescendsTo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		load  func(ctx context.Context, opts ...Option) error
		field string
	}{
		{"koanf skipped struct", loadErrOpts[unreadSkippedCfg], "field Port of config.unreadInner"},
		{"text unmarshaler struct", loadErrOpts[unreadTextCfg], "field Host of config.unreadText"},
		{"embedded struct", loadErrOpts[unreadEmbeddedCfg], "field Port of config.unreadEmbedded"},
		{"nested section", loadErrOpts[unreadSectionCfg], "field Port of config.unreadInner"},
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

// libInner's rule is registered on a validator other than config's, as a
// library's own type may carry.
type libInner struct {
	Host string `validate:"lib_host"`
}

type behindPointerCfg struct {
	Name   string    `koanf:"name"`
	Handle *libInner `koanf:"-"`
}

type behindSliceCfg struct {
	Items []libInner `koanf:"items" validate:"dive"`
}

type behindMapCfg struct {
	Items map[string]libInner `koanf:"items" validate:"dive,keys,min=1,endkeys"`
}

type behindArrayPointerCfg struct {
	Items [1]*libInner `koanf:"-" validate:"dive"`
}

type behindOmitEmptyCfg struct {
	Lib libInner `koanf:"-" validate:"omitempty"`
}

// valuerInner is validated as the string it returns, not as its fields.
type valuerInner struct {
	Host string `validate:"lib_host"`
}

func (v valuerInner) ValidatorValue() any { return v.Host }

type behindValuerCfg struct {
	Lib valuerInner `koanf:"-"`
}

// The validator reads a struct behind a pointer, a slice, a map or an
// interface, or beneath omitempty, only when a value puts one there, and a
// struct that names its own ValidatorValue never: the pre-check leaves
// their tags alone, and a config that leaves them out loads. A value that
// does put one there meets the tag when validation does, as ErrSchema.
func TestDefaultValidator_LeavesNilableEdgesToValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		empty func(context.Context) error
		set   func(context.Context) error
	}{
		{"pointer", func(ctx context.Context) error {
			_, err := Decode[behindPointerCfg](ctx, map[string]any{"name": "x"})
			return err
		}, func(ctx context.Context) error {
			_, err := Load[behindPointerCfg](ctx, WithPostLoad(func(_ context.Context, c *behindPointerCfg) error {
				c.Handle = &libInner{Host: "h"}
				return nil
			}))
			return err
		}},
		{"slice", func(ctx context.Context) error {
			_, err := Decode[behindSliceCfg](ctx, nil)
			return err
		}, func(ctx context.Context) error {
			_, err := Decode[behindSliceCfg](ctx, map[string]any{"items": []any{map[string]any{"host": "h"}}})
			return err
		}},
		{"map", func(ctx context.Context) error {
			_, err := Decode[behindMapCfg](ctx, nil)
			return err
		}, func(ctx context.Context) error {
			_, err := Decode[behindMapCfg](ctx, map[string]any{"items": map[string]any{"a": map[string]any{"host": "h"}}})
			return err
		}},
		{"pointer in an array", func(ctx context.Context) error {
			_, err := Decode[behindArrayPointerCfg](ctx, nil)
			return err
		}, func(ctx context.Context) error {
			_, err := Load[behindArrayPointerCfg](ctx, WithPostLoad(func(_ context.Context, c *behindArrayPointerCfg) error {
				c.Items[0] = &libInner{Host: "h"}
				return nil
			}))
			return err
		}},
		{"omitempty", func(ctx context.Context) error {
			_, err := Decode[behindOmitEmptyCfg](ctx, nil)
			return err
		}, func(ctx context.Context) error {
			_, err := Load[behindOmitEmptyCfg](ctx, WithPostLoad(func(_ context.Context, c *behindOmitEmptyCfg) error {
				c.Lib.Host = "h"
				return nil
			}))
			return err
		}},
		{"validator value", func(ctx context.Context) error {
			_, err := Load[behindValuerCfg](ctx, WithPostLoad(func(_ context.Context, c *behindValuerCfg) error {
				c.Lib.Host = "h"
				return nil
			}))
			return err
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.empty != nil {
				require.NoError(t, tc.empty(t.Context()))
			}
			if tc.set != nil {
				var err error
				require.NotPanics(t, func() { err = tc.set(t.Context()) })
				require.ErrorIs(t, err, ErrSchema)
				assert.Contains(t, err.Error(), "Undefined validation function 'lib_host' on field 'Host'")
			}
		})
	}
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
// load return it as ErrSchema naming the field by key and carrying the
// validator's message.
func TestDefaultValidator_RuleThatCannotApplyIsSchemaError(t *testing.T) {
	for _, tc := range []struct {
		name string
		load func(context.Context) error
		at   string
		msg  string
	}{
		{"unparsable param", func(ctx context.Context) error {
			_, err := Decode[applyBadParamCfg](ctx, map[string]any{"count": 1})
			return err
		}, "count", `parsing "abc": invalid syntax`},
		{"len on a bool", func(ctx context.Context) error {
			_, err := Decode[applyLenOnBoolCfg](ctx, map[string]any{"on": true})
			return err
		}, "on", "Bad field type bool"},
		{"oneof on a float", func(ctx context.Context) error {
			_, err := Decode[applyOneOfOnFloatCfg](ctx, map[string]any{"ratio": 1.5})
			return err
		}, "ratio", "Bad field type float64"},
		{"required_if with an odd param count", func(ctx context.Context) error {
			_, err := Decode[applyRequiredIfCfg](ctx, map[string]any{"mode": "x"})
			return err
		}, "count", "Bad param number for required_if count"},
		{"unreadable tag beneath an interface", func(ctx context.Context) error {
			_, err := Load[applyDynamicCfg](ctx, WithPostLoad(func(_ context.Context, c *applyDynamicCfg) error {
				c.Plugin = applyDynamicPlugin{Port: 1}
				return nil
			}))
			return err
		}, "plugin", "Undefined validation function 'requird' on field 'Port'"},
		{"reloader's first load", func(ctx context.Context) error {
			r, err := NewReloader[applyBadParamCfg](ctx, []Option{WithDefaults(map[string]any{"count": 1})})
			assert.Nil(t, r)
			return err
		}, "count", `parsing "abc": invalid syntax`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() { err = tc.load(t.Context()) })
			require.ErrorIs(t, err, ErrSchema)
			assert.NotErrorIs(t, err, ErrValidation)
			assert.Contains(t, err.Error(), "does not apply to its field at "+tc.at+": ")
			assert.Contains(t, err.Error(), tc.msg)
		})
	}
}

type diveSection struct {
	Count int `koanf:"count" validate:"dive"`
}

type keysOnSliceSection struct {
	List []string `koanf:"list" validate:"dive,keys,min=1,endkeys"`
}

type misappliedDiveCfg struct {
	Name      string             `koanf:"name"`
	Count     int                `koanf:"count" validate:"dive"`
	List      []string           `koanf:"list" validate:"omitempty,dive,keys,min=1,endkeys"`
	Matrix    [][]int            `koanf:"matrix" validate:"dive,dive,dive"`
	Pointer   *int               `koanf:"pointer" validate:"dive"`
	KeyDive   map[string]int     `koanf:"key_dive" validate:"dive,keys,dive,endkeys"`
	ValueDive map[string]int     `koanf:"value_dive" validate:"dive,keys,min=1,endkeys,dive"`
	Section   diveSection        `koanf:"section"`
	Skipped   keysOnSliceSection `koanf:"-"`
}

type appliedDivesCfg struct {
	Ints     []int            `koanf:"ints" validate:"dive,min=1"`
	Lists    map[string][]int `koanf:"lists" validate:"dive,keys,min=1,endkeys,dive,min=0"`
	Arrays   [][2]int         `koanf:"arrays" validate:"dive,dive"`
	Pointers []*[]int         `koanf:"pointers" validate:"dive,dive"`
	Dynamic  []any            `koanf:"dynamic" validate:"dive,dive"`
	Labels   map[string]int   `koanf:"labels" validate:"dive,keys,min=1"`
}

// A dive a field's tag takes on a kind it cannot walk — a dive on an int,
// keys after a dive on a slice — is wrong whatever the field holds, so a
// field the validator reads on every validation has it refused before any
// source is read, by field and rule, though the validator would only panic
// on it once the field held something to walk.
func TestDefaultValidator_RefusesDivesThatCannotApply(t *testing.T) {
	read := 0
	src, err := NewMapProvider(func(context.Context) (map[string]any, error) { read++; return nil, nil }, "")
	require.NoError(t, err)
	_, err = Load[misappliedDiveCfg](t.Context(), WithBaseProvider(src, nil))
	require.ErrorIs(t, err, ErrSchema)
	assert.Zero(t, read, "refused before any source is read")
	for _, want := range []string{
		"field Count of config.misappliedDiveCfg has a validate tag the validator cannot apply: dive on int, which is not a slice, an array or a map",
		"field List of config.misappliedDiveCfg has a validate tag the validator cannot apply: keys after a dive on []string, which is not a map",
		"field Matrix of config.misappliedDiveCfg has a validate tag the validator cannot apply: dive on int,",
		"field Pointer of config.misappliedDiveCfg has a validate tag the validator cannot apply: dive on int,",
		"field KeyDive of config.misappliedDiveCfg has a validate tag the validator cannot apply: dive on string,",
		"field ValueDive of config.misappliedDiveCfg has a validate tag the validator cannot apply: dive on int,",
		"field Count of config.diveSection has a validate tag the validator cannot apply: dive on int,",
		"field List of config.keysOnSliceSection has a validate tag the validator cannot apply: keys after a dive on []string,",
	} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "Name")

	got, err := Decode[appliedDivesCfg](t.Context(), map[string]any{
		"ints":     []any{1},
		"lists":    map[string]any{"a": []any{0}},
		"arrays":   []any{[]any{1, 2}},
		"pointers": []any{[]any{1}},
		"dynamic":  []any{[]any{"x"}},
		"labels":   map[string]any{"b": 1},
	})
	require.NoError(t, err, "dives that apply are not refused")
	assert.Equal(t, []int{1}, got.Ints)
}

type arrayOfLibCfg struct {
	Items [1]libInner `koanf:"-" validate:"dive"`
}

type arrayOfDiveSectionCfg struct {
	Sections [2]diveSection `koanf:"sections" validate:"dive"`
}

type arrayOfKeysOnSliceCfg struct {
	Sections [1]keysOnSliceSection `koanf:"-" validate:"dive"`
}

type emptyArraysCfg struct {
	Lib      [0]libInner    `koanf:"-" validate:"dive"`
	Sections [0]diveSection `koanf:"-" validate:"dive"`
	Ints     [0]int         `koanf:"-" validate:"dive,dive"`
	Keys     [0]string      `koanf:"-" validate:"dive,keys,min=1,endkeys"`
}

// An array of one or more elements holds them on every validation, so a
// dive into it reaches each element's struct as a field held by value does:
// an unreadable tag or a misapplied dive there is refused before any source
// is read. An array of no elements is never walked, and nothing beneath its
// dive is refused.
func TestDefaultValidator_ArraysAreReachedByLength(t *testing.T) {
	for _, tc := range []struct {
		name string
		load func(context.Context, Option) error
		want string
	}{
		{"unreadable tag in an element", func(ctx context.Context, src Option) error {
			_, err := Load[arrayOfLibCfg](ctx, src)
			return err
		}, "field Host of config.libInner has a validate tag the validator cannot read: Undefined validation function 'lib_host'"},
		{"dive on an element's int", func(ctx context.Context, src Option) error {
			_, err := Load[arrayOfDiveSectionCfg](ctx, src)
			return err
		}, "field Count of config.diveSection has a validate tag the validator cannot apply: dive on int,"},
		{"keys on an element's empty slice", func(ctx context.Context, src Option) error {
			_, err := Load[arrayOfKeysOnSliceCfg](ctx, src)
			return err
		}, "field List of config.keysOnSliceSection has a validate tag the validator cannot apply: keys after a dive on []string,"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := 0
			src, err := NewMapProvider(func(context.Context) (map[string]any, error) { read++; return nil, nil }, "")
			require.NoError(t, err)
			err = tc.load(t.Context(), WithBaseProvider(src, nil))
			require.ErrorIs(t, err, ErrSchema)
			assert.Contains(t, err.Error(), tc.want)
			assert.Zero(t, read, "refused before any source is read")
		})
	}

	_, err := Decode[emptyArraysCfg](t.Context(), nil)
	require.NoError(t, err)
}

type behindDivePointerCfg struct {
	Section *diveSection `koanf:"section"`
}

type behindKeysSliceCfg struct {
	Items []keysOnSliceSection `koanf:"items" validate:"dive"`
}

// The same dive in a struct the validator reaches only through a value is
// left to validation, which names the field it was validating by key; the
// field's value is never shown.
func TestDefaultValidator_DiveBehindAnEdgeNamesItsField(t *testing.T) {
	_, err := Decode[behindDivePointerCfg](t.Context(), nil)
	require.NoError(t, err)
	_, err = Decode[behindDivePointerCfg](t.Context(), map[string]any{"section": map[string]any{"count": 4242}})
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "a validate tag of config.behindDivePointerCfg does not apply to its field at section.count: dive error! can't dive on a non slice or map")
	assert.NotContains(t, err.Error(), "4242")

	_, err = Decode[behindKeysSliceCfg](t.Context(), map[string]any{"items": []any{map[string]any{"list": []any{"secret-ish"}}}})
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "does not apply to its field at items[0].list: runtime error: invalid memory address or nil pointer dereference")
	assert.NotContains(t, err.Error(), "secret-ish")
}

type panickyValue struct{ N int }

func (panickyValue) Validate() error { panic("caller method") }

type callerMethodCfg struct {
	Value panickyValue `koanf:"value" validate:"validateFn"`
}

// A key check meets the same rule: its error is the declaration's, as it
// is, and no key's or batch's — whether the pre-check refuses the tag before
// the candidate decodes or validation meets it behind a pointer the edit
// sets.
func TestDefaultValidator_KeyCheckRuleThatCannotApplyIsSchemaError(t *testing.T) {
	upFront := "dive on int, which is not a slice, an array or a map"
	atValidation := "does not apply to its field at section.count: dive error! can't dive on a non slice or map"
	tests := []struct {
		name  string
		check func() error
		want  string
	}{
		{"DecodeKey up front", func() error { _, err := DecodeKey[applyDiveOnIntCfg](t.Context(), "count", 1); return err }, upFront},
		{"CheckKey up front", func() error { return CheckKey[applyDiveOnIntCfg](t.Context(), "count", 1) }, upFront},
		{"DecodeEdits up front", func() error {
			_, err := DecodeEdits[applyDiveOnIntCfg](t.Context(), nil, map[string]any{"count": 1})
			return err
		}, upFront},
		{"DecodeKey at validation", func() error {
			_, err := DecodeKey[behindDivePointerCfg](t.Context(), "section.count", 1)
			return err
		}, atValidation},
		{"CheckKey at validation", func() error { return CheckKey[behindDivePointerCfg](t.Context(), "section.count", 1) }, atValidation},
		{"DecodeEdits at validation", func() error {
			_, err := DecodeEdits[behindDivePointerCfg](t.Context(), nil, map[string]any{"section.count": 1})
			return err
		}, atValidation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() { err = tt.check() })
			require.ErrorIs(t, err, ErrSchema)
			assert.Contains(t, err.Error(), tt.want)
			assert.NotErrorIs(t, err, ErrValidation)
			var ke *KeyError
			assert.NotErrorAs(t, err, &ke)
			var ee *EditsError
			assert.NotErrorAs(t, err, &ee)
		})
	}
}

// A panic raised by the caller's own code that the default validator calls
// — a validateFn rule's method — is the caller's and still panics.
func TestDefaultValidator_CallerMethodPanicsAreTheirs(t *testing.T) {
	assert.PanicsWithValue(t, "caller method", func() {
		_, _ = Decode[callerMethodCfg](t.Context(), map[string]any{"value": map[string]any{"n": 1}})
	})
}
