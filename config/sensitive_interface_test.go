package config

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type ifaceVault struct {
	Plugin any `koanf:"plugin" validate:"omitempty,dive,min=3"`
}

type ifaceItem struct {
	Token any `koanf:"token" validate:"omitempty,dive,min=3"`
}

type ifaceCfg struct {
	Secret   any                  `koanf:"secret" sensitive:"true" validate:"omitempty,dive,min=3"`
	Password any                  `koanf:"password" validate:"omitempty,dive,min=3"`
	Vault    ifaceVault           `koanf:"vault" sensitive:"true"`
	Items    map[string]ifaceItem `koanf:"items" validate:"dive"`
	Ext      any                  `koanf:"ext" validate:"omitempty,dive,min=3"`
}

// ifaceKey is the key beneath each interface: named like a credential, as a
// key that is itself a secret often is.
const ifaceKey = "prod_password"

type ifaceField struct {
	field    string
	tree     []string // field's path in a source tree
	withheld bool
}

// ifaceFields is each interface field of ifaceCfg, and whether its keys are
// withheld. A sensitive interface can hold a map or a list whose keys are
// secrets, as a sensitive map can.
func ifaceFields() []ifaceField {
	fields := []ifaceField{
		{"secret", []string{"secret"}, true},                        // tagged sensitive
		{"password", []string{"password"}, true},                    // named like a credential
		{"vault.plugin", []string{"vault", "plugin"}, true},         // in a sensitive struct
		{`items["a"].token`, []string{"items", "a", "token"}, true}, // named like a credential, in a map entry
		{"ext", []string{"ext"}, false},
	}
	return fields
}

// shown is how diagnostics name key, a key beneath field: field alone when
// its keys are withheld.
func shown(field string, withheld bool, key string) string {
	if withheld {
		return field
	}
	return key
}

// nested is a source tree holding v at path.
func nested(path []string, v any) map[string]any {
	for i := len(path) - 1; i > 0; i-- {
		v = map[string]any{path[i]: v}
	}
	tree := map[string]any{path[0]: v}
	return tree
}

func TestDecodeEdits_WithholdsKeysBeneathASensitiveInterface(t *testing.T) {
	for _, tt := range ifaceFields() {
		t.Run(tt.field, func(t *testing.T) {
			key := tt.field + "." + ifaceKey

			_, err := DecodeEdits[ifaceCfg](t.Context(), nil, map[string]any{key: "x", key + ".y": 1})
			var ee *EditsError
			require.ErrorAs(t, err, &ee)
			require.Len(t, ee.Refused, 1)
			assert.Equal(t, shown(tt.field, tt.withheld, key+".y"), ee.Refused[0].Key)
			if tt.withheld {
				assert.Contains(t, err.Error(), "key "+tt.field+": "+withheldReason)
				assert.NotContains(t, err.Error(), ifaceKey)
			} else {
				assert.Contains(t, err.Error(), key+" is set to a value with no keys beneath it")
			}

			_, err = DecodeEdits[ifaceCfg](t.Context(), nil, map[string]any{key + "[*]": 1})
			require.ErrorAs(t, err, &ee)
			require.Len(t, ee.Refused, 1)
			assert.True(t, ee.Refused[0].Unknown)
			assert.Equal(t, shown(tt.field, tt.withheld, key+"[*]"), ee.Refused[0].Key)
			if tt.withheld {
				assert.NotContains(t, err.Error(), ifaceKey)
			}
		})
	}
}

func TestCheckKey_WithholdsKeysBeneathASensitiveInterface(t *testing.T) {
	for _, tt := range ifaceFields() {
		t.Run(tt.field, func(t *testing.T) {
			key := tt.field + "." + ifaceKey

			err := CheckKey[ifaceCfg](t.Context(), key+"[*]", 1)
			var ke *KeyError
			require.ErrorAs(t, err, &ke)
			require.ErrorIs(t, err, ErrKey)
			assert.Equal(t, shown(tt.field, tt.withheld, key+"[*]"), ke.Key)

			_, err = CanonicalKey[ifaceCfg](key + "[*]")
			require.ErrorAs(t, err, &ke)
			assert.Equal(t, shown(tt.field, tt.withheld, key+"[*]"), ke.Key)

			err = CheckKey[ifaceCfg](t.Context(), key, "ab")
			require.ErrorAs(t, err, &ke)
			require.ErrorIs(t, err, ErrValidation)
			assert.Equal(t, shown(tt.field, tt.withheld, key), ke.Key)
		})
	}
}

func TestValidation_WithholdsKeysBeneathASensitiveInterface(t *testing.T) {
	for _, tt := range ifaceFields() {
		t.Run(tt.field, func(t *testing.T) {
			key := tt.field + "." + ifaceKey
			_, err := Decode[ifaceCfg](t.Context(), nested(append(tt.tree, ifaceKey), "ab"))
			require.ErrorIs(t, err, ErrValidation)
			if tt.withheld {
				assert.Contains(t, err.Error(), tt.field+"(min)")
				assert.NotContains(t, err.Error(), ifaceKey)
				return
			}
			assert.Contains(t, err.Error(), key+"(min)")
		})
	}
}

// A stored entry holding [*] beneath a sensitive interface is refused, named
// by the interface's field.
func TestLoad_NamesPatternEntriesBeneathASensitiveInterfaceByItsField(t *testing.T) {
	entries := map[string]any{}
	var want []string
	for _, tt := range ifaceFields() {
		key := tt.field + "." + ifaceKey + "[*]"
		entries[key] = 1
		want = append(want, shown(tt.field, tt.withheld, key))
	}
	var rejected []KeyError
	_, err := Load[ifaceCfg](t.Context(),
		WithBaseEntries(func(context.Context) (map[string]any, error) { return entries, nil }, WithInvalidEntries(func(_ context.Context, ke []KeyError) error {
			rejected = ke
			return nil
		})))
	require.NoError(t, err)
	var got []string
	for i := range rejected {
		got = append(got, rejected[i].Key)
		require.ErrorIs(t, &rejected[i], ErrKey)
		assert.Contains(t, rejected[i].Error(), "not concrete")
		if !strings.Contains(rejected[i].Key, "[*]") {
			assert.NotContains(t, rejected[i].Error(), ifaceKey, "withheld")
		}
	}
	assert.ElementsMatch(t, want, got)
}
