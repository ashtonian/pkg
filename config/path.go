package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// A key names one value of a configuration, in one grammar every output of
// this package uses:
//
//	db.host                a struct field: its key segment
//	items[0].port          a slice or array element: its decimal index
//	named["primary"].port  a map entry: its key's text, always quoted as a Go string
//	ports["80"]            an entry of a map keyed by any type with text (see keyCodec)
//	limits["/upload"]
//	items[*].port          any element or entry, in a schema pattern
//	["a.b"].port           a source key at the root no field's key can spell
//	extra.x, extra["a.b"]  a key beneath an interface, a remain field, or
//	                       a key no field reads: a member (see member)
//
// Quoting every map key keeps a key holding ".", "[", "]" or a quote — or
// one that looks like an index — from reading as structure. A key begins
// with a field, or with a quoted map key: the root is a struct, so a key
// that begins with one names a source key no field reads, as a report of
// unknown keys names it, and never an index or a wildcard.

type segKind uint8

const (
	segField segKind = iota // a struct field: db
	segKey                  // a map entry: ["primary"]
	segIndex                // a slice or array element: [0]
	segAny                  // any element or entry, in a pattern: [*]
)

type segment struct {
	kind  segKind
	name  string // segField, segKey
	index int    // segIndex
}

// keyPath is a key as typed segments. The empty path is the root.
type keyPath []segment

func (p keyPath) String() string {
	size := 0
	for _, s := range p {
		size += len(s.name) + 4 // a dot or brackets and quotes, an index's digits mostly
	}
	var b strings.Builder
	b.Grow(size)
	for i, s := range p {
		switch s.kind {
		case segField:
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString(s.name)
		case segKey:
			b.WriteByte('[')
			b.WriteString(strconv.Quote(s.name))
			b.WriteByte(']')
		case segIndex:
			b.WriteByte('[')
			b.WriteString(strconv.Itoa(s.index))
			b.WriteByte(']')
		case segAny:
			b.WriteString("[*]")
		}
	}
	return b.String()
}

// orRoot is p's text, or "$" for the root, so a message never names an empty
// key.
func (p keyPath) orRoot() string {
	if len(p) == 0 {
		return "$"
	}
	return p.String()
}

// field, key, index and any return p extended by one segment; p's backing
// array is never shared.
func (p keyPath) field(name string) keyPath {
	return append(slices.Clip(p), segment{kind: segField, name: name})
}

func (p keyPath) key(k string) keyPath {
	return append(slices.Clip(p), segment{kind: segKey, name: k})
}

// member is p extended by a member: a key no schema types, so neither a
// field nor a map entry it declares — one beneath an interface, one a
// remain field collects, a source key no field reads. A member is a field
// wherever its name can be one and a quoted map key otherwise, so data
// beneath an interface reads as a file writes it: extra.x, extra["a.b"].
func (p keyPath) member(name string) keyPath {
	return append(slices.Clip(p), memberSegment(name))
}

func memberSegment(name string) segment {
	if validFieldKey(name) {
		return segment{kind: segField, name: name}
	}
	return segment{kind: segKey, name: name}
}

// entrySegment names a map entry: a member beneath an interface, otherwise
// the quoted key a schema's map declares.
func entrySegment(name string, beneathInterface bool) segment {
	if beneathInterface {
		return memberSegment(name)
	}
	return segment{kind: segKey, name: name}
}

func (p keyPath) entry(name string, beneathInterface bool) keyPath {
	return append(slices.Clip(p), entrySegment(name, beneathInterface))
}

func (p keyPath) index(i int) keyPath {
	return append(slices.Clip(p), segment{kind: segIndex, index: i})
}

// raw is p as a merged tree keys it: a field or map key by its name, an
// index as its decimal text.
func (p keyPath) raw() []string {
	out := make([]string, len(p))
	for i, s := range p {
		if s.kind == segIndex {
			out[i] = strconv.Itoa(s.index)
		} else {
			out[i] = s.name
		}
	}
	return out
}

func (p keyPath) child(s segment) keyPath {
	return append(slices.Clip(p), s)
}

func (p keyPath) any() keyPath {
	return append(slices.Clip(p), segment{kind: segAny})
}

// isPattern reports whether p holds [*], so names no one key.
func (p keyPath) isPattern() bool {
	return slices.ContainsFunc(p, func(s segment) bool { return s.kind == segAny })
}

// keyList is keys for a message: in key order, each once, joined by ", ".
// A walk over map entries meets them in no particular order.
func keyList(keys []keyPath) string {
	sorted := slices.SortedFunc(slices.Values(keys), comparePaths)
	names := make([]string, 0, len(sorted))
	for _, p := range sorted {
		names = append(names, p.orRoot())
	}
	return strings.Join(slices.Compact(names), ", ")
}

// parseKeyPath parses a key in the grammar above. Wildcards ([*]) are
// accepted; a caller that needs a concrete key rejects them.
func parseKeyPath(s string) (keyPath, error) {
	var p keyPath
	rest := s
	wantField := true // a key starts with a field, and so does each segment after "."
	for rest != "" {
		if rest[0] == '[' {
			switch {
			case len(p) == 0 && !strings.HasPrefix(rest, `["`):
				return nil, fmt.Errorf("%w %q: a key begins with a field or a quoted map key", ErrKey, s)
			case len(p) > 0 && wantField:
				return nil, fmt.Errorf("%w %q: a selector must follow a field", ErrKey, s)
			}
			seg, n, err := parseSelector(rest)
			if err != nil {
				return nil, fmt.Errorf("%w %q: %w", ErrKey, s, err)
			}
			p = append(p, seg)
			rest, wantField = rest[n:], false
		} else {
			if !wantField {
				return nil, fmt.Errorf("%w %q: expected \".\" or \"[\" after %q", ErrKey, s, p.String())
			}
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			name := rest[:end]
			if problem := fieldKeyProblem(name); problem != "" {
				return nil, fmt.Errorf("%w %q: %q is not a field key: %s", ErrKey, s, name, problem)
			}
			p = append(p, segment{kind: segField, name: name})
			rest = rest[end:]
			wantField = false
		}
		if next, ok := strings.CutPrefix(rest, "."); ok {
			if next == "" {
				return nil, fmt.Errorf("%w %q: ends with \".\"", ErrKey, s)
			}
			rest, wantField = next, true
		}
	}
	if len(p) == 0 {
		return nil, fmt.Errorf("%w: empty key", ErrKey)
	}
	return p, nil
}

// parseSelector parses the bracketed selector s starts with, returning it and
// the bytes it took.
func parseSelector(s string) (segment, int, error) {
	switch {
	case strings.HasPrefix(s, "[*]"):
		return segment{kind: segAny}, 3, nil
	case strings.HasPrefix(s, `["`):
		quoted, err := strconv.QuotedPrefix(s[1:])
		if err != nil {
			return segment{}, 0, fmt.Errorf("unterminated map key")
		}
		name, err := strconv.Unquote(quoted)
		if err != nil {
			return segment{}, 0, err
		}
		n := 1 + len(quoted)
		if n >= len(s) || s[n] != ']' {
			return segment{}, 0, fmt.Errorf("map key %s is not followed by \"]\"", quoted)
		}
		return segment{kind: segKey, name: name}, n + 1, nil
	}
	end := strings.IndexByte(s, ']')
	if end < 0 {
		return segment{}, 0, fmt.Errorf("unterminated selector")
	}
	digits := s[1:end]
	i, err := strconv.Atoi(digits)
	if err != nil || i < 0 || digits != strconv.Itoa(i) {
		return segment{}, 0, fmt.Errorf("selector [%s] is neither an index, a quoted map key nor [*]", digits)
	}
	return segment{kind: segIndex, index: i}, end + 1, nil
}

// validFieldKey reports whether name can be a struct field's key segment.
func validFieldKey(name string) bool { return fieldKeyProblem(name) == "" }

// fieldKeyProblem is why name cannot be a struct field's key segment, or ""
// when it can: it is empty, is $, the name every message gives the root,
// or holds a character the grammar uses for structure.
func fieldKeyProblem(name string) string {
	switch name {
	case "":
		return "it is empty"
	case "$":
		return "$ names the root"
	}
	for _, r := range name {
		switch {
		case r == '.', r == '[', r == ']', r == '\\', r == '*', r == '"':
			return fmt.Sprintf("it holds %q, which keys use for structure", r)
		case unicode.IsControl(r):
			return "it holds a control character"
		}
	}
	return ""
}
