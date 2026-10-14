//go:build unit

package config

import (
	"reflect"
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

	got, err := GetConfig[cfg](t.Context(), opt, WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, []string{"original"}, got.Block.Names)
	require.NotNil(t, got.Ptr)
	assert.Equal(t, []string{"original"}, got.Ptr.Names)
	assert.Equal(t, [1][]int{{1}}, got.Arr)
}

// Sharing and cycles inside the defaults survive the copy without it
// recursing forever; unexported fields keep their values.
func TestDeepCopy_SharingCyclesAndUnexported(t *testing.T) {
	type node struct {
		Next *node
		Tags []string
		note string
	}
	shared := []string{"a"}
	n := &node{Tags: shared, note: "kept"}
	n.Next = n
	self := map[string]any{"tags": shared}
	self["self"] = self

	in := map[string]any{"node": n, "self": self, "again": shared}
	out := deepCopy(reflect.ValueOf(in)).Interface().(map[string]any)

	cn := out["node"].(*node)
	assert.NotSame(t, n, cn)
	assert.Same(t, cn, cn.Next, "the cycle points at the copy")
	assert.Equal(t, "kept", cn.note)
	cself := out["self"].(map[string]any)
	assert.Equal(t, reflect.ValueOf(cself).Pointer(), reflect.ValueOf(cself["self"]).Pointer())

	shared[0] = "changed"
	assert.Equal(t, []string{"a"}, cn.Tags)
	assert.Equal(t, []string{"a"}, out["again"])
}
