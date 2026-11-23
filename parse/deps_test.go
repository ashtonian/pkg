//go:build unit

package parse_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The packages a server compiles whatever its transport link no HTTP stack:
// net/http, and crypto/tls with it, belong to parsehttp.
func TestTransportFreePackages_ImportNoNetHTTP(t *testing.T) {
	for _, pkg := range []string{
		"github.com/ashtonian/pkg/parse",
		"github.com/ashtonian/pkg/parse/core",
		"github.com/ashtonian/pkg/parse/filter",
		"github.com/ashtonian/pkg/parse/patch",
		"github.com/ashtonian/pkg/parse/client",
	} {
		out, err := exec.Command("go", "list", "-deps", pkg).Output()
		require.NoError(t, err, pkg)
		deps := strings.Fields(string(out))
		require.Contains(t, deps, pkg, "go list named the package")
		require.False(t, slices.Contains(deps, "net/http"), "%s imports net/http", pkg)
	}
}
