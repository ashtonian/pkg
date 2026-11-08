//go:build integration && (linux || darwin)

package state

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestControllerOwnershipRetainsItsFileIdentity(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	first, err := LockFile(t.Context(), root, "controller.lock")
	require.NoError(t, err)
	_, err = LockFile(t.Context(), root, "controller.lock")
	require.Error(t, err)
	require.NoError(t, first.Close())
	second, err := LockFile(t.Context(), root, "controller.lock")
	require.NoError(t, err)
	require.NoError(t, second.Close())
	_, err = LockFile(t.Context(), root, "../controller.lock")
	require.Error(t, err)
	require.NoError(t, root.Symlink("controller.lock", "alias.lock"))
	_, err = LockFile(t.Context(), root, "alias.lock")
	require.Error(t, err)
}
