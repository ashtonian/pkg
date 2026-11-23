//go:build unit

package oauth2_test

import (
	"os"
	"strings"
	"testing"
)

// The README Quick Start's Go block is example_integration_test.go
// verbatim, so the module's integration lane compiles and runs exactly what
// the README shows.
func TestREADMEQuickStartIsTheExample(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile("example_integration_test.go")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(readme), "## Quick Start\n")
	if !ok {
		t.Fatal("README has no Quick Start section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	_, rest, ok := strings.Cut(section, "```go\n")
	if !ok {
		t.Fatal("README Quick Start has no go block")
	}
	block, _, ok := strings.Cut(rest, "\n```\n")
	if !ok {
		t.Fatal("unterminated Quick Start block")
	}
	if strings.TrimSpace(block) != strings.TrimSpace(string(example)) {
		t.Fatal("README Quick Start differs from example_integration_test.go; copy the file into the README")
	}
}
