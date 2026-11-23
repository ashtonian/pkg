package cookbook

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// makeVariable is a variable the repository's Makefile declares for the
// cookbook, which a caller may set.
var makeVariable = regexp.MustCompile(`(?m)^(COOKBOOK_[A-Z_]+) \?=`)

// Every Make variable the repository's Makefile declares for the cookbook
// is in this README's tables.
func TestREADMENamesEveryMakeVariable(t *testing.T) {
	makefile, err := os.ReadFile("../../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	declared := makeVariable.FindAllStringSubmatch(string(makefile), -1)
	if len(declared) == 0 {
		t.Fatal("the Makefile declares no COOKBOOK_ variable")
	}
	for _, d := range declared {
		if !strings.Contains(string(readme), "`"+d[1]+"`") {
			t.Errorf("README names no Make variable %s", d[1])
		}
	}
}

// recipe marks a README block with the cookbook file it is taken from.
var recipe = regexp.MustCompile("<!-- recipe: ([a-z/]+\\.go) -->\n\n```go\n((?s:.*?))\n```\n")

// Every recipe block in this README is a verbatim excerpt of the
// compiled, tested file it names, so the README shows only code that builds.
func TestREADMERecipesAreCompiledCode(t *testing.T) {
	readme, err := os.ReadFile("README.md")
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
