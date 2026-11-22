//go:build unit

package patch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func matchText(t *testing.T, pattern, path string) bool {
	t.Helper()
	p, err := parsePattern(pattern)
	require.NoError(t, err)
	return p.matches(pointerTokens(path))
}

// TestSlashStarMatchesOneSegmentOnly: "/*" means exactly one token under
// root, not everything.
func TestSlashStarMatchesOneSegmentOnly(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/*", "/a", true},
		{"/*", "/name", true},
		{"/*", "/a/b", false},
		{"/*", "/a/b/c", false},
		{"**", "/a", true},
		{"**", "/a/b/c", true},
		{"/config/*", "/config/x", true},
		{"/config/*", "/config/x/y", false},
		{"/config/**", "/config/x/y", true},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, matchText(t, tc.pattern, tc.path), "%q against %q", tc.pattern, tc.path)
	}
}

// * is exactly one reference token, and an empty key is a token.
func TestPattern_WildcardTokens(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{
		{"/a/*", "/a//b", false},
		{"/a/*", "/a/", true},
		{"/a/*/c", "/a//c", true},
		{"/a/**", "/a//b", true},
		{"/*", "/", true},
		{"/*", "//", false},
		{"/a~1b/*", "/a~1b/c", true},
		{"/a/*", "/a~1b", false},
	} {
		assert.Equal(t, tc.want, matchText(t, tc.pattern, tc.path), "%q against %q", tc.pattern, tc.path)
	}
}

// ** is zero or more tokens wherever it stands.
func TestPattern_DoubleStarTakesZeroTokens(t *testing.T) {
	for _, tc := range []struct{ pattern, path string }{
		{"/**/b", "/b"},        // leading
		{"/a/**/b", "/a/b"},    // middle
		{"/a/**", "/a"},        // trailing
		{"/**", ""},            // alone
		{"/a/**/**/b", "/a/b"}, // repeated
	} {
		assert.True(t, matchText(t, tc.pattern, tc.path), "%q against %q", tc.pattern, tc.path)
	}
	assert.False(t, matchText(t, "/a/**/b", "/a/c"))
	assert.True(t, matchText(t, "/a/**/b", "/a/x/y/b"))
}

// A path is below a pattern's match when the pattern still has tokens to
// match once the path's are consumed.
func TestPattern_MatchesBelow(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{
		{"/attrs/secret", "/attrs", true},
		{"/attrs/secret", "/attrs/secret", false},
		{"/attrs/secret/**", "/attrs/secret", true},
		{"/*/secret", "/anything", true},
		{"/**/secret", "/a/b", true},
		{"/attrs/secret", "/other", false},
		{"/attrs", "/attrs/x", false},
	} {
		p, err := parsePattern(tc.pattern)
		require.NoError(t, err)
		assert.Equal(t, tc.want, p.matchesBelow(pointerTokens(tc.path)), "%q below %q", tc.pattern, tc.path)
	}
}
