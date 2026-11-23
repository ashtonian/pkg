//go:build unit

package parse_test

import (
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exampleMarker precedes a fenced block that shows what an example prints:
//
//	<!-- output of Example_jsonDocuments -->
//
// The block is that example's Output under one elision rule: "…" stands for
// any run of characters, line breaks included, and nothing else is elided.
// The example's own Output comment is checked by go test, so a change to
// what the compiler prints fails the example, then the guide.
var exampleMarker = regexp.MustCompile(`^<!-- output of (Example\w*) -->$`)

// compilerOutput is a line only compiler output holds: SQL, bound
// arguments' dialect labels, or a refused request's code.
var compilerOutput = regexp.MustCompile(`\b(SELECT|INSERT INTO|UPDATE|DELETE FROM|WHERE|SET)\b|^(postgres|sqlite)\b|\[E\d{4}\]`)

func TestGuideBlocksMatchTheirExamples(t *testing.T) {
	outputs := exampleOutputs(t)
	guides, err := filepath.Glob(filepath.Join("docs", "*.md"))
	require.NoError(t, err)
	for _, guide := range append([]string{"README.md"}, guides...) {
		raw, err := os.ReadFile(guide)
		require.NoError(t, err)
		for _, problem := range guideProblems(string(raw), outputs) {
			t.Errorf("%s:%s", guide, problem)
		}
	}
}

// guideProblems are a guide's blocks that show compiler output without
// naming an example, name an example outputs lacks, or are not its output.
func guideProblems(doc string, outputs map[string]string) []string {
	var problems []string
	for _, b := range fencedBlocks(doc) {
		if b.example == "" {
			if b.lang != "go" && compilerOutput.MatchString(b.body) {
				problems = append(problems, fmt.Sprintf("%d: a block showing compiler output names no example: precede it with <!-- output of ExampleName -->", b.line))
			}
			continue
		}
		output, ok := outputs[b.example]
		if !ok {
			problems = append(problems, fmt.Sprintf("%d: %s is no example of this package with an Output comment", b.line, b.example))
			continue
		}
		if !elided(b.body).MatchString(output) {
			problems = append(problems, fmt.Sprintf("%d: the block is not %s's output, with … for any run of characters; the example prints:\n%s", b.line, b.example, output))
		}
	}
	return problems
}

func TestGuideProblems(t *testing.T) {
	outputs := map[string]string{"Example_x": "{\"a\":1}\npostgres WHERE (a = $1) ORDER BY id [1]\nsqlite   WHERE (a = ?1) ORDER BY id [1]"}
	pinned := func(body string) string { return "Text.\n\n<!-- output of Example_x -->\n```text\n" + body + "\n```\n" }
	tests := []struct {
		name     string
		doc      string
		problems int
	}{
		{"the whole output", pinned("{\"a\":1}\npostgres WHERE (a = $1) ORDER BY id [1]\nsqlite   WHERE (a = ?1) ORDER BY id [1]"), 0},
		{"an elision within a line", pinned("{\"a\":1}\npostgres WHERE (a = $1) … [1]\nsqlite   WHERE (a = ?1) … [1]"), 0},
		{"an elision across lines", pinned("{\"a\":1}\npostgres WHERE (a = $1) …"), 0},
		{"trailing spaces", pinned("{\"a\":1}  \npostgres … [1]\t"), 0},
		{"a block that stops short", pinned("{\"a\":1}\npostgres WHERE (a = $1) ORDER BY id [1]"), 1},
		{"a block that starts late", pinned("postgres WHERE (a = $1) …"), 1},
		{"a changed character", pinned("{\"a\":1}\npostgres WHERE (a = $2) …"), 1},
		{"metacharacters are literal", pinned("{.\"a\":1}\npostgres WHERE (a = $1) …"), 1},
		{"an example the package lacks", "<!-- output of Example_y -->\n```text\nx\n```\n", 1},
		{"compiler output with no marker", "```text\npostgres WHERE (a = $1)\n```\n", 1},
		{"a marker a line away from its block", "<!-- output of Example_x -->\n\n```sql\nSELECT 1\n```\n", 1},
		{"a refused request with no marker", "```text\n[E2071] field 'x' is not filterable\n```\n", 1},
		{"Go code is not output", "```go\nq := `SELECT 1 WHERE x`\n```\n", 0},
		{"requests are not output", "```text\n{\"tags\":{\"$some\":{}}}\n```\n", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Len(t, guideProblems(tt.doc, outputs), tt.problems)
		})
	}
}

type fencedBlock struct {
	line    int // of the opening fence, 1-based
	lang    string
	example string
	body    string
}

// fencedBlocks are a Markdown file's fenced code blocks, each with the
// example the line before its fence names.
func fencedBlocks(doc string) []fencedBlock {
	lines := strings.Split(doc, "\n")
	var blocks []fencedBlock
	for i := 0; i < len(lines); i++ {
		fence, ok := strings.CutPrefix(lines[i], "```")
		if !ok {
			continue
		}
		b := fencedBlock{line: i + 1, lang: strings.TrimSpace(fence)}
		if i > 0 {
			if m := exampleMarker.FindStringSubmatch(lines[i-1]); m != nil {
				b.example = m[1]
			}
		}
		var body []string
		for i++; i < len(lines) && !strings.HasPrefix(lines[i], "```"); i++ {
			body = append(body, lines[i])
		}
		b.body = strings.Join(body, "\n")
		blocks = append(blocks, b)
	}
	return blocks
}

// elided is the pattern a block's text stands for: itself, each "…" any run
// of characters. Trailing spaces are not compared, as go test compares an
// example's output.
func elided(block string) *regexp.Regexp {
	parts := strings.Split(trimLines(block), "…")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile(`\A` + strings.Join(parts, `(?s:.*?)`) + `\z`)
}

func trimLines(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n")
}

// exampleOutputs are this package's examples' Output comments, by function
// name, as go test reads them.
func exampleOutputs(t *testing.T) map[string]string {
	t.Helper()
	names, err := filepath.Glob("*_test.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range names {
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		require.NoError(t, err)
		files = append(files, f)
	}
	outputs := map[string]string{}
	for _, ex := range doc.Examples(files...) {
		if !ex.EmptyOutput && ex.Output != "" {
			outputs["Example"+ex.Name] = trimLines(ex.Output)
		}
	}
	return outputs
}
