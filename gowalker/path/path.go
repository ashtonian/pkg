// Package path provides immutable typed paths for gowalker. Use [Parse] for
// fields, quoted string map keys and indexes, such as Servers["primary"].Ports[0].
// Use [Of] with [Field], [Key], [Index] and [Deref] for exact key types, arbitrary
// field names, or explicit pointer/interface dereferencing. A zero [Path] selects
// the root. Paths do not support wildcards or implicit key conversion.
package path

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrSyntax          = errors.New("path: invalid syntax")
	ErrUnrepresentable = errors.New("path: no lossless text representation")
	ErrLimit           = errors.New("path: text exceeds 65536 bytes")
)

// MaxTextBytes bounds Parse input. Typed paths are bounded during resolution.
const MaxTextBytes = 65536

// Kind distinguishes path steps. The zero value is invalid.
type Kind uint8

const (
	FieldKind Kind = iota + 1
	KeyKind
	IndexKind
	DerefKind
)

// Step is a field name, a typed map key, a list index or an explicit dereference.
// A key retains its Go type and ordinary Go reference semantics.
type Step struct {
	kind  Kind
	name  string
	key   any
	index int
}

// Field selects a visible logical field name, including names outside text syntax.
func Field(name string) Step { return Step{kind: FieldKind, name: name} }

// Key retains the exact Go map-key type and ordinary reference semantics.
func Key(key any) Step { return Step{kind: KeyKind, key: key} }

// Index selects an existing slice or array element; it does not permit growth.
func Index(index int) Step { return Step{kind: IndexKind, index: index} }

// Deref selects the value inside one pointer or interface, including at a terminal.
func Deref() Step { return Step{kind: DerefKind} }

// Kind identifies which of Name, Key or Index describes the step.
func (s Step) Kind() Kind { return s.kind }

// Name returns the field name for FieldKind, or an empty string otherwise.
func (s Step) Name() string { return s.name }

// Key returns the typed map key for KeyKind, or nil otherwise.
func (s Step) Key() any { return s.key }

// Index returns the index for IndexKind, or zero otherwise.
func (s Step) Index() int { return s.index }

// Path is immutable. Its zero value selects the root.
type Path struct{ steps []Step }

// Of copies the steps into a path. Map keys retain their own reference semantics.
func Of(steps ...Step) Path { return Path{steps: append([]Step(nil), steps...)} }

// Len returns the number of explicit steps; the root path has length zero.
func (p Path) Len() int { return len(p.steps) }

// Steps returns a detached slice; modifying it does not change the path.
func (p Path) Steps() []Step { return append([]Step(nil), p.steps...) }

// Append returns a new path without changing p.
func (p Path) Append(steps ...Step) Path {
	result := make([]Step, 0, len(p.steps)+len(steps))
	result = append(result, p.steps...)
	return Path{steps: append(result, steps...)}
}

// MarshalText returns the lossless text form, or ErrUnrepresentable for steps
// the grammar cannot retain, including named-string keys and dereferences.
// Output longer than MaxTextBytes returns ErrLimit.
func (p Path) MarshalText() ([]byte, error) {
	var out strings.Builder
	for i, step := range p.steps {
		switch step.kind {
		case FieldKind:
			if !identifier(step.name) {
				return nil, ErrUnrepresentable
			}
			if i > 0 {
				out.WriteByte('.')
			}
			out.WriteString(step.name)
		case KeyKind:
			key, ok := step.key.(string)
			if !ok {
				return nil, ErrUnrepresentable
			}
			out.WriteByte('[')
			out.WriteString(strconv.Quote(key))
			out.WriteByte(']')
		case IndexKind:
			if step.index < 0 {
				return nil, ErrUnrepresentable
			}
			out.WriteByte('[')
			out.WriteString(strconv.Itoa(step.index))
			out.WriteByte(']')
		default:
			return nil, ErrUnrepresentable
		}
	}
	if out.Len() > MaxTextBytes {
		return nil, ErrLimit
	}
	return []byte(out.String()), nil
}

// Parse accepts fields, quoted string keys, and non-negative indexes, including
// root selectors: Servers["primary.eu"].Ports[0], ["key"].Name, or [0].Name.
// The empty string selects the root. Whitespace outside quoted keys is invalid.
func Parse(text string) (Path, error) {
	if len(text) > MaxTextBytes {
		return Path{}, ErrLimit
	}
	if !utf8.ValidString(text) {
		return Path{}, ErrSyntax
	}
	var steps []Step
	for i := 0; i < len(text); {
		if text[i] == '[' {
			i++
			if i >= len(text) {
				return Path{}, ErrSyntax
			}
			if text[i] == '"' {
				start := i
				i++
				for i < len(text) && text[i] != '"' {
					if text[i] == '\\' {
						i++
					}
					i++
				}
				if i >= len(text) {
					return Path{}, ErrSyntax
				}
				key, err := strconv.Unquote(text[start : i+1])
				if err != nil {
					return Path{}, ErrSyntax
				}
				steps = append(steps, Key(key))
				i++
			} else {
				start := i
				for i < len(text) && text[i] >= '0' && text[i] <= '9' {
					i++
				}
				index, err := strconv.Atoi(text[start:i])
				if err != nil {
					return Path{}, ErrSyntax
				}
				steps = append(steps, Index(index))
			}
			if i >= len(text) || text[i] != ']' {
				return Path{}, ErrSyntax
			}
			i++
		} else {
			start := i
			for i < len(text) && text[i] != '.' && text[i] != '[' {
				i++
			}
			name := text[start:i]
			if !identifier(name) {
				return Path{}, ErrSyntax
			}
			steps = append(steps, Field(name))
		}
		if i == len(text) {
			break
		}
		switch text[i] {
		case '[':
		case '.':
			i++
			if i == len(text) || text[i] == '[' {
				return Path{}, ErrSyntax
			}
		default:
			return Path{}, ErrSyntax
		}
	}
	return Path{steps: steps}, nil
}

func identifier(name string) bool {
	if name == "" || !utf8.ValidString(name) {
		return false
	}
	for i, r := range name {
		if r != '_' && !unicode.IsLetter(r) && !(i > 0 && unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}
