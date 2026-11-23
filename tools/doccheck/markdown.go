package main

import (
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// mdDoc is what the checks read of one Markdown file, outside fenced code
// blocks: its inline code spans, links, headings with their anchors, and
// tables by the section they sit in.
type mdDoc struct {
	path     string // relative to the repository root
	spans    []mdSpan
	fenced   []string // the lines of fenced code blocks
	links    []mdLink
	headings []mdHeading
	anchors  map[string]bool
	rows     []mdRow
}

type mdSpan struct {
	text string
	line int
}

type mdLink struct {
	target string
	line   int
}

type mdHeading struct {
	level int
	text  string
	line  int
}

// mdRow is a table body row: its cells' code spans, and the headings of
// the sections it is in, outermost first.
type mdRow struct {
	sections []string
	cells    [][]string
	line     int
}

var (
	headingLine  = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$`)
	fenceLine    = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	inlineLink   = regexp.MustCompile(`\]\(\s*(<[^>]*>|[^)\s]+)(?:\s+"[^"]*")?\s*\)`)
	refDef       = regexp.MustCompile(`^ {0,3}\[[^\]]+\]:\s*(<[^>]*>|\S+)`)
	htmlAnchor   = regexp.MustCompile(`<a\s[^>]*\b(?:id|name)="([^"]+)"`)
	tableDivider = regexp.MustCompile(`^\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$`)
	headingLink  = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	htmlTag      = regexp.MustCompile(`<[^>]+>`)
)

func parseMarkdown(path, text string) *mdDoc {
	doc := &mdDoc{path: path, anchors: map[string]bool{}}
	slugs := map[string]int{}
	var fence string
	var sections []string // heading text by level - 1
	inTable := false
	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		if fence != "" {
			if m := fenceLine.FindStringSubmatch(line); m != nil && m[1][0] == fence[0] && len(m[1]) >= len(fence) && strings.TrimSpace(line[strings.Index(line, m[1])+len(m[1]):]) == "" {
				fence = ""
				continue
			}
			doc.fenced = append(doc.fenced, line)
			continue
		}
		if m := fenceLine.FindStringSubmatch(line); m != nil {
			fence = m[1]
			inTable = false
			continue
		}
		if m := headingLine.FindStringSubmatch(line); m != nil {
			h := mdHeading{level: len(m[1]), text: m[2], line: n}
			doc.headings = append(doc.headings, h)
			s := slug(h.text)
			if c := slugs[s]; c > 0 {
				doc.anchors[s+"-"+strconv.Itoa(c)] = true
			} else {
				doc.anchors[s] = true
			}
			slugs[s]++
			sections = append(sections[:min(len(sections), h.level-1)], h.text)
			inTable = false
			continue
		}
		for _, m := range htmlAnchor.FindAllStringSubmatch(line, -1) {
			doc.anchors[m[1]] = true
		}
		spans, rest := codeSpans(line)
		for _, s := range spans {
			doc.spans = append(doc.spans, mdSpan{text: s, line: n})
		}
		for _, m := range inlineLink.FindAllStringSubmatch(rest, -1) {
			doc.links = append(doc.links, mdLink{target: strings.Trim(m[1], "<>"), line: n})
		}
		if m := refDef.FindStringSubmatch(rest); m != nil {
			doc.links = append(doc.links, mdLink{target: strings.Trim(m[1], "<>"), line: n})
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case !strings.HasPrefix(trimmed, "|"):
			inTable = false
		case tableDivider.MatchString(trimmed):
			inTable = true
		case inTable:
			doc.rows = append(doc.rows, mdRow{sections: slices.Clone(sections), cells: rowCells(trimmed), line: n})
		}
	}
	return doc
}

// codeSpans returns a line's inline code spans, and the line with each span
// blanked so that what a span holds is not read as a link.
func codeSpans(line string) ([]string, string) {
	var spans []string
	out := []byte(line)
	for i := 0; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		run := 0
		for i+run < len(line) && line[i+run] == '`' {
			run++
		}
		open := strings.Repeat("`", run)
		end := -1
		for j := i + run; j < len(line); {
			k := strings.Index(line[j:], open)
			if k < 0 {
				break
			}
			k += j
			after := k + run
			if (k == 0 || line[k-1] != '`') && (after >= len(line) || line[after] != '`') {
				end = k
				break
			}
			j = after
			for j < len(line) && line[j] == '`' {
				j++
			}
		}
		if end < 0 {
			i += run
			continue
		}
		spans = append(spans, strings.TrimSpace(line[i+run:end]))
		for j := i; j < end+run; j++ {
			out[j] = ' '
		}
		i = end + run
	}
	return spans, string(out)
}

// rowCells splits a table row into cells and returns each cell's code spans.
func rowCells(row string) [][]string {
	row = strings.TrimSuffix(strings.TrimPrefix(row, "|"), "|")
	var cells [][]string
	for _, cell := range splitCells(row) {
		spans, _ := codeSpans(cell)
		cells = append(cells, spans)
	}
	return cells
}

// splitCells splits on the pipes that separate cells: not escaped, and not
// inside a code span.
func splitCells(row string) []string {
	var cells []string
	start, inCode := 0, 0
	for i := 0; i < len(row); i++ {
		switch {
		case row[i] == '\\':
			i++
		case row[i] == '`':
			run := 1
			for i+run < len(row) && row[i+run] == '`' {
				run++
			}
			if inCode == 0 {
				inCode = run
			} else if inCode == run {
				inCode = 0
			}
			i += run - 1
		case row[i] == '|' && inCode == 0:
			cells = append(cells, row[start:i])
			start = i + 1
		}
	}
	return append(cells, row[start:])
}

// slug is the anchor GitHub gives a heading: its text as rendered,
// lowercased, keeping letters, marks, numbers, connector punctuation,
// hyphens and spaces, each space becoming a hyphen.
func slug(heading string) string {
	text := headingLink.ReplaceAllString(heading, "$1")
	text = htmlTag.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "`", "")
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r) || unicode.Is(unicode.Pc, r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// splitTarget separates a link target into its path and its fragment, both
// decoded.
func splitTarget(target string) (string, string) {
	path, fragment, _ := strings.Cut(target, "#")
	path, _, _ = strings.Cut(path, "?")
	if p, err := url.PathUnescape(path); err == nil {
		path = p
	}
	if f, err := url.PathUnescape(fragment); err == nil {
		fragment = f
	}
	return path, fragment
}
