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
//	named["primary"].port  a map entry: its key, always quoted as a Go string
//	limits["/upload"]
//	items[*].port          any element or entry, in a schema pattern
//
// Quoting every map key keeps a key holding ".", "[", "]" or a quote — or
// one that looks like an index — from reading as structure.

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
	var b strings.Builder
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

func (p keyPath) index(i int) keyPath {
	return append(slices.Clip(p), segment{kind: segIndex, index: i})
}

func (p keyPath) any() keyPath {
	return append(slices.Clip(p), segment{kind: segAny})
}

// parseKeyPath parses a key in the grammar above. Wildcards ([*]) are
// accepted; a caller that needs a concrete key rejects them.
func parseKeyPath(s string) (keyPath, error) {
	var p keyPath
	rest := s
	wantField := true // a key starts with a field, and so does each segment after "."
	for rest != "" {
		if rest[0] == '[' {
			if wantField {
				return nil, fmt.Errorf("%w %q: a selector must follow a field", ErrKey, s)
			}
			seg, n, err := parseSelector(rest)
			if err != nil {
				return nil, fmt.Errorf("%w %q: %w", ErrKey, s, err)
			}
			p = append(p, seg)
			rest = rest[n:]
		} else {
			if !wantField {
				return nil, fmt.Errorf("%w %q: expected \".\" or \"[\" after %q", ErrKey, s, p.String())
			}
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			name := rest[:end]
			if !validFieldKey(name) {
				return nil, fmt.Errorf("%w %q: %q is not a field key", ErrKey, s, name)
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

// validFieldKey reports whether name can be a struct field's key segment: it
// must not be empty or hold a character the grammar uses for structure.
func validFieldKey(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r == '.', r == '[', r == ']', r == '\\', r == '*', r == '"', unicode.IsControl(r):
			return false
		}
	}
	return true
}
