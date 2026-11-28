//go:build unit

package filter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchLike(t *testing.T) {
	tests := []struct {
		s, pattern string
		fold, want bool
	}{
		{"abc", "abc", false, true},
		{"abc", "a%", false, true},
		{"abc", "%c", false, true},
		{"abc", "%", false, true},
		{"", "%", false, true},
		{"", "_", false, false},
		{"abc", "a_c", false, true},
		{"aXbYb", "a%b", false, true}, // % backtracks past the first b
		{"aXbYc", "a%b", false, false},
		{"Ωmega", "_mega", false, true}, // _ is one character, not one byte
		{"ABC", "abc", false, false},
		{"ABC", "abc", true, true},
		{"Ωmega", "ωmega", true, true},
		{"50%", `50\%`, false, true},
		{"50x", `50\%`, false, false},
		{`a\b`, `a\\b`, false, true},
		{"a_b", `a\_b`, false, true},
		{"axb", `a\_b`, false, false},
		{"abc", `abc\`, false, false}, // malformed: matches nothing
		{"aΩΩb", "a%Ωb", false, true}, // % backtracks over multi-byte characters
		{"aΩΩc", "a%Ωb", false, false},
		{"日本語", "日_語", false, true},
		{"Ω", `\Ω`, false, true}, // an escaped multi-byte character
		{"50%off", `50\%%`, false, true},
		{"50off", `50\%%`, false, false},
		{"abab", "%ab", false, true},
		{"abc", "%%%", false, true},
		{"ab", "a%%b%", false, true},
		{"ΩMEGA", "ω%a", true, true},
		{"\xff", "_", false, true}, // an invalid byte is one character
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, MatchLike(tt.s, tt.pattern, tt.fold), "MatchLike(%q, %q, %v)", tt.s, tt.pattern, tt.fold)
	}
}

func TestLikeToGlob(t *testing.T) {
	for pattern, want := range map[string]string{
		"a%b_c":   "a*b?c",
		`50\%`:    "50%",
		`a\_`:     "a_",
		"x*y?[z]": "x[*]y[?][[]z]",
		`back\\%`: `back\*`,
		"Ωmega_%": "Ωmega?*",
	} {
		got, err := likeToGlob(pattern)
		require.NoError(t, err)
		assert.Equal(t, want, got, pattern)
	}
	_, err := likeToGlob(`abc\`)
	assert.ErrorIs(t, err, errLikeTrailingEscape)
}
