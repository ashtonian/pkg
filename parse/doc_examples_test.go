//go:build unit

package parse_test

import (
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
		for _, b := range fencedBlocks(string(raw)) {
			if b.example == "" {
				if b.lang != "go" && compilerOutput.MatchString(b.body) {
					t.Errorf("%s:%d: a block showing compiler output names no example: precede it with <!-- output of ExampleName -->", guide, b.line)
				}
				continue
			}
			output, ok := outputs[b.example]
			if !assert.True(t, ok, "%s:%d: %s is no example of this package with an Output comment", guide, b.line, b.example) {
				continue
			}
			assert.Regexp(t, elided(b.body), output, "%s:%d: the block is not %s's output, with … for any run of characters; the example prints:\n%s", guide, b.line, b.example, output)
		}
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
