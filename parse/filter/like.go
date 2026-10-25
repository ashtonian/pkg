package filter

import (
	"errors"
	"strings"
)

// Pattern semantics shared by `$like` / `$ilike` on every backend
//: `%` matches any run of characters, `_` exactly one, and
// `\` makes the next character literal. `$like` is case-sensitive; `$ilike`
// folds case (SQLite folds ASCII letters only). `$contains` on a string is a
// literal, case-sensitive substring.

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

func likeTokens(pattern string) ([]likeToken, error) {
	toks := make([]likeToken, 0, len(pattern))
	escaped := false
	for _, r := range pattern {
		switch {
		case escaped:
			toks = append(toks, likeToken{kind: likeLiteral, r: r})
			escaped = false
		case r == '\\':
			escaped = true
		case r == '%':
			toks = append(toks, likeToken{kind: likeAny})
		case r == '_':
			toks = append(toks, likeToken{kind: likeOne})
		default:
			toks = append(toks, likeToken{kind: likeLiteral, r: r})
		}
	}
	if escaped {
		return nil, errLikeTrailingEscape
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

// MatchLike reports whether s matches the LIKE pattern, folding case when
// fold is set. It is the reference semantics of `$like` / `$ilike`; a
// malformed pattern matches nothing (the parser rejects it first).
func MatchLike(s, pattern string, fold bool) bool {
	toks, err := likeTokens(pattern)
	if err != nil {
		return false
	}
	str := []rune(s)
	si, ti := 0, 0
	star, mark := -1, 0
	for si < len(str) {
		switch {
		case ti < len(toks) && toks[ti].kind == likeAny:
			star, mark = ti, si
			ti++
		case ti < len(toks) && (toks[ti].kind == likeOne || runesEqual(toks[ti].r, str[si], fold)):
			si++
			ti++
		case star >= 0:
			ti = star + 1
			mark++
			si = mark
		default:
			return false
		}
	}
	for ti < len(toks) && toks[ti].kind == likeAny {
		ti++
	}
	return ti == len(toks)
}

func runesEqual(a, b rune, fold bool) bool {
	return a == b || fold && strings.EqualFold(string(a), string(b))
}
