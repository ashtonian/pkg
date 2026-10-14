package kongprovider

import (
	"os"
	"strings"
	"testing"
)

// The README Quick Start is examples/cli/main.go verbatim, so the
// module build compiles exactly what the README shows.
func TestREADMEQuickStartIsTheCompiledExample(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.ReadFile("examples/cli/main.go")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(readme), "## Quick Start\n\n```go\n")
	if !ok {
		t.Fatal("README has no Quick Start go block")
	}
	block, _, ok := strings.Cut(rest, "\n```\n")
	if !ok {
		t.Fatal("unterminated Quick Start block")
	}
	if strings.TrimSpace(block) != strings.TrimSpace(string(program)) {
		t.Fatal("README Quick Start differs from examples/cli/main.go; copy the program into the README")
	}
}
