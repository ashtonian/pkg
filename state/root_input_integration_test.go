//go:build integration

package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRootedRawInputRetainsDirectoryAuthority(t *testing.T) {
	parent := t.TempDir()
	directory := filepath.Join(parent, "selected")
	require.NoError(t, os.Mkdir(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "input"), []byte("original"), 0o600))
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	moved := filepath.Join(parent, "moved")
	require.NoError(t, os.Rename(directory, moved))
	require.NoError(t, os.Mkdir(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "input"), []byte("replacement"), 0o600))
	pathData, err := ReadFile(t.Context(), filepath.Join(directory, "input"), 64)
	require.NoError(t, err)
	require.Equal(t, "replacement", string(pathData))
	rootedData, err := ReadFileAt(t.Context(), root, "input", 64)
	require.NoError(t, err)
	require.Equal(t, "original", string(rootedData))
	require.NoError(t, os.WriteFile(filepath.Join(moved, "empty"), nil, 0o600))
	rootedData, err = ReadFileAt(t.Context(), root, "empty", 1)
	require.NoError(t, err)
	require.Empty(t, rootedData)
}
func TestRootedRawInputPreservesBoundsAndJSONContract(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, os.WriteFile(filepath.Join(directory, "input"), []byte(`{"value":1}`), 0o600))
	data, err := ReadFileAt(t.Context(), root, "input", 11)
	require.NoError(t, err)
	require.Equal(t, `{"value":1}`, string(data))
	_, err = ReadFileAt(t.Context(), root, "input", 10)
	require.Error(t, err)
	for _, name := range []string{"", ".", "..", "../input", "sub/input", "/input"} {
		_, err = ReadFileAt(t.Context(), root, name, 64)
		require.Error(t, err)
	}
	_, err = ReadFileAt(t.Context(), nil, "input", 64)
	require.Error(t, err)
	_, err = ReadFileAt(t.Context(), root, "missing", 64)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.Mkdir(filepath.Join(directory, "directory"), 0o700))
	_, err = ReadFileAt(t.Context(), root, "directory", 64)
	require.Error(t, err)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = ReadFileAt(cancelled, root, "input", 64)
	require.ErrorIs(t, err, context.Canceled)
	var value struct {
		Value int `json:"value"`
	}
	require.NoError(t, ReadJSONStrictAt(t.Context(), root, "input", 64, &value))
	require.Equal(t, 1, value.Value)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "input"), []byte(`{"unknown":1}`), 0o600))
	require.Error(t, ReadJSONStrictAt(t.Context(), root, "input", 64, &value))
}
func TestOpenedRawInputRejectsAnotherIdentity(t *testing.T) {
	directory := t.TempDir()
	first, second := filepath.Join(directory, "first"), filepath.Join(directory, "second")
	require.NoError(t, os.WriteFile(first, []byte("first"), 0o600))
	require.NoError(t, os.WriteFile(second, []byte("second"), 0o600))
	expected, err := os.Stat(first)
	require.NoError(t, err)
	file, err := os.Open(second)
	require.NoError(t, err)
	_, err = readAndCloseRegular(t.Context(), file, expected, 64)
	require.Error(t, err)
	_, err = file.Stat()
	require.True(t, errors.Is(err, os.ErrClosed))
}
