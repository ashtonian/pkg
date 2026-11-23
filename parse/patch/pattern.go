package patch

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// Wildcard tokens of a path pattern.
const (
	wildcardOne = "*"  // exactly one token
	wildcardAny = "**" // zero or more tokens
)

// pathPattern is a checked path pattern: a JSON Pointer whose reference
// tokens are each a literal, `*` (exactly one token) or `**` (zero or more
// tokens, wherever it stands). Tokens stay escaped, as paths compare them, so
// an empty key is a token like any other.
type pathPattern struct {
	text   string
	tokens []string
}

// parsePattern checks a path pattern: a JSON Pointer (the leading slash may
// be left out) whose tokens are each a literal, `*` or `**`. A token that only
// contains `*` would be compared literally and match nothing a glob would,
// so it is refused rather than left to match silently.
func parsePattern(text string) (pathPattern, error) {
	if text == "" {
		return pathPattern{}, fmt.Errorf("pattern %q names no path", text)
	}
	if _, err := core.ParsePointer("/" + strings.TrimPrefix(text, "/")); err != nil {
		return pathPattern{}, fmt.Errorf("pattern %q: %w", text, err)
	}
	tokens := pointerTokens(text)
	for _, tok := range tokens {
		if strings.Contains(tok, wildcardOne) && !isWildcardToken(tok) {
			return pathPattern{}, fmt.Errorf("pattern %q: token %q is neither a literal nor * (one token) nor ** (any number); a token cannot hold * among other characters", text, tok)
		}
	}
	return pathPattern{text: text, tokens: tokens}, nil
}

// matches reports whether the pattern matches path's tokens.
func (p pathPattern) matches(path []string) bool {
	return slices.Contains(p.states(path), len(p.tokens))
}

// matchesBelow reports whether the pattern matches a path strictly below
// path: a write or read of path whole reaches such a path too.
func (p pathPattern) matchesBelow(path []string) bool {
	for _, s := range p.states(path) {
		// Any token left, a literal, * or **, matches at least one more.
		if s < len(p.tokens) {
			return true
		}
	}
	return false
}

// reaches reports whether the pattern matches path or a path below it.
func (p pathPattern) reaches(path []string) bool { return len(p.states(path)) > 0 }

// remainders are the patterns a path below path must still match, one per
// way the pattern can have matched path's tokens: suffixes of the pattern.
func (p pathPattern) remainders(path []string) [][]string {
	var out [][]string
	for _, s := range p.states(path) {
		if s < len(p.tokens) {
			out = append(out, p.tokens[s:])
		}
	}
	return out
}

// states are the pattern positions a match can stand at once path's tokens
// are consumed, in ascending order: the pattern simulated as an automaton,
// in time proportional to the product of the two lengths however many `**`
// the pattern holds.
func (p pathPattern) states(path []string) []int {
	reached := make([]bool, len(p.tokens)+1)
	reached[0] = true
	p.close(reached)
	for _, tok := range path {
		next := make([]bool, len(reached))
		alive := false
		for s, on := range reached {
			if !on || s == len(p.tokens) {
				continue
			}
			switch p.tokens[s] {
			case wildcardAny:
				next[s], alive = true, true // ** takes this token and may take more
			case wildcardOne, tok:
				next[s+1], alive = true, true
			}
		}
		if !alive {
			return nil
		}
		p.close(next)
		reached = next
	}
	var out []int
	for s, on := range reached {
		if on {
			out = append(out, s)
		}
	}
	return out
}

// close adds the positions a `**` reaches by taking no token.
func (p pathPattern) close(reached []bool) {
	for s := range p.tokens {
		if reached[s] && p.tokens[s] == wildcardAny {
			reached[s+1] = true
		}
	}
}

// compareSpecificity orders patterns most specific first: more leading
// literal tokens, then fewer `**`, then fewer `*`, then more tokens, then by
// text, so selection among overlapping patterns never depends on map order.
func compareSpecificity(a, b pathPattern) int {
	if c := cmp.Compare(b.literalPrefix(), a.literalPrefix()); c != 0 {
		return c
	}
	if c := cmp.Compare(a.count(wildcardAny), b.count(wildcardAny)); c != 0 {
		return c
	}
	if c := cmp.Compare(a.count(wildcardOne), b.count(wildcardOne)); c != 0 {
		return c
	}
	if c := cmp.Compare(len(b.tokens), len(a.tokens)); c != 0 {
		return c
	}
	return strings.Compare(a.text, b.text)
}

// literalPrefix is the number of literal tokens before the first wildcard.
func (p pathPattern) literalPrefix() int {
	if i := slices.IndexFunc(p.tokens, isWildcardToken); i >= 0 {
		return i
	}
	return len(p.tokens)
}

func (p pathPattern) count(wildcard string) int {
	n := 0
	for _, tok := range p.tokens {
		if tok == wildcard {
			n++
		}
	}
	return n
}

// isWildcard reports a pattern with a wildcard token, which matches by
// pattern rather than by its own text.
func (p pathPattern) isWildcard() bool { return slices.ContainsFunc(p.tokens, isWildcardToken) }

func isWildcardToken(t string) bool { return t == wildcardOne || t == wildcardAny }

// pointerTokens are a JSON Pointer's or pattern's reference tokens, still
// escaped: "" (the whole document) has none, "/" one empty token, and "/a//b"
// the three tokens "a", "" and "b". A leading slash is optional.
func pointerTokens(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimPrefix(s, "/"), "/")
}

// joinPointer is the JSON Pointer of escaped tokens.
func joinPointer(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	return "/" + strings.Join(tokens, "/")
}
