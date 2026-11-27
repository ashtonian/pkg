//go:build unit

package patch

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRawPatchOp_MembersAreTheOperationObjects: RawPatchOp's JSON form names
// an operation object's members as patch reads and writes them in maps.
func TestRawPatchOp_MembersAreTheOperationObjects(t *testing.T) {
	text, err := json.Marshal(RawPatchOp{Op: "copy", Path: "/a", From: "/b", Value: 1})
	require.NoError(t, err)
	var members map[string]any
	require.NoError(t, json.Unmarshal(text, &members))
	assert.Equal(t, map[string]any{memberOp: "copy", memberPath: "/a", memberFrom: "/b", memberValue: float64(1)}, members)
}
