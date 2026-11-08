//go:build integration && (linux || darwin)

package state

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDocumentReadRejectsSpecialFiles(t *testing.T) {
	if path := os.Getenv("STATE_SPECIAL_FILE_HELPER"); path != "" {
		var value any
		if err := ReadJSON(t.Context(), path, 1024, &value); err == nil {
			t.Fatal("special file accepted as a document")
		}
		return
	}
	path := filepath.Join(t.TempDir(), "input.pipe")
	require.NoError(t, syscall.Mkfifo(path, 0o600))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeNamedPipe)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDocumentReadRejectsSpecialFiles$")
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "STATE_SPECIAL_FILE_HELPER="+path)
	output, err := cmd.CombinedOutput()
	require.NoError(t, ctx.Err(), "document open blocked before rejecting its source")
	require.NoError(t, err, "%s", output)
}

func TestManagedFileReadKeepsBoundedRegularSource(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "secret")
	link := filepath.Join(root, "current")
	require.NoError(t, os.WriteFile(file, []byte("managed-value\n"), 0o600))
	require.NoError(t, os.Symlink(file, link))
	data, err := ReadFile(t.Context(), link, 64)
	require.NoError(t, err)
	require.Equal(t, "managed-value\n", string(data))
	_, err = ReadFile(t.Context(), link, 3)
	require.Error(t, err)
	_, err = ReadFile(t.Context(), root, 64)
	require.Error(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = ReadFile(ctx, link, 64)
	require.ErrorIs(t, err, context.Canceled)
}
