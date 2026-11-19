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

// The README's usage table is ExampleUsageFlags's checked output.
func TestREADMEUsageTableIsTheExampleOutput(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	require.NoError(t, err)
	examples, err := os.ReadFile("example_test.go")
	require.NoError(t, err)

	_, example, ok := strings.Cut(string(examples), "func ExampleUsageFlags() {")
	require.True(t, ok, "example_test.go has no ExampleUsageFlags")
	_, output, ok := strings.Cut(example, "\t// Output:\n")
	require.True(t, ok, "ExampleUsageFlags has no output")
	output, _, _ = strings.Cut(output, "\n}")
	var want []string
	for line := range strings.Lines(output) {
		want = append(want, strings.TrimPrefix(strings.TrimRight(line, "\n"), "\t// "))
	}

	_, section, ok := strings.Cut(string(readme), "\n## Documenting the settings\n")
	require.True(t, ok, "README has no usage section")
	_, rest, ok := strings.Cut(section, "```txt\n")
	require.True(t, ok, "the usage section has no txt block")
	block, _, ok := strings.Cut(rest, "\n```\n")
	require.True(t, ok, "unterminated txt block")
	assert.Equal(t, strings.Join(want, "\n"), block)
}
