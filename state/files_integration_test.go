//go:build integration

package state

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDurableStateReplacement(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", "cache.json")
	var target map[string]string
	err := ReadJSON(t.Context(), file, 1024, &target)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, WriteJSON(t.Context(), file, map[string]string{"state": "first"}))
	require.NoError(t, WriteJSON(t.Context(), file, map[string]string{"state": "second"}))
	require.NoError(t, ReadJSON(t.Context(), file, 1024, &target))
	require.Equal(t, map[string]string{"state": "second"}, target)
	info, err := os.Stat(file)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.Error(t, ReadJSON(t.Context(), file, 1, &target))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, WriteJSON(ctx, file, target), context.Canceled)
	require.NoError(t, ReadJSON(t.Context(), file, 1024, &target))
	require.Equal(t, "second", target["state"])
	files, err := os.ReadDir(filepath.Dir(file))
	require.NoError(t, err)
	require.Len(t, files, 1)
}
func TestInvalidStateIsNotAnEmptySuccess(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(file, []byte("{broken"), 0o600))
	var target map[string]string
	err := ReadJSON(t.Context(), file, 1024, &target)
	var syntax *json.SyntaxError
	require.True(t, errors.As(err, &syntax))
}

func TestStrictStateRejectsUnknownSchemaFields(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plan.json")
	require.NoError(t, os.WriteFile(file, []byte(`{"version":1,"unknown":true}`), 0o600))
	var target struct {
		Version int `json:"version"`
	}
	require.ErrorContains(t, ReadJSONStrict(t.Context(), file, 1024, &target), "unknown field")
	require.NoError(t, ReadJSON(t.Context(), file, 1024, &target))
	require.NoError(t, os.WriteFile(file, []byte(`{"version":1} {"version":2}`), 0o600))
	require.Error(t, ReadJSONStrict(t.Context(), file, 1024, &target))
}

func TestBoundedStateWritePreservesReadableReceipt(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", "receipt.json")
	require.NoError(t, WriteJSONBounded(t.Context(), file, 128, map[string]string{"state": "known"}))
	require.Error(t, WriteJSONBounded(t.Context(), file, 8, map[string]string{"state": "replacement"}))
	var saved map[string]string
	require.NoError(t, ReadJSON(t.Context(), file, 128, &saved))
	require.Equal(t, "known", saved["state"])
	absent := filepath.Join(t.TempDir(), "not-created", "receipt.json")
	require.Error(t, WriteJSONBounded(t.Context(), absent, 8, saved))
	_, err := os.Stat(filepath.Dir(absent))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestNewRunStateNeverReplacesAnExistingReceiptOrSymlink(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "receipt.json")
	first := map[string]string{"run": "first"}
	require.NoError(t, CreateJSONBounded(t.Context(), file, 1024, first))
	require.ErrorIs(t, CreateJSONBounded(t.Context(), file, 1024, map[string]string{"run": "second"}), os.ErrExist)
	var value map[string]string
	require.NoError(t, ReadJSON(t.Context(), file, 1024, &value))
	require.Equal(t, first, value)
	link := filepath.Join(directory, "link.json")
	require.NoError(t, os.Symlink(file, link))
	require.ErrorIs(t, CreateJSONBounded(t.Context(), link, 1024, first), os.ErrExist)
	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestRootedStateRejectsEscapesSymlinksAndReplacementOfNewRuns(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	value := map[string]string{"owner": "first"}
	require.NoError(t, CreateJSONAt(t.Context(), root, "claim.json", 1024, value))
	require.ErrorIs(t, CreateJSONAt(t.Context(), root, "claim.json", 1024, value), os.ErrExist)
	var read map[string]string
	require.NoError(t, ReadJSONStrictAt(t.Context(), root, "claim.json", 1024, &read))
	require.Equal(t, value, read)
	require.NoError(t, root.Symlink("claim.json", "link.json"))
	require.Error(t, ReadJSONStrictAt(t.Context(), root, "link.json", 1024, &read))
	for _, name := range []string{"../outside.json", "/outside.json", "a/b.json", ".", "..", ""} {
		require.Error(t, CreateJSONAt(t.Context(), root, name, 1024, value))
		require.Error(t, WriteJSONAt(t.Context(), root, name, 1024, value))
	}
	require.NoError(t, WriteJSONAt(t.Context(), root, "claim.json", 1024, map[string]string{"owner": "second"}))
	require.NoError(t, ReadJSONStrictAt(t.Context(), root, "claim.json", 1024, &read))
	require.Equal(t, "second", read["owner"])
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Len(t, entries, 2)
}
