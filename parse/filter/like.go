package filter

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Pattern semantics shared by `$like` / `$ilike` on every backend: `%`
// matches any run of characters, `_` exactly one, and `\` makes the next
// character literal. `$like` is case-sensitive. `$ilike` compares each
// character lower-cased (unicode.ToLower), as PostgreSQL's ILIKE does under
// pg_c_utf8: ſ is no s and ς no σ, while İ is i. SQLite, and a PostgreSQL
// column under the C collation, lower-case ASCII letters only. `$contains`
// on a string is a literal, case-sensitive substring.

// likeEscaper makes a literal safe inside a LIKE pattern.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// EscapeLike returns s as a `$like` pattern matching s literally; append `%`
// for a prefix match.
func EscapeLike(s string) string { return likeEscaper.Replace(s) }

// errLikeTrailingEscape rejects a pattern PostgreSQL would reject at
// execution ("LIKE pattern must not end with escape character").
var errLikeTrailingEscape = errors.New(`pattern ends with the escape character \`)

type likeTokenKind uint8

const (
	likeLiteral likeTokenKind = iota
	likeOne                   // _
	likeAny                   // %
)

type likeToken struct {
	kind likeTokenKind
	r    rune
}

// likeAt is the token of pattern at byte i and its width in bytes, an
// escape and the character it makes literal counted together; ok is false
// for an escape that ends the pattern.
func likeAt(pattern string, i int) (tok likeToken, width int, ok bool) {
	r, w := utf8.DecodeRuneInString(pattern[i:])
	switch {
	case r == '\\' && i+w == len(pattern):
		return tok, 0, false
	case r == '\\':
		literal, lw := utf8.DecodeRuneInString(pattern[i+w:])
		tok, w = likeToken{kind: likeLiteral, r: literal}, w+lw
	case r == '%':
		tok = likeToken{kind: likeAny}
	case r == '_':
		tok = likeToken{kind: likeOne}
	default:
		tok = likeToken{kind: likeLiteral, r: r}
	}
	return tok, w, true
}

// checkLike refuses a pattern PostgreSQL would refuse.
func checkLike(pattern string) error {
	for i := 0; i < len(pattern); {
		_, w, ok := likeAt(pattern, i)
		if !ok {
			return errLikeTrailingEscape
		}
		i += w
	}
	return nil
}

func likeTokens(pattern string) ([]likeToken, error) {
	toks := make([]likeToken, 0, len(pattern))
	for i := 0; i < len(pattern); {
		tok, w, ok := likeAt(pattern, i)
		if !ok {
			return nil, errLikeTrailingEscape
		}
		toks = append(toks, tok)
		i += w
	}
	return toks, nil
}

// likeToGlob translates a LIKE pattern into the equivalent SQLite GLOB
// pattern, which — unlike SQLite's LIKE — is case-sensitive.
func likeToGlob(pattern string) (string, error) {
	toks, err := likeTokens(pattern)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, t := range toks {
		switch t.kind {
		case likeAny:
			b.WriteByte('*')
		case likeOne:
			b.WriteByte('?')
		default:
			switch t.r {
			case '*', '?', '[':
				b.WriteByte('[')
				b.WriteRune(t.r)
				b.WriteByte(']')
			default:
				b.WriteRune(t.r)
			}
		}
	}
	return b.String(), nil
}

// MatchLike reports whether s matches the LIKE pattern, comparing each
// character lower-cased when fold is set: PostgreSQL's ILIKE under
// pg_c_utf8, not Unicode case folding. It is the reference semantics of
// `$like` / `$ilike`; a malformed pattern matches nothing (the parser
// rejects it first). It reads the pattern in place, since an evaluator
// matches one pattern against every row it reads.
func MatchLike(s, pattern string, fold bool) bool {
	if checkLike(pattern) != nil {
		return false
	}
	si, pi := 0, 0
	// After a %, star is the pattern's next token and mark the first
	// character of s it has not yet absorbed: a mismatch absorbs one more.
	star, mark := -1, 0
	for si < len(s) {
		sr, sw := utf8.DecodeRuneInString(s[si:])
		if pi < len(pattern) {
			tok, pw, _ := likeAt(pattern, pi)
			if tok.kind == likeAny {
				star, mark = pi+pw, si
				pi += pw
				continue
			}
			if tok.kind == likeOne || runesEqual(tok.r, sr, fold) {
				si += sw
				pi += pw
				continue
			}
		}
		if star < 0 {
			return false
		}
		_, mw := utf8.DecodeRuneInString(s[mark:])
		mark += mw
		si, pi = mark, star
	}
	for pi < len(pattern) {
		tok, pw, _ := likeAt(pattern, pi)
		if tok.kind != likeAny {
			return false
		}
		pi += pw
	}
	return true
}

func runesEqual(a, b rune, fold bool) bool {
	return a == b || fold && unicode.ToLower(a) == unicode.ToLower(b)
}
