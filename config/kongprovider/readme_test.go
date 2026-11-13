package kongprovider

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The README's programs are the example programs verbatim, so the module
// build compiles exactly what the README shows.
func TestREADMEProgramsAreTheCompiledExamples(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	require.NoError(t, err)
	for _, tt := range []struct {
		heading, program string
	}{
		{"## Quick start", "examples/cli/main.go"},
		{"## Reloading", "examples/reload/main.go"},
	} {
		t.Run(tt.program, func(t *testing.T) {
			program, err := os.ReadFile(tt.program)
			require.NoError(t, err)
			_, section, ok := strings.Cut(string(readme), "\n"+tt.heading+"\n")
			require.True(t, ok, "README has no %q section", tt.heading)
			_, rest, ok := strings.Cut(section, "```go\n")
			require.True(t, ok, "%q has no go block", tt.heading)
			block, _, ok := strings.Cut(rest, "\n```\n")
			require.True(t, ok, "unterminated go block under %q", tt.heading)
			assert.Equal(t, strings.TrimSpace(string(program)), strings.TrimSpace(block),
				"the README block under %q differs from %s; copy the program into the README", tt.heading, tt.program)
		})
	}
}
