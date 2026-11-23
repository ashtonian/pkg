package config

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func remainOutputCfg() remainDiffCfg {
	return remainDiffCfg{
		Plugins: remainPlugins{Host: "h", Extra: map[string]any{"foo": 1, "a.b": 2, "api_token": "t0ken", "endpoint": "postgres://u:hunter2pw@db/x"}},
		Vault:   &remainSecrets{Addr: "a", Paths: map[string]string{"prod_db": "s3cret"}},
		Typed:   remainTyped{Items: map[string]remainItem{"x": {Port: 2}}},
		Locked:  remainLabels{Team: "t", Values: map[string]string{"k": "v4lue"}},
	}
}

// Tree withholds nothing: each key a remain field collects is a key of its
// section, sensitive or not, so the tree loads back.
func TestTree_ShowsTheKeysARemainFieldCollects(t *testing.T) {
	cfg := remainOutputCfg()
	tree, err := Tree(cfg)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"host": "h", "foo": int64(1), "a.b": int64(2), "api_token": "t0ken", "endpoint": "postgres://u:hunter2pw@db/x"}, tree["plugins"])
	assert.Equal(t, map[string]any{"addr": "a", "prod_db": "s3cret"}, tree["vault"])
	assert.Equal(t, map[string]any{"name": "", "x": map[string]any{"port": int64(2)}}, tree["typed"])
	assert.Equal(t, map[string]any{"team": "t", "k": "v4lue"}, tree["locked"])

	back, err := Decode[remainDiffCfg](t.Context(), tree)
	require.NoError(t, err)
	assert.Equal(t, cfg.Vault, back.Vault)
	assert.Equal(t, cfg.Typed, back.Typed)
	assert.Equal(t, cfg.Locked, back.Locked)
}

// TreeFunc hands a policy each key a remain field collects with the remain
// field as its Field, and a sensitive remain field once, whole, at
// section[*], as a sensitive map.
func TestTreeFunc_GivesAPolicyTheKeysARemainFieldCollects(t *testing.T) {
	seen := map[string]Field{}
	tree, err := TreeFunc(remainOutputCfg(), func(key string, f Field, v any) any {
		seen[key] = f
		if f.Sensitive {
			return redactedValue
		}
		return v
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"host": "h", "foo": int64(1), "a.b": int64(2), "api_token": redactedValue, "endpoint": "postgres://u:hunter2pw@db/x"}, tree["plugins"])
	assert.Equal(t, map[string]any{"addr": "a", "[*]": redactedValue}, tree["vault"])
	assert.Equal(t, map[string]any{"team": redactedValue, "[*]": redactedValue}, tree["locked"])
	assert.Equal(t, "plugins[*]", seen["plugins.foo"].Path)
	assert.Equal(t, "vault[*]", seen["vault[*]"].Path)
	assert.True(t, seen["vault[*]"].Sensitive)
	assert.NotContains(t, seen, "vault.prod_db")
	assert.Equal(t, "plugins[*]", seen[`plugins["a.b"]`].Path)
}

// A policy that returns a map for a sensitive remain field sets the keys of
// its section, so one that returns what it is given gives Tree's tree. It
// cannot set a key a field reads.
func TestTreeFunc_APolicysMapSetsTheKeysOfARemainFieldsSection(t *testing.T) {
	cfg := remainOutputCfg()
	want, err := Tree(cfg)
	require.NoError(t, err)
	got, err := TreeFunc(cfg, func(_ string, _ Field, v any) any { return v })
	require.NoError(t, err)
	assert.Equal(t, want, got)

	_, err = TreeFunc(cfg, func(key string, _ Field, v any) any {
		if key == "vault[*]" {
			return map[string]any{"addr": "elsewhere"}
		}
		return v
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vault.addr")
}

// Redacted output shows each key a non-sensitive remain field collects, by
// the credential-name and text rules every map value follows, and a
// sensitive remain field as one redacted value at section[*].
func TestRedactedMap_ShowsTheKeysARemainFieldCollects(t *testing.T) {
	got, err := RedactedMapChecked(remainOutputCfg())
	require.NoError(t, err)
	assert.Equal(t, "1", got["plugins.foo"])
	assert.Equal(t, "2", got[`plugins["a.b"]`])
	assert.Equal(t, redactedValue, got["plugins.api_token"], "a key named like a credential")
	assert.NotContains(t, got["plugins.endpoint"], "hunter2pw", "a credential in the text")
	assert.Contains(t, got["plugins.endpoint"], "postgres://")
	assert.Equal(t, "2", got["typed.x.port"])
	assert.Equal(t, redactedValue, got["vault[*]"])
	assert.Equal(t, redactedValue, got["locked[*]"])
	assert.Equal(t, redactedValue, got["locked.team"])
	for key, value := range got {
		assert.NotContains(t, key, "prod_db")
		assert.NotContains(t, value, "s3cret")
		assert.NotContains(t, value, "v4lue")
		assert.NotContains(t, value, "t0ken")
	}

	tree, err := RedactedTreeChecked(remainOutputCfg())
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"addr": "a", "[*]": redactedValue}, tree["vault"])
	assert.Equal(t, int64(1), tree["plugins"].(map[string]any)["foo"])
	assert.Equal(t, int64(2), tree["plugins"].(map[string]any)["a.b"])

	for _, format := range []string{FormatTable, FormatJSON, FormatYAML} {
		var out bytes.Buffer
		require.NoError(t, PrintRedacted(remainOutputCfg(), WithWriter(&out), WithFormat(format)), format)
		text := out.String()
		assert.Contains(t, text, "foo", format)
		assert.Contains(t, text, "[*]", format)
		for _, secret := range []string{"prod_db", "s3cret", "v4lue", "t0ken", "hunter2pw"} {
			assert.False(t, strings.Contains(text, secret), "%s shows %s", format, secret)
		}
	}
}
