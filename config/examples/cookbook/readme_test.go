//go:build unit

package cookbook

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// recipe marks a README block with the cookbook file it is taken from.
var recipe = regexp.MustCompile("<!-- recipe: ([a-z/]+\\.go) -->\n\n```go\n((?s:.*?))\n```\n")

// Every recipe block in the config README is a verbatim excerpt of the
// compiled, tested file it names, so the README shows only code that builds.
func TestREADMERecipesAreCompiledCode(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	blocks := recipe.FindAllStringSubmatch(string(readme), -1)
	if len(blocks) == 0 {
		t.Fatal("the README marks no recipe blocks")
	}
	for _, b := range blocks {
		file, block := b[1], b[2]
		src, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("README recipe names %s: %v", file, err)
			continue
		}
		if !strings.Contains(string(src), block) {
			t.Errorf("README recipe block is not an excerpt of %s; copy it from the file:\n%s", file, block)
		}
	}
}
