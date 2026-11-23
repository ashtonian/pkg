package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKey_CanonicalGrammar(t *testing.T) {
	for _, tt := range []struct{ key, fields, last string }{
		{"items[12].port", "items.port", "port"},
		{`items["a.b[0]\\\""].ports[*]`, "items.ports", "ports"},
		{`map[""]`, "map", ""},
		{"rows[*][0]", "rows", "rows"},
	} {
		t.Run(tt.key, func(t *testing.T) {
			key, err := ParseKey(tt.key)
			require.NoError(t, err)
			assert.Equal(t, tt.key, key.String())
			assert.Equal(t, tt.fields, key.Fields().String())
			assert.Equal(t, tt.last, key.LastName())
		})
	}
	for _, key := range []string{"", ".x", "a..b", "a[-1]", "a[no]", "a[1", "a[*]x", "a.\"b\""} {
		_, err := ParseKey(key)
		assert.ErrorIs(t, err, ErrKey, key)
	}
}

func TestKey_DiffPaths(t *testing.T) {
	type cfg struct {
		Values map[string][]int `koanf:"values"`
	}
	changes, err := Diff(&cfg{Values: map[string][]int{`a.b["c"]`: {1}}}, &cfg{Values: map[string][]int{`a.b["c"]`: {2}}})
	require.NoError(t, err)
	require.NotEmpty(t, changes)
	for _, c := range changes {
		key, err := ParseKey(c.Key)
		require.NoError(t, err)
		assert.Equal(t, c.Key, key.String())
		assert.Equal(t, "values", key.Fields().String())
	}
}

func TestKey_Within(t *testing.T) {
	key := func(s string) Key {
		k, err := ParseKey(s)
		require.NoError(t, err)
		return k
	}
	for _, tc := range []struct {
		k, p   string
		within bool
	}{
		{"primary.port", "primary", true},
		{"primary", "primary", true},
		{"primary", "primary.port", false},
		{"primaryx", "primary", false},
		{`addresses["a"].port`, `addresses["a"]`, true},
		{`addresses["a.b"]`, `addresses["a"]`, false},
		{"pool[1].host", "pool[1]", true},
		{"pool[10].host", "pool[1]", false},
		{"pool[1]", "pool", true},
		{"extra.x", `extra["x"]`, true},
		{"pool[1].host", "pool[*]", true},
		{`addresses["a"]`, "addresses[*]", true},
		{"primary.port", "primary[*]", true},
		{"pool[*].host", "pool[1]", false},
	} {
		assert.Equal(t, tc.within, key(tc.k).Within(key(tc.p)), "%s within %s", tc.k, tc.p)
	}
}

// A wildcard stands for an entry of a map beneath an interface field as
// CanonicalKey spells it, a field.
func TestKey_WithinMatchesCanonicalKeysBeneathAWildcard(t *testing.T) {
	for _, tc := range []struct{ key, pattern string }{
		{`extra["x"]`, "extra[*]"},
		{`extra["x"].y`, "extra[*].y"},
		{`addresses["a"].port`, "addresses[*].port"},
		{"pool[2].host", "pool[*].host"},
	} {
		canonical, err := CanonicalKey[editConfig](tc.key)
		require.NoError(t, err)
		k, err := ParseKey(canonical)
		require.NoError(t, err)
		p, err := ParseKey(tc.pattern)
		require.NoError(t, err)
		assert.True(t, k.Within(p), "%s (%s) within %s", tc.key, canonical, tc.pattern)
	}
}
