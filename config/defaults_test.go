package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WithDefaults owns what it was given: mutable data reached through structs,
// arrays and pointers is copied, not only maps and slices.
func TestWithDefaults_SnapshotsNestedData(t *testing.T) {
	type block struct {
		Names []string `koanf:"names"`
	}
	type cfg struct {
		Block block    `koanf:"block"`
		Ptr   *block   `koanf:"ptr"`
		Arr   [1][]int `koanf:"arr"`
	}
	names := []string{"original"}
	ptrNames := []string{"original"}
	arr := [1][]int{{1}}
	opt := WithDefaults(map[string]any{
		"block": block{Names: names},
		"ptr":   &block{Names: ptrNames},
		"arr":   arr,
	})
	names[0], ptrNames[0], arr[0][0] = "changed", "changed", 2

	got, err := Load[cfg](t.Context(), opt, WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, []string{"original"}, got.Block.Names)
	require.NotNil(t, got.Ptr)
	assert.Equal(t, []string{"original"}, got.Ptr.Names)
	assert.Equal(t, [1][]int{{1}}, got.Arr)
}

// Map keys are copied too: a pointer key's target changed by the caller
// afterwards does not reach a load.
func TestWithDefaults_CopiesMapKeys(t *testing.T) {
	type key struct{ Label string }
	type cfg struct {
		Values map[*key]string `koanf:"values"`
	}
	k := &key{Label: "original"}
	opt := WithDefaults(map[string]any{"values": map[*key]string{k: "value"}})
	k.Label = "changed"
	got, err := Load[cfg](t.Context(), opt, WithSkipValidation())
	require.NoError(t, err)
	require.Len(t, got.Values, 1)
	for copied := range got.Values {
		assert.Equal(t, "original", copied.Label)
		assert.NotSame(t, k, copied)
	}
}
