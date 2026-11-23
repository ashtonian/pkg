package main

import (
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// generated is the header go generate tools write; their comments are not
// the module's prose.
var generated = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)

// checkComments holds every line comment of the module's Go files, tests
// included, to whole sentences: a line that opens on a stray `.`, `,`, `;`
// or `:`, or ends in a lone full stop, is what removing a reference from
// the middle of a sentence leaves.
func (m *module) checkComments(root string) ([]finding, error) {
	var findings []finding
	for _, rel := range m.goFiles {
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", rel, err)
		}
		if generated.Match(src) {
			continue
		}
		fset := token.NewFileSet()
		file := fset.AddFile(rel, -1, len(src))
		var s scanner.Scanner
		// Syntax errors are the compiler's to report; the scan goes on.
		s.Init(file, src, func(token.Position, string) {}, scanner.ScanComments)
		for {
			pos, tok, lit := s.Scan()
			if tok == token.EOF {
				break
			}
			if tok != token.COMMENT || !strings.HasPrefix(lit, "//") {
				continue
			}
			if why := commentFragment(strings.TrimPrefix(lit, "//")); why != "" {
				findings = append(findings, finding{path: rel, line: fset.Position(pos).Line, msg: why})
			}
		}
	}
	return findings, nil
}

// commentFragment says why the text of a line comment, after its slashes,
// is a piece of a sentence, or returns "" when it is not. The mark a line
// opens on stands alone, followed by a space or nothing: a line may open on
// a file suffix or a path (.json, ..data). An indented line is code, as in
// a doc comment's code block.
func commentFragment(text string) string {
	t, _ := strings.CutPrefix(text, " ")
	if t == "" || t[0] == ' ' || t[0] == '\t' {
		return ""
	}
	if strings.ContainsRune(".,;:", rune(t[0])) && (len(t) == 1 || t[1] == ' ') {
		return fmt.Sprintf("a comment line opens on %q: a piece of its sentence is missing", t[:1])
	}
	if strings.HasSuffix(t, " .") {
		return "a comment line ends in a lone full stop: a piece of its sentence is missing"
	}
	return ""
}
