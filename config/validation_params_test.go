package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type paramsLimits struct {
	Low  int `koanf:"low"`
	High int `koanf:"high" validate:"gtefield=Low"`
}

type paramsShared struct {
	Mode string `koanf:"mode"`
}

type paramsCfg struct {
	paramsShared `koanf:",squash"`

	MinWait time.Duration `koanf:"min_wait"`
	MaxWait time.Duration `koanf:"max_wait" validate:"gtefield=MinWait"`

	Store struct {
		Type   string `koanf:"type"`
		Bucket string `koanf:"bucket" validate:"required_if=Type s3"`
		Region string `koanf:"region" validate:"required_unless=Type local"`
		Owner  string `koanf:"owner" validate:"required_with=Bucket Region"`
		Notes  string `koanf:"notes" validate:"excluded_with_all=Bucket Region"`
	} `koanf:"store"`

	Limits paramsLimits   `koanf:"limits"`
	Pools  []paramsLimits `koanf:"pools" validate:"dive"`
	Cap    int            `koanf:"cap" validate:"ltecsfield=Limits.High"`
	Label  string         `koanf:"label" validate:"fieldcontains=Mode"`
	Proxy  string         `koanf:"proxy" validate:"required_if=Mode 'via proxy'"`
	Unseen string         `koanf:"unseen" validate:"eqfield=Missing"`

	Secret  string `koanf:"secret" sensitive:"true"`
	Backup  string `koanf:"backup" validate:"required_if=Secret open"`
	Mirror  string `koanf:"mirror" validate:"nefield=Secret"`
	Contact string `koanf:"contact" validate:"required_without_all=Secret Mode"`
}

// A summary names the fields a cross-field rule refers to by key, as it
// names the field that failed. A rule that compares against a sensitive
// field's value shows no param.
func TestValidation_NamesCrossFieldParamsByKey(t *testing.T) {
	values := map[string]any{
		"mode":     "via proxy",
		"min_wait": "2s",
		"max_wait": "1s",
		"store":    map[string]any{"type": "s3", "notes": "n", "bucket": "", "region": "", "owner": ""},
		"limits":   map[string]any{"low": 5, "high": 1},
		"pools":    []map[string]any{{"low": 3, "high": 2}},
		"cap":      9,
		"label":    "plain",
		"unseen":   "x",
		"secret":   "open",
		"mirror":   "open",
		"backup":   "",
		"contact":  "",
		"proxy":    "",
	}
	_, err := Load[paramsCfg](t.Context(), WithDefaults(values))
	require.Error(t, err)
	for _, want := range []string{
		"max_wait(gtefield=min_wait)",
		"store.bucket(required_if=store.type s3)",
		"store.region(required_unless=store.type local)",
		"limits.high(gtefield=limits.low)",
		"pools[0].high(gtefield=pools[0].low)",
		"cap(ltecsfield=limits.high)",
		"label(fieldcontains=mode)",
		"proxy(required_if=mode 'via proxy')",
		"unseen(eqfield=Missing)",
		"backup(required_if)",
		"mirror(nefield=secret)",
	} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "open")
}

// Lists of fields name every field by key.
func TestValidation_NamesFieldListParamsByKey(t *testing.T) {
	values := map[string]any{"store": map[string]any{"bucket": "b", "region": "r", "notes": "n"}}
	_, err := Load[paramsCfg](t.Context(), WithDefaults(values), WithPostLoad(func(_ context.Context, c *paramsCfg) error {
		c.Mode, c.Secret, c.Contact = "", "", ""
		return nil
	}))
	require.Error(t, err)
	for _, want := range []string{
		"store.owner(required_with=store.bucket store.region)",
		"store.notes(excluded_with_all=store.bucket store.region)",
		"contact(required_without_all=secret mode)",
	} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestParentNamespace(t *testing.T) {
	tests := []struct {
		ns, want string
		ok       bool
	}{
		{"Config.DB.Port", "Config.DB", true},
		{"Config.Hosts[1]", "Config", true},
		{"Config.Named[a.b].Port", "Config.Named[a.b]", true},
		{"Config", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.ns, func(t *testing.T) {
			got, ok := parentNamespace(tt.ns)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

type rootSection struct {
	Port int `koanf:"port" validate:"min=1"`
}

// unnamedRoot has no type name, so the validator's namespaces for it start
// at the field: DB.Port, not unnamedRoot.DB.Port.
type unnamedRoot = struct {
	Port    int          `koanf:"port"`
	Min     int          `koanf:"min"`
	Max     int          `koanf:"max" validate:"gtefield=Min"`
	DB      rootSection  `koanf:"db"`
	Section *diveSection `koanf:"section"`
}

// genericRoot's type name holds its type argument's: genericRoot[time.Duration].
type genericRoot[T any] struct {
	Value T           `koanf:"value"`
	Port  int         `koanf:"port"`
	DB    rootSection `koanf:"db"`
}

// A failure is named by its own key under a root type of any name: one with
// none, and a generic one whose name holds a qualified type argument. A
// root field sharing the failed field's name is not the one named.
func TestValidation_NamesFieldsUnderAnyRootTypeName(t *testing.T) {
	_, err := Load[unnamedRoot](t.Context(), WithDefaults(map[string]any{
		"port": 5, "min": 3, "max": 1, "db": map[string]any{"port": 0},
	}))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "configuration validation failed: max(gtefield=min), db.port(min=1)")

	_, err = Load[genericRoot[time.Duration]](t.Context(), WithDefaults(map[string]any{
		"port": 5, "db": map[string]any{"port": 0},
	}))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "configuration validation failed: db.port(min=1)")

	_, err = Load[unnamedRoot](t.Context(), WithDefaults(map[string]any{
		"max": 1, "db": map[string]any{"port": 1}, "section": map[string]any{"count": 1},
	}))
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "does not apply to its field at section.count: dive error!")
}
