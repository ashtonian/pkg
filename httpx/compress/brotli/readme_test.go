//go:build unit

package brotli_test

import (
	"os"
	"strings"
	"testing"
)

// The README Quick Start is example_test.go verbatim, so the module's unit
// lane compiles and runs exactly what the README shows.
func TestREADMEQuickStartIsTheExample(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile("example_test.go")
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
	if strings.TrimSpace(block) != strings.TrimSpace(string(example)) {
		t.Fatal("README Quick Start differs from example_test.go; copy the file into the README")
	}
}
