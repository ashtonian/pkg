//go:build unit

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A variable is ambiguous whenever two destinations share its name, wherever
// they come from: two static fields, a static field and a map entry, or two
// maps whose names prefix one another.
func TestLoad_EnvAmbiguityAcrossDestinations(t *testing.T) {
	type entry struct {
		Limit int `koanf:"limit"`
	}
	type db struct {
		Port int `koanf:"port"`
	}
	type cfg struct {
		DB        db               `koanf:"db"`
		DBPort    int              `koanf:"db_port"`
		Limits    map[string]entry `koanf:"limits"`
		LimitsPer map[string]entry `koanf:"limits_per"`
		Features  map[string]entry `koanf:"features"`
		Flags     map[string]bool  `koanf:"features_flags"`
	}
	for _, tt := range []struct {
		name, env string
		readings  []string
	}{
		{"static fields", "AMB_DB_PORT", []string{"db.port", "db_port"}},
		{"two maps", "AMB_LIMITS_PER_TENANT_LIMIT", []string{`limits["per_tenant"].limit`, `limits_per["tenant"].limit`}},
		{"map of structs and map of scalars", "AMB_FEATURES_FLAGS_X_LIMIT", []string{`features["flags_x"].limit`, `features_flags["x_limit"]`}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.env, "7")
			_, err := Load[cfg](t.Context(), WithEnvPrefix("AMB"), WithSkipValidation())
			require.ErrorIs(t, err, ErrValidation)
			assert.Contains(t, err.Error(), tt.env)
			for _, r := range tt.readings {
				assert.Contains(t, err.Error(), r)
			}
		})
	}
}

// Unambiguous variables still reach every kind of destination, and a
// variable that fits no key is not loaded.
func TestLoad_EnvDestinations(t *testing.T) {
	type entry struct {
		MaxConns int `koanf:"max_conns"`
	}
	type cfg struct {
		MaxConns int               `koanf:"max_conns"`
		Pools    map[string]entry  `koanf:"pools"`
		Labels   map[string]string `koanf:"labels"`
		Extra    map[string]any    `koanf:"extra"`
	}
	t.Setenv("DEST_MAX_CONNS", "5")
	t.Setenv("DEST_POOLS_READ_REPLICA_MAX_CONNS", "8")
	t.Setenv("DEST_LABELS_TEAM_NAME", "core")
	t.Setenv("DEST_EXTRA_A_B", "c")
	t.Setenv("DEST_UNKNOWN_SETTING", "ignored")
	got, err := Load[cfg](t.Context(), WithEnvPrefix("DEST"), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, 5, got.MaxConns)
	assert.Equal(t, map[string]entry{"read_replica": {MaxConns: 8}}, got.Pools)
	assert.Equal(t, map[string]string{"team_name": "core"}, got.Labels, "a map key keeps its underscores")
	assert.Equal(t, map[string]any{"a_b": "c"}, got.Extra)
}

func TestEnvSchema_ReadReportsUnmatched(t *testing.T) {
	type cfg struct {
		Host string `koanf:"host"`
	}
	s := newEnvSchema(Fields[cfg](), "APP")
	values, unmatched, err := s.read([]string{"APP_HOST=h", "APP_HOTS=typo", "OTHER_HOST=x", "APP_NOEQUALS"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"host": "h"}, values)
	assert.Equal(t, []string{"APP_HOTS"}, unmatched)
}
