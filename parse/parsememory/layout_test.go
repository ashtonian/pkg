//go:build unit && !wasip1

package parsememory_test

import (
	"encoding/json"
	"os/exec"
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
	out, err := exec.CommandContext(t.Context(), "go", "list", "-json", parse+"/parsememory").Output()
	require.NoError(t, err)
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
