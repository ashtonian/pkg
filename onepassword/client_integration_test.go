//go:build integration && (linux || darwin)

package onepassword

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ashtonian/pkg/process"
	"github.com/stretchr/testify/require"
)

func TestNativeProcessObservesSelectedIDsAndPreservesBinaryReferences(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "op")
	script := `#!/bin/sh
case "$1/$2" in
item/list) printf '%s' '[{"id":"a","title":"example","version":2,"vault":{"id":"v"}}]' ;;
item/get) /bin/cat > "$FIXTURE_ROOT/selected.json"
printf '%s' '{"id":"a","title":"example","version":2,"vault":{"id":"v"},"fields":[{"label":"password","value":"credential-canary"}]}' ;;
read/--no-newline) [ "$3" = op://v/a/binary ] || { printf credential-canary >&2; exit 5; }
printf '\000\377\n' ;;
*) exit 64 ;;
esac
`
	require.NoError(t, os.WriteFile(executable, []byte(script), 0o700))
	supervisor, err := process.New(process.WithTimeout(5*time.Second), process.WithGrace(time.Second), process.WithMaxOutputBytes(1<<20))
	require.NoError(t, err)
	client, err := New(WithRunner(supervisor), WithExecutable(executable), WithEnvironment([]string{"FIXTURE_ROOT=" + root}))
	require.NoError(t, err)
	metadata, err := client.List(t.Context(), "example-vault")
	require.NoError(t, err)
	items, err := client.Get(t.Context(), "example-vault", metadata)
	require.NoError(t, err)
	require.Equal(t, "credential-canary", items["a"].Fields[0].Value)
	selected, err := os.ReadFile(filepath.Join(root, "selected.json"))
	require.NoError(t, err)
	var ids []map[string]string
	require.NoError(t, json.Unmarshal(selected, &ids))
	require.Equal(t, []map[string]string{{"id": "a"}}, ids)
	value, err := client.Read(t.Context(), "op://v/a/binary")
	require.NoError(t, err)
	require.Equal(t, []byte{0, 0xff, '\n'}, value)
	value, err = client.Read(t.Context(), "op://v/a/denied")
	require.Error(t, err)
	require.Nil(t, value)
	require.NotContains(t, err.Error(), "credential-canary")
}
