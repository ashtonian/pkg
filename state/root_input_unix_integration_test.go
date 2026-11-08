//go:build integration && (linux || darwin)

package state

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRootedRawInputRejectsLinksAndSpecialFiles(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, os.WriteFile(filepath.Join(directory, "input"), []byte("safe"), 0o600))
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
	for name, target := range map[string]string{"link": "input", "escape": outside} {
		require.NoError(t, os.Symlink(target, filepath.Join(directory, name)))
		_, err = ReadFileAt(t.Context(), root, name, 64)
		require.Error(t, err)
	}
	pipe := filepath.Join(directory, "pipe")
	require.NoError(t, syscall.Mkfifo(pipe, 0o600))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = ReadFileAt(ctx, root, "pipe", 64)
	require.Error(t, err)
	require.NoError(t, ctx.Err())
	expected, err := root.Stat("input")
	require.NoError(t, err)
	file, err := os.OpenFile(pipe, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	require.NoError(t, err)
	_, err = readAndCloseRegular(ctx, file, expected, 64)
	require.Error(t, err)
	require.NoError(t, ctx.Err())
}
