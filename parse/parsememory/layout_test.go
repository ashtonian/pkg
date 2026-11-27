//go:build unit && !wasip1

package parsememory_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The provider runs no SQL: it imports none of parse's SQL compilers or
// executor, links the executor not even through parse, and links no
// database driver. (wasip1 runs no go command to list it.)
func TestLayout_LinksNoSQLExecutorOrDriver(t *testing.T) {
	const parse = "github.com/ashtonian/pkg/parse"
	out := goList(t, parse+"/parsememory")
	var listed struct{ Imports, Deps []string }
	require.NoError(t, json.Unmarshal(out, &listed))
	require.Contains(t, listed.Imports, parse, "go list read the package")

	for _, sql := range []string{parse + "/internal/sqlplan", parse + "/internal/sqlexec", parse + "/internal/dialect"} {
		assert.NotContains(t, listed.Imports, sql, "parsememory imports %s", sql)
	}
	assert.NotContains(t, listed.Deps, parse+"/internal/sqlexec", "parsememory links the SQL executor")
	for _, dep := range listed.Deps {
		for _, driver := range []string{"modernc.org/sqlite", "github.com/jackc/", "github.com/lib/pq", "github.com/mattn/go-sqlite3"} {
			assert.False(t, strings.HasPrefix(dep, driver), "parsememory links the driver package %s", dep)
		}
	}
}

// goList is go list -json of pkg, from the package's directory. Outside a
// workspace the module is the main module, which the release check reads
// from the read-only module cache, so the go command reads a copy of its
// go.mod and writes the go.sum it resolves beside that copy (-modfile).
func goList(t *testing.T, pkg string) []byte {
	t.Helper()
	ctx := t.Context()
	args := []string{"list", "-json"}
	work, err := exec.CommandContext(ctx, "go", "env", "GOWORK").Output()
	require.NoError(t, err)
	workspace := strings.TrimSpace(string(work))
	if workspace == "" || workspace == "off" {
		dir := t.TempDir()
		for _, name := range []string{"go.mod", "go.sum"} {
			b, err := os.ReadFile(name)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), b, 0o600))
		}
		args = append(args, "-modfile="+filepath.Join(dir, "go.mod"))
	}
	out, err := exec.CommandContext(ctx, "go", append(args, pkg)...).Output()
	exitErr, exited := errors.AsType[*exec.ExitError](err)
	if exited {
		require.NoError(t, err, "go list: %s", exitErr.Stderr)
	}
	require.NoError(t, err)
	return out
}
